package toolbox

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Use the browser's real response writer without loading Playwright or making
// network calls. Both process modes must honor the Go toolbox text contract.
const browserWriterFixture = `
import importlib.util, json, sys
spec = importlib.util.spec_from_file_location("browser", sys.argv[1])
browser = importlib.util.module_from_spec(spec)
spec.loader.exec_module(browser)
if sys.argv[2] == "daemon":
    for line in sys.stdin:
        browser._write_line_result(**json.loads(line))
else:
    browser.write_result(**json.load(sys.stdin))
`

func TestBrowserProducerMatchesToolboxResultContract(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	source, err := filepath.Abs("../../../toolbox/browser_agent/main.py")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := func(mode string) *exec.Cmd {
		return exec.CommandContext(ctx, python, "-u", "-c", browserWriterFixture, source, mode)
	}
	dp, err := startDaemon(4096, func() (*exec.Cmd, func(), error) {
		return command("daemon"), nil, nil
	}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.stdin.Close(); <-dp.done })
	for _, tc := range []struct {
		name, request, want string
		failed              bool
	}{
		{"object", `{"result":{"action":"read","text":"héllo 世界"}}`, `{"action":"read","text":"héllo 世界"}`, false},
		{"string", `{"result":"closed browser context"}`, "closed browser context", false},
		{"unicode", `{"result":"héllo 世界"}`, "héllo 世界", false},
		{"error", `{"error":"browser failed"}`, "browser failed", true},
		{"after error", `{"result":"still alive"}`, "still alive", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []string{"one-shot", "daemon"} {
				var raw string
				if mode == "daemon" {
					raw, err = invokeDaemon(ctx, dp, []byte(tc.request))
				} else {
					cmd := command(mode)
					cmd.Stdin = strings.NewReader(tc.request)
					out, runErr := cmd.Output()
					raw, err = string(out), runErr
				}
				if err != nil {
					t.Fatal(err)
				}
				decode := pluginResult
				if mode == "daemon" {
					decode = parseDaemonResponse
				}
				got, decodeErr := decode("browser_agent", raw)
				if tc.failed {
					if decodeErr == nil || !strings.Contains(decodeErr.Error(), tc.want) {
						t.Fatalf("%s error = %v", mode, decodeErr)
					}
					continue
				}
				if tc.name == "object" && decodeErr == nil {
					var value any
					if err := json.Unmarshal([]byte(got), &value); err != nil {
						t.Fatal(err)
					}
					canonical, _ := json.Marshal(value)
					got = string(canonical)
				}
				if decodeErr != nil || got != tc.want {
					t.Errorf("%s result = %q, %v; want %q", mode, got, decodeErr, tc.want)
				}
			}
		})
	}
}
