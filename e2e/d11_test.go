//go:build e2e

package e2e

import (
	"testing"

	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

func TestD11_ConfigurationVariants(t *testing.T) {
	t.Run("native schemas and denial", func(t *testing.T) {
		provider := providertest.NewScripted(t,
			providertest.NativeToolCallStep("d11-read", "read", map[string]any{"paths": []string{"README.md"}}),
			loadApplyPatch("d11-load"),
			providertest.NativeToolCallStep("d11-write", "apply_patch", map[string]any{"patch": "*** Begin Patch\n*** Add File: .hidden-d11\n+x\n*** End Patch"}),
			providertest.ReplyStep("done"),
		)
		defer provider.Close()
		home := newHome(t, provider.URL())
		got := runInProc(t, home, provider.URL(), "native mode", denyAll)
		if got.Resp.ToolCallCount != 3 || len(got.failureKinds()) != 1 || got.failureKinds()[0].Kind != "denied" {
			t.Fatalf("response=%#v failures=%#v", got.Resp, got.failureKinds())
		}
		if provider.Snapshot().CompletionRequests[0].Tools == nil {
			t.Fatal("native mode sent no tool schemas")
		}
	})

	t.Run("environment disables native requests", func(t *testing.T) {
		provider := providertest.NewScripted(t,
			providertest.ToolCallStep("read", `{"paths":["README.md"]}`),
			providertest.ReplyStep("done"),
		)
		defer provider.Close()
		home := newHome(t, provider.URL())
		result := runExecEnv(t, home, provider.URL(), "tagged via env", map[string]string{"INVOKR_NATIVE_TOOL_REQUESTS_DISABLED": "true"})
		if result.Err != nil || len(parseLedger(t, result.Stdout).failures()) != 0 {
			t.Fatalf("result=%v stderr=%s", result.Err, result.Stderr)
		}
		if provider.Snapshot().CompletionRequests[0].Tools != nil {
			t.Fatal("env-disabled native mode still sent schemas")
		}
	})

	t.Run("configured tagged mode", func(t *testing.T) {
		provider := providertest.NewScripted(t,
			providertest.ToolCallStep("read", `{"paths":["README.md"]}`),
			providertest.ReplyStep("done"),
		)
		defer provider.Close()
		home := newHome(t, provider.URL())
		home.app.Agent.NativeToolCalling = false
		home.app.Agent.NativeToolRequestsDisabled = true
		got := runInProc(t, home, provider.URL(), "configured tagged", grantAll)
		if got.Resp.ToolCallCount != 1 || len(got.Resp.ToolFailures) != 0 {
			t.Fatalf("response=%#v", got.Resp)
		}
		if provider.Snapshot().CompletionRequests[0].Tools != nil {
			t.Fatal("tagged mode sent native schemas")
		}
	})
}
