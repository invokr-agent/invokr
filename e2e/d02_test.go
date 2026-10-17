//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/support/testutil"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

func TestD02_ToolboxSandboxCapabilityMatrix(t *testing.T) {
	if exec.Command("unshare", "--user", "--map-root-user", "true").Run() != nil {
		t.Skip("BLOCKED: user namespaces unavailable")
	}

	networkFixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("d02-network-ok")) }))
	defer networkFixture.Close()
	home := newHome(t, "")
	rewriteConfig(t, &home, func(app *config.AppConfig) {
		app.Toolbox.ExecTimeoutMS = 500
		app.Toolbox.MaxOutputBytes = 128
	})
	marker := filepath.Join(home.workspace, "d02-write-marker")
	hostMarker := "/tmp/invokr-d02-host-marker"
	_ = os.Remove(hostMarker)
	t.Cleanup(func() { _ = os.Remove(hostMarker) })

	probes := []d02Probe{
		{"d02_read_denied", py(`open(os.environ["INVOKR_WORKING_DIR"]+"/README.md").read()`), false, false, false},
		{"d02_write_denied", py(fmt.Sprintf(`open(%q,"w").write("bad")`, marker)), false, false, false},
		{"d02_write_allowed", py(fmt.Sprintf(`open(%q,"w").write("d02-ok")`, marker)), true, true, false},
		{"d02_passwd", py(`open("/etc/passwd").read()`), false, false, false},
		{"d02_env", py(`os.environ.get("INVOKR_TEST_SECRET","")`), false, false, false},
		{"d02_net_denied", py(fmt.Sprintf(`urllib.request.urlopen(%q).read().decode()`, networkFixture.URL)), false, false, false},
		{"d02_net_allowed", py(fmt.Sprintf(`urllib.request.urlopen(%q).read().decode()`, networkFixture.URL)), false, false, true},
		{"d02_host_tmp", py(fmt.Sprintf(`open(%q,"w").write("escape")`, hostMarker)), false, false, false},
		{"d02_timeout", "import time\ntime.sleep(2)\nprint('{}')\n", false, false, false},
		{"d02_output", "print('x'*4096)\n", false, false, false},
	}
	calls := make([]providertest.CompletionToolCall, 0, len(probes))
	loads := make([]providertest.CompletionToolCall, 0, len(probes))
	for _, probe := range probes {
		d02WritePlugin(t, home.state.ToolboxDir, probe)
		loads = append(loads, providertest.CompletionToolCall{ID: "load-" + probe.name, Type: "function", Function: providertest.CompletionToolFunction{Name: "tools", Arguments: map[string]any{"action": "enable", "names": []string{probe.name}}}})
		calls = append(calls, providertest.CompletionToolCall{ID: probe.name, Type: "function", Function: providertest.CompletionToolFunction{Name: "tools", Arguments: map[string]any{"action": "run", "name": probe.name, "arguments": map[string]any{}}}})
	}
	provider := providertest.NewScripted(t, providertest.CompletionStep{ToolCalls: loads}, providertest.CompletionStep{ToolCalls: calls}, providertest.ReplyStep("d02 done"))
	defer provider.Close()

	result := runInProc(t, home, provider.URL(), "run sandbox probes", grantAll)
	if got := readFile(t, marker); got != "d02-ok" {
		t.Fatalf("declared workspace write = %q", got)
	}
	if _, err := os.Stat(hostMarker); !os.IsNotExist(err) {
		t.Fatalf("sandbox wrote host marker: %v", err)
	}
	snapshot, err := json.Marshal(provider.Snapshot().CompletionRequests)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"host-canary-91", "root:"} {
		if strings.Contains(string(snapshot), secret) || strings.Contains(result.Resp.Message, secret) {
			t.Fatalf("sandbox leaked %q", secret)
		}
	}
	if !strings.Contains(string(snapshot), "d02-network-ok") {
		t.Fatalf("declared network probe did not reach fixture")
	}
	if !strings.Contains(string(snapshot), "output truncated") {
		t.Fatalf("missing truncation marker in provider requests")
	}
	if len(result.Approvals) != 2 {
		t.Fatalf("approvals=%d, want declared write and network only", len(result.Approvals))
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}

type d02Probe struct {
	name, source                        string
	writeTier, writesWorkspace, network bool
}

func py(expression string) string {
	return "import json,os,urllib.request\ntry:\n r=" + expression + "\n print(json.dumps({'result':str(r)}))\nexcept Exception as e:\n print(json.dumps({'error':str(e)}))\n"
}

func d02WritePlugin(t *testing.T, root string, probe d02Probe) {
	t.Helper()
	dir := testutil.MustWriteToolboxPlugin(t, root, probe.name, "D02 sandbox probe", map[string]string{"main.py": probe.source})
	specPath := filepath.Join(dir, "tool.json")
	var spec map[string]any
	if err := json.Unmarshal([]byte(readFile(t, specPath)), &spec); err != nil {
		t.Fatal(err)
	}
	spec["capabilities"] = map[string]any{"reads_workspace": false, "writes_workspace": probe.writesWorkspace, "network": probe.network, "executes_code": true}
	if probe.writeTier || probe.network {
		tier := "write_workspace"
		if probe.network {
			tier = "network"
		}
		spec["policy"] = map[string]any{"tiers": []string{tier}, "approval": "ask"}
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
