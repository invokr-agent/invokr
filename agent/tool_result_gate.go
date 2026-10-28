package agent

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/support/strutil"

	"go.uber.org/zap"
)

// The result gate bounds what one tool result may cost the context window.
// Where capToolResult destroys the overflow, the gate hands it to an oversize
// strategy that may preserve it out-of-band — v1 ships one strategy: spill the
// full output to a file under ~/.invokr/spill (a workspace extra root, so
// read can page it) and give the model a tail-biased head+tail preview
// plus an explicit pointer. Strategy choice is per tool with a default
// (config: agent.tool_result_strategy / agent.tool_result_strategies).

type spillStore interface {
	put(name string, data []byte) (ref string, err error)
}

type resultGate struct {
	budget          int // tokens
	defaultStrategy string
	perTool         map[string]string
	spill           spillStore
	logger          *zap.Logger
}

// newResultGate builds the gate from config. Unknown strategy names were
// rejected by config.Validate; a stray one here falls back to spill.
func newResultGate(settings config.AgentConfig, sessionID string, logger *zap.Logger) *resultGate {
	return &resultGate{
		budget: settings.ToolResultMaxTokens, defaultStrategy: settings.ToolResultStrategy,
		perTool: maps.Clone(settings.ToolResultStrategies), spill: newFileSpillStore(sessionID), logger: logger,
	}
}

// cap returns the model-facing form of a tool result and the spill ref, if
// any. A nil gate (tests constructing TurnProcessor literals) degrades to the
// legacy hard cap. Strategy failure falls back the same way: no regression,
// just no recovery.
func (g *resultGate) cap(tool string, _ json.RawMessage, result string) (string, string) {
	if g == nil {
		return capToolResult(result, constants.ToolResultMaxContextTokens), ""
	}
	if len(result)/4 <= g.budget { // same len/4 estimator as the context budget
		return result, ""
	}
	strategy := g.defaultStrategy
	if s, ok := g.perTool[tool]; ok {
		strategy = s
	}
	// Readers page themselves: cap at twice the gate budget, never above the
	// legacy maximum (#536). Spill failures below use the ordinary budget.
	if strategy == constants.ToolResultStrategyCap {
		return capToolResult(result, min(g.budget*2, constants.ToolResultMaxContextTokens)), ""
	}
	text, ref, err := g.spillResult(tool, result)
	if err != nil {
		g.logger.Warn("oversize strategy failed; hard-truncating", zap.String("tool", tool), zap.Error(err))
		return capToolResult(result, g.budget), ""
	}
	return text, ref
}

// spillResult preserves the complete longest output field, with a tail-biased
// preview. Other envelope fields survive, including exit_code/timed_out;
// completed-but-flagged ToolError.Payload results must stay non-empty.
// Historical archiving separately stores the complete envelope.
func (g *resultGate) spillResult(tool, result string) (string, string, error) {
	fields, key, payload, enveloped := envelopeLongestField(result)
	if !enveloped {
		payload = result
	}
	ref, err := g.spill.put(tool, []byte(payload))
	if err != nil {
		return "", "", fmt.Errorf("spill %s result: %w", tool, err)
	}
	stub := spillStub(payload, ref, g.budget)
	if !enveloped {
		return stub, ref, nil
	}
	encoded, _ := json.Marshal(stub + shellRecaptureHint(fields))
	fields[key] = encoded
	// ponytail: fields contain only validated JSON and the encoded preview string.
	envelope, _ := json.Marshal(fields)
	return string(envelope), ref, nil
}

// envelopeLongestField reports the longest string field of a JSON object
// result — the payload worth spilling — when there is one.
// Result filters (tokf) may wrap the envelope in decoration lines, so a
// failed strict parse retries as "first JSON object found in the text".
func envelopeLongestField(result string) (map[string]json.RawMessage, string, string, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result), &fields); err != nil {
		if err := json.Unmarshal([]byte(strutil.ExtractJSONObject(result)), &fields); err != nil {
			return nil, "", "", false
		}
	}
	if len(fields) == 0 {
		return nil, "", "", false
	}
	key, val, ok := longestStringField(fields)
	return fields, key, val, ok
}

// shellRecaptureHint tells the model when the spilled text is itself
// incomplete: shell capped it at capture time, so paging the spill file cannot
// recover the tail — only re-running with a larger max_output_bytes can.
func shellRecaptureHint(fields map[string]json.RawMessage) string {
	if string(fields["truncated"]) != "true" {
		return ""
	}
	return fmt.Sprintf("\n[note: the tool itself truncated this output at capture; re-run with a larger max_output_bytes (up to %d) if the missing part matters]", constants.ShellMaxOutputBytes)
}

