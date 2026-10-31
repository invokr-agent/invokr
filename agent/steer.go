package agent

import (
	"context"
	"slices"
	"sync"

	inference "github.com/rivt-ai/go-inference-router"

	"github.com/invokr-agent/invokr/platform/events"
)

// steering holds messages the user sent while a run was in flight. The run
// takes them after its next tool round, before it calls the model again.
type steering struct {
	mu      sync.Mutex
	pending []string
	taken   []string // by the run in flight, for its after-response record; run goroutine only
}

// Steer hands text to the run in flight; it joins after the next tool round.
// A run that ends first leaves it for DropSteered, and the caller sends it as
// the next prompt instead.
func (s *Session) Steer(text string) {
	st := &s.runner.steering
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pending = append(st.pending, text)
}

// Unsteer takes back the newest message the run has not taken yet.
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

// DropSteered forgets what the finished run did not take.
func (s *Session) DropSteered() { s.runner.takeSteered() }

func (r *Runner) takeSteered() []string {
	r.steering.mu.Lock()
	defer r.steering.mu.Unlock()
	taken := r.steering.pending
	r.steering.pending = nil
	return taken
}

// injectSteered adds every pending message as a user message, in order. A
// pending internal nudge stays last: afterModelTurn drops the last message
// as that nudge, and the user's words must outlive it.
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
