package shell

import (
	"encoding/json"
	"testing"

	"github.com/invokr-agent/invokr/tools/core"
)

func TestInlinePython(t *testing.T) {
	for cmd, want := range map[string]bool{
		`python3 -c 'print(1)'`:            true,
		"python3 - <<'EOF'\nprint(1)\nEOF": true,
		`echo 'print(1)' | python3`:        true,
		`cd x && python -c "import sys"`:   true,
		`python3 script.py`:                false,
		`python3 -u tools/gen.py --out x`:  false,
		`python3 -m pytest -q`:             false,
		`python3 --version`:                false,
		`pytest -q`:                        false,
		`go test ./...`:                    false,
	} {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		if got := InlinePython(&core.ToolCall{Name: "shell", Arguments: args}); got != want {
			t.Errorf("InlinePython(%q) = %v, want %v", cmd, got, want)
		}
	}
	// An argv call carries its arguments apart from the command.
	for argv, want := range map[string]bool{`["--version"]`: false, `["-c","print(1)"]`: true, `["x.py"]`: false} {
		args := json.RawMessage(`{"command":"python3","args":` + argv + `}`)
		if got := InlinePython(&core.ToolCall{Name: "shell", Arguments: args}); got != want {
			t.Errorf("InlinePython(python3 %s) = %v, want %v", argv, got, want)
		}
	}
}
