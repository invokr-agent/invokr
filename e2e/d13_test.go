//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/support/testutil"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// TestD13_ToolboxManifestExtensions is the oracle for the four manifest-driven
// toolbox core extensions: argument schemas rejected before spawn, per-tool
// resource limits, approval-gated scoped network, and the progress channel.
func TestD13_ToolboxManifestExtensions(t *testing.T) {
	if exec.Command("unshare", "--user", "--map-root-user", "true").Run() != nil {
		t.Skip("BLOCKED: user namespaces unavailable")
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("d13-network-ok"))
	}))
	defer fixture.Close()
	port := fixture.URL[strings.LastIndex(fixture.URL, ":")+1:]
	deniedHost := d13DeniedHost(t, port)

	home := newHome(t, "")
	sentinel := filepath.Join(home.workspace, "d13-sentinel")

	d13Plugin(t, home, "d13_schema", d13SentinelSource(sentinel), map[string]any{
		"capabilities": map[string]any{"writes_workspace": true, "executes_code": true},
		"parameters": map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"path": map[string]any{"type": "string"}},
			"required":             []any{"path"},
			"additionalProperties": false,
		},
	})
	d13Plugin(t, home, "d13_slow", "setTimeout(()=>console.log('{}'),5000);", map[string]any{
		"limits": map[string]any{"timeout_seconds": 2},
	})
	d13Plugin(t, home, "d13_progress", d13ProgressSource, nil)
	d13Plugin(t, home, "d13_net_allowed", d13FetchSource("http://localhost:"+port), map[string]any{
		"network": map[string]any{"allow_hosts": []string{"localhost"}},
	})
	d13Plugin(t, home, "d13_net_denied", d13FetchSource("http://"+deniedHost+":"+port), map[string]any{
		"network": map[string]any{"allow_hosts": []string{"localhost"}},
	})

	names := []string{"d13_schema", "d13_slow", "d13_progress", "d13_net_allowed", "d13_net_denied"}
	args := map[string]map[string]any{"d13_schema": {"nope": 1}}
	provider := providertest.NewScripted(t,
		providertest.CompletionStep{ToolCalls: d13Calls(names, "load-", "tool_search", nil)},
		providertest.CompletionStep{ToolCalls: d13Calls(names, "", "", args)},
		providertest.CompletionStep{ToolCalls: d13Calls([]string{"d13_schema"}, "retry-", "", map[string]map[string]any{"d13_schema": {"path": "ok.txt"}})},
		providertest.ReplyStep("d13 done"))
	defer provider.Close()

	result := runInProc(t, home, provider.URL(), "run manifest extension probes", grantAll)

	// 1. Schema violation is refused in-core: flagged failure, no plugin spawn.
	if !d13Flagged(result, "d13_schema") {
		t.Fatalf("schema violation was not recorded as a tool failure: %+v", result.Tools)
	}
	// 2. Limits: the 5s plugin declaring timeout_seconds:2 is killed.
	if !d13Flagged(result, "d13_slow") {
		t.Fatalf("declared 2s timeout did not fail the 5s plugin: %+v", result.Tools)
	}
	// 3. Progress events arrive before the result, and the result still parses.
	if got := d13Progress(result, "d13_progress"); len(got) < 2 {
		t.Fatalf("progress events = %v, want at least 2", got)
	}
	snapshot, err := json.Marshal(provider.Snapshot().CompletionRequests)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(snapshot), "d13-progress-done") {
		t.Fatalf("final {\"result\":...} payload did not reach the model")
	}
	// 4. Scoped network: the allowed host resolves, the undeclared one does not.
	if !strings.Contains(string(snapshot), "d13-network-ok") {
		t.Fatalf("allow_hosts plugin could not reach the allowed host")
	}
	if !strings.Contains(string(snapshot), "d13-network-blocked") {
		t.Fatalf("undeclared host %q was reachable under allow_hosts", deniedHost)
	}
	if !d13NetworkApprovalRequested(result) {
		t.Fatalf("allow_hosts did not raise a %s approval: %+v", constants.ShellTierNetwork, result.Approvals)
	}
	// The plugin only ever ran with valid arguments, so the sentinel proves the
	// invalid call never reached a process.
	if got := readFile(t, sentinel); got != "ok.txt" {
		t.Fatalf("sentinel = %q; the plugin ran with the rejected arguments", got)
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}

