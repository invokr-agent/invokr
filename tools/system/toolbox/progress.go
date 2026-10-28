package toolbox

import (
	"bytes"
	"context"
	"strings"
	"time"

	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/tools/core"
)

// progressLineCap bounds the partial line held between writes, so a plugin
// that streams megabytes without a newline cannot grow this buffer unbounded.
const progressLineCap = 64 * 1024

// progressWriter turns a plugin's stderr stream into tool progress events. It
// reads only lines carrying constants.ToolboxProgressPrefix; everything else
// passes through untouched, and stdout — where the final
// {"result":...}/{"error":...} payload lives — is never involved, so the
// existing final-payload parse and tokf filtering are unaffected.
//
// Only the one-shot invocation path tees stderr through this writer. A daemon
// plugin multiplexes every call of a session over one long-lived process, so
// its stderr is not attributable to a single call and carries no progress; the
// limitation is documented in toolbox/README.md.
type progressWriter struct {
	ctx  context.Context
	tool *executableTool
	buf  []byte
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		line, rest, ok := bytes.Cut(w.buf, []byte("\n"))
		if !ok {
			break
		}
		w.buf = rest
		text, tagged := strings.CutPrefix(strings.TrimSpace(string(line)), constants.ToolboxProgressPrefix)
		if text = strings.TrimSpace(text); !tagged || text == "" || w.tool.dispatcher == nil {
			continue
		}
		w.tool.dispatcher.Dispatch(w.ctx, events.Event{
			Type: events.EventToolProgress, Timestamp: time.Now(), SessionID: core.SessionIDFromContext(w.ctx),
			Tool: &events.ToolPayload{ID: core.ToolCallIDFromContext(w.ctx), Name: w.tool.Name, Progress: text},
		})
	}
	if len(w.buf) > progressLineCap {
		w.buf = nil
	}
	return len(p), nil
}
