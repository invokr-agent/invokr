package shell

import (
	"testing"

	"github.com/invokr-agent/invokr/tools/system/workspace"
)

// Probe: the real heredoc shapes that were refused in the round-6 bench rows.
func TestHeredocProbeFromBenchRows(t *testing.T) {
	wsp := workspace.Policy{Root: t.TempDir()}
	allowed := []string{"cat", "python3", "echo", "chmod"}
	cases := map[string]string{
		"pythonpath_body": "cat > /tmp/t.py << 'PYEOF'\nfrom main import Fib\n\nf = Fib(); next(f)\nimport os\nos.environ['PYTHONPATH'] = '.'\nPYEOF",
		"seq_in_body":     "cat > verify_fib.py << 'PYEOF'\nfrom main import Fib\n\nf = Fib()\nfor i in seq(10):\n    print(i)\nPYEOF",
		"body_as_cmd":     "cat > verify_fib.py << 'PYEOF'\nfrom main import Fib\n\ndef run():\n    f = Fib(); next(f)             # -> 1 ; state prev_prev=1, prev=1\n    return f\nPYEOF",
		"pandas_stdin":    "python3 << 'EOF'\nimport pandas as pd\n\ndf = pd.read_csv(\"data.csv\", dtype=str)\n\ndef get_postal(addr):\n    return addr.strip()\nEOF",
		"dict_literal":    "cat > f.py <<'EOF'\nns={}\nout = {k: v for k, v in d.items()}\nEOF",
		"solve_permut":    "cat > /tmp/solve.py << 'EOF'\nfrom itertools import permutations\ndef go():\n    return [p for p in permutations(range(4))]\nEOF",
	}
	for name, line := range cases {
		pol, err := classifyLine(line, allowSet(allowed...), allowed, wsp, wsp.Root, false)
		if err != nil {
			t.Errorf("%s: REFUSED: %v", name, err)
			continue
		}
		t.Logf("%s: allowed, tier=%q", name, pol.tier)
	}
}