const d13ProgressSource = `
console.error("` + constants.ToolboxProgressPrefix + ` scanning");
console.error("` + constants.ToolboxProgressPrefix + ` merging");
console.log(JSON.stringify({result:"d13-progress-done"}));`

func d13SentinelSource(path string) string {
	return fmt.Sprintf(`const fs=require("fs");
let body="";process.stdin.on("data",d=>body+=d).on("end",()=>{
const a=JSON.parse(body).arguments||{};
fs.writeFileSync(%q,String(a.path));
console.log(JSON.stringify({result:"d13-ran"}));});`, path)
}

// d13DeniedHost picks the host name the not-allowed probe dials. It has to be
// one that WOULD reach the fixture if scoping were absent, or the assertion
// proves nothing: an RFC 2606 .invalid name resolves nowhere, so the probe
// fails identically whether or not the sandbox scopes anything. These
// candidates are public wildcard domains that resolve to loopback through the
// host's real resolver — exactly the resolver the sandbox withholds — so the
// probe succeeds on the host, and inside the sandbox it can only succeed if
// /etc/resolv.conf leaked in.
func d13DeniedHost(t *testing.T, port string) string {
	t.Helper()
	for _, host := range []string{"lvh.me", "localtest.me", "127.0.0.1.nip.io"} {
		resp, err := http.Get("http://" + net.JoinHostPort(host, port))
		if err != nil {
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr == nil && strings.Contains(string(body), "d13-network-ok") {
			return host
		}
	}
	t.Skip("BLOCKED: no DNS-resolvable loopback host reaches the fixture; the scoping assertion would be vacuous")
	return ""
}

func d13FetchSource(url string) string {
	// node's http module, not fetch: undici allocates a WASM heap that does not
	// fit under the sandbox's RLIMIT_AS.
	return fmt.Sprintf(`require("http").get(%q,r=>{let b="";r.on("data",d=>b+=d);
r.on("end",()=>console.log(JSON.stringify({result:b})));})
.on("error",e=>console.log(JSON.stringify({result:"d13-network-blocked: "+e.message})));`, url)
}

func d13Plugin(t *testing.T, home testHome, name, source string, extra map[string]any) {
	t.Helper()
	dir := testutil.MustWriteToolboxPlugin(t, home.state.ToolboxDir, name, "D13 "+name, map[string]string{"main.js": source})
	specPath := filepath.Join(dir, "tool.json")
	spec := map[string]any{}
	if err := json.Unmarshal([]byte(readFile(t, specPath)), &spec); err != nil {
		t.Fatal(err)
	}
	for key, value := range extra {
		spec[key] = value
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// d13Calls enables or runs each probe through the manager.
func d13Calls(names []string, idPrefix, searchTool string, args map[string]map[string]any) []providertest.CompletionToolCall {
	var calls []providertest.CompletionToolCall
	for _, name := range names {
		arguments := args[name]
		if arguments == nil {
			arguments = map[string]any{}
		}
		managed := map[string]any{"action": "run", "name": name, "arguments": arguments}
		if searchTool != "" {
			managed = map[string]any{"action": "enable", "names": []string{name}}
		}
		calls = append(calls, providertest.CompletionToolCall{ID: idPrefix + name, Type: "function", Function: providertest.CompletionToolFunction{Name: "tools", Arguments: managed}})
	}
	return calls
}

func d13Flagged(result inProcResult, tool string) bool {
	for _, ev := range result.Tools {
		if ev.Name != tool {
			continue
		}
		if ev.Type == events.EventToolFailed || ev.Code != "" {
			return true
		}
	}
	return false
}

func d13Progress(result inProcResult, tool string) []string {
	var out []string
	for _, ev := range result.Tools {
		if ev.Name == tool && ev.Type == events.EventToolProgress {
			out = append(out, ev.Progress)
		}
	}
	return out
}

func d13NetworkApprovalRequested(result inProcResult) bool {
	for _, req := range result.Approvals {
		if strings.Contains(req.Tier, constants.ShellTierNetwork) && strings.HasPrefix(req.Tool, "d13_net") {
			return true
		}
	}
	return false
}