// spillStub builds the model-facing preview: an anti-confabulation banner
// naming the spill file, then head and tail slices. Tail-biased (~20/80)
// because errors and summaries live at the end of most tool output.
func spillStub(full, ref string, budgetTokens int) string {
	previewBytes := budgetTokens * 4 / 2 // spend half the budget on the preview
	head, tail, omitted := headTailPreview(full, previewBytes)
	pageLines := budgetPageLines(full, budgetTokens)
	return fmt.Sprintf(
		"[TOOL RESULT TRUNCATED — the text below is only a preview, NOT the complete output.\n"+
			"Full output: %d bytes, %d lines, saved to %s\n"+
			"Page through it with read, e.g. {\"paths\":[%q],\"offset\":1,\"limit\":%d}; "+
			"the result budget is ~%d tokens (~%d bytes), and at this file's ~%d bytes/line that is ~%d lines per page.]\n"+
			"%s\n--- %d lines omitted (read the spill file for the rest) ---\n%s",
		len(full), 1+strings.Count(full, "\n"), ref, ref, pageLines,
		budgetTokens, budgetTokens*4, avgLineBytes(full), pageLines, head, omitted, tail)
}

func avgLineBytes(s string) int {
	return max(len(s)/max(lineCount(s), 1), 1)
}

// budgetPageLines converts the result-token budget into a line count for THIS
// file, using the same len/4 estimator the budget itself uses: a fixed
// "~500 lines" hint was 7-10k tokens on wide CSV rows against a 4k budget
// (#536).
func budgetPageLines(s string, budgetTokens int) int {
	return max(budgetTokens*4/avgLineBytes(s), 1)
}

// headTailPreview cuts a head (~20%) and tail (~80%) slice of s within
// previewBytes, on UTF-8 boundaries trimmed to whole lines.
func headTailPreview(s string, previewBytes int) (head, tail string, omittedLines int) {
	head = s[:runeFloor(s, min(previewBytes/5, len(s)))]
	if i := strings.LastIndexByte(head, '\n'); i >= 0 {
		head = head[:i]
	}
	tail = s[runeCeil(s, max(len(s)-(previewBytes-previewBytes/5), len(head))):]
	if i := strings.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	omittedLines = max(lineCount(s)-lineCount(head)-lineCount(tail), 0)
	return head, tail, omittedLines
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// runeFloor/runeCeil move an index to the nearest rune boundary at or
// below/above it.
func runeFloor(s string, i int) int {
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

func runeCeil(s string, i int) int {
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}

// fileSpillStore writes spill files under
// InvokrHomeDir()/spill/<sessionhash>/<call>.txt, pruning the whole spill
// tree once per process (mirrors the background-shell log retention).
type fileSpillStore struct {
	dir       string
	pruneOnce *sync.Once
	seq       int
	mu        sync.Mutex
}

func newFileSpillStore(sessionID string) *fileSpillStore {
	scope := "unscoped"
	if strings.TrimSpace(sessionID) != "" {
		scope = strutil.HashHex(sessionID)[:12]
	}
	return &fileSpillStore{dir: filepath.Join(config.InvokrHomeDir(), constants.SpillDirName, scope), pruneOnce: &spillPruneOnce}
}

var spillPruneOnce sync.Once

const (
	spillRetention = 24 * time.Hour
	spillMaxFiles  = 200
)

func (s *fileSpillStore) put(name string, data []byte) (string, error) {
	s.pruneOnce.Do(func() { pruneSpillFiles(filepath.Dir(s.dir), time.Now()) })
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return "", fmt.Errorf("create spill dir: %w", err)
	}
	s.mu.Lock()
	s.seq++
	path := filepath.Join(s.dir, fmt.Sprintf("%s-%d.txt", sanitizeSpillName(name), s.seq))
	s.mu.Unlock()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write spill file: %w", err)
	}
	return path, nil
}

func sanitizeSpillName(name string) string {
	mapped := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, name)
	mapped = cmp.Or(mapped, "result")
	if len(mapped) > 64 {
		mapped = mapped[:64]
	}
	return mapped
}

// pruneSpillFiles removes spill files past retention and, oldest first, any
// beyond the count cap, across every session subdirectory.
func pruneSpillFiles(root string, now time.Time) {
	type spillFile struct {
		path    string
		modTime time.Time
	}
	var kept []spillFile
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".txt" {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		if now.Sub(info.ModTime()) > spillRetention {
			_ = os.Remove(path)
			return nil
		}
		kept = append(kept, spillFile{path: path, modTime: info.ModTime()})
		return nil
	})
	slices.SortFunc(kept, func(a, b spillFile) int { return b.modTime.Compare(a.modTime) })
	for _, stale := range kept[min(len(kept), spillMaxFiles):] {
		_ = os.Remove(stale.path)
	}
}
