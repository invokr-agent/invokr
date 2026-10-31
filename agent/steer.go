package agent

import (
	"context"
	"slices"
	"sync"

	inference "github.com/rivt-ai/go-inference-router"

	"github.com/invokr-agent/invokr/platform/events"
)

type steering struct {
	mu      sync.Mutex
	pending []string
	taken   []string // run goroutine only
}

func (s *Session) Steer(text string) {
	st := &s.runner.steering
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pending = append(st.pending, text)
}

func (s *Session) Unsteer() (string, bool) {
	st := &s.runner.steering
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.pending) == 0 {
		return "", false
	}
	text := st.pending[len(st.pending)-1]
	st.pending = st.pending[:len(st.pending)-1]
	return text, true
}

func (s *Session) DropSteered() { s.runner.takeSteered() }

func (r *Runner) takeSteered() []string {
	r.steering.mu.Lock()
	defer r.steering.mu.Unlock()
	taken := r.steering.pending
	r.steering.pending = nil
	return taken
}

// A pending nudge stays last: afterModelTurn drops the last message as the nudge.
func (r *Runner) injectSteered(ctx context.Context, req RunRequest, state *runState) {
	taken := r.takeSteered()
	if len(taken) == 0 {
		return
	}
	at := len(state.messages)
	if state.nudgeAppended {
		at--
	}
	added := make([]inference.Message, len(taken))
	for i, text := range taken {
		added[i] = inference.UserMessage(text)
		events.Steered(ctx, r.events, req.SessionID, r.parentSessionID, text)
	}
	state.messages = slices.Insert(state.messages, at, added...)
	r.steering.taken = append(r.steering.taken, taken...)
}
