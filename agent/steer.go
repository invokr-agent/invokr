package agent

import (
	"context"
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

// injectSteered adds every pending message as a user message, in order.
func (r *Runner) injectSteered(ctx context.Context, req RunRequest, state *runState) {
	for _, text := range r.takeSteered() {
		r.steering.taken = append(r.steering.taken, text)
		state.messages = append(state.messages, inference.UserMessage(text))
		events.Steered(ctx, r.events, req.SessionID, r.parentSessionID, text)
	}
}
