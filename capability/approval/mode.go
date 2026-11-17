package approval

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
)

type Mode string

const (
	Manual      Mode = "manual"
	Auto        Mode = "auto"
	Plan        Mode = "plan"
	YOLO        Mode = "yolo"
	YOLOWarning      = "YOLO: approvals and Invokr sandbox/workspace restrictions disabled. Explicit denials still apply. Restart to leave YOLO."
)

// Policy is session-local. The execution owners provide facts about prepared
// operations; neither model arguments nor tool-advertised capabilities prove confinement.
type Policy struct {
	mu              sync.Mutex
	mode            Mode
	publish         bool
	network         bool
	outside         bool
	chatFollowsMode bool
	store           *Store
	loadErr         error
	active          map[Mode]int
}

func NewPolicy(mode Mode, publish bool) *Policy {
	mode = cmp.Or(mode, Manual)
	return &Policy{mode: mode, publish: publish, active: make(map[Mode]int)}
}

func (p *Policy) Mode() Mode {
	if p == nil {
		return Manual
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mode
}

func (p *Policy) SetMode(mode Mode) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mode == YOLO {
		return fmt.Errorf("YOLO is fixed for this session; restart to change mode")
	}
	if !slices.Contains([]Mode{Manual, Auto, Plan}, mode) {
		return fmt.Errorf("mode must be manual, auto, or plan; YOLO requires --yolo at startup")
	}
	if p.loadErr != nil && mode != Manual {
		return fmt.Errorf("standing rules unavailable: %w", p.loadErr)
	}
	p.mode = mode
	return nil
}

func (p *Policy) Clone() *Policy {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return &Policy{mode: p.mode, publish: p.publish, network: p.network, outside: p.outside, chatFollowsMode: p.chatFollowsMode, store: p.store, loadErr: p.loadErr, active: make(map[Mode]int)}
}

func (p *Policy) SetPublishing(allow bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.publish = allow
}
func (p *Policy) Publishing() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.publish }

// SetAutoNetwork lets Auto run network access without asking, for this
// session. Off, Auto asks before any network use, and confined work that did
// not ask for network runs without it.
func (p *Policy) SetAutoNetwork(allow bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.network = allow
}
func (p *Policy) AutoNetwork() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.network
}

func AutoNetworkAllowed(ctx context.Context) bool {
	s, ok := ctx.Value(callKey{}).(*callState)
	return ok && s.policy.AutoNetwork()
}

// SetOutsideReads lets read and file_search ask to reach paths beyond the
// workspace roots (workspace.reads "ask"), for this session.
func (p *Policy) SetOutsideReads(on bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outside = on
}
func (p *Policy) OutsideReads() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.outside
}

func OutsideReadsAllowed(ctx context.Context) bool {
	s, ok := ctx.Value(callKey{}).(*callState)
	return ok && s.policy.OutsideReads()
}

// SetChatFollowsMode lets a chat-started turn on a shared session inherit this
// session's mode and standing grants, as a TUI turn does (config
// relay.chat_follows_mode). Off, the pin keeps its trust boundary; on, it only
// marks whose approvals may travel to the chat.
func (p *Policy) SetChatFollowsMode(on bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.chatFollowsMode = on
	p.mu.Unlock()
}

func (p *Policy) ChatFollowsMode() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.chatFollowsMode
}

func (p *Policy) SetStore(store *Store, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.store, p.loadErr = store, err
}

func (p *Policy) PriorWork() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for mode, count := range p.active {
		if mode != p.mode {
			n += count
		}
	}
	return n
}

type callKey struct{}
type callState struct {
	approver Approver
	policy   *Policy // what this call's tools may assume
	host     *Policy // whose activity counter this call occupies
	mode     Mode
	request  Request
	pinned   bool
}

// Pin marks a turn nobody local started: a chat contact on a shared TUI sent
// the message, so nothing this session granted or allowed may answer for it.
type pinnedKey struct{}

func Pin(ctx context.Context) context.Context { return context.WithValue(ctx, pinnedKey{}, true) }

func Pinned(ctx context.Context) bool { return ctx.Value(pinnedKey{}) != nil }

func (p *Policy) Begin(ctx context.Context, req Request, approvers ...Approver) (context.Context, func()) {
	p.mu.Lock()
	// The pin downgrades this call's policy unless the operator opted into
	// relay.chat_follows_mode.
	pinned := Pinned(ctx) && !p.chatFollowsMode
	s := &callState{policy: p, host: p, mode: p.mode, request: req, pinned: pinned}
	if pinned {
		// Manual with no Auto toggles; the store stays so standing denials still
		// refuse. Grants are refused by the pinned branch of decide, and the
		// request's Pinned mark keeps the approval queue from deciding one where
		// the chat should answer.
		s.policy = &Policy{mode: Manual, store: p.store, loadErr: p.loadErr, active: make(map[Mode]int)}
		s.mode = Manual
		if p.mode == Plan {
			s.mode = Plan
		}
	}
	if len(approvers) > 0 {
		s.approver = approvers[0]
	}
	p.active[s.mode]++
	p.mu.Unlock()
	return context.WithValue(ctx, callKey{}, s), s.release
}

func (s *callState) release() {
	s.host.mu.Lock()
	defer s.host.mu.Unlock()
	s.host.active[s.mode]--
}

// Retain keeps background work visible after its initiating tool has returned.
func Retain(ctx context.Context) func() {
	events.RecordExecution(ctx, turn.ExecutionEvidence{Persistent: true})
	s, _ := ctx.Value(callKey{}).(*callState)
	if s == nil {
		return func() {}
	}
	s.host.mu.Lock()
	s.host.active[s.mode]++
	s.host.mu.Unlock()
	return sync.OnceFunc(s.release)
}

func ModeFromContext(ctx context.Context) Mode {
	if s, ok := ctx.Value(callKey{}).(*callState); ok {
		return s.mode
	}
	return Manual
}

func CallRequest(ctx context.Context) Request {
	if s, ok := ctx.Value(callKey{}).(*callState); ok {
		return s.request
	}
	return Request{}
}

// RulesPath lets execution owners seal the actual store, including custom locations.
func RulesPath(ctx context.Context) string {
	s, ok := ctx.Value(callKey{}).(*callState)
	if !ok {
		return ""
	}
	s.policy.mu.Lock()
	defer s.policy.mu.Unlock()
	if s.policy.store == nil {
		return ""
	}
	return s.policy.store.path
}

// Authorize returns handled=false for legacy callers without session policy.
// Approval is bound to this invocation, never installed as a standing grant.
func Authorize(ctx context.Context, approver Approver, req Request, ask bool) (bool, error) {
	s, ok := ctx.Value(callKey{}).(*callState)
	if !ok {
		return false, nil
	}
	approver = cmp.Or(approver, s.approver)
	req.Subject = cmp.Or(req.Subject, s.request.Subject)
	decision, resolved := s.decide(req, ask)
	if resolved {
		return true, decisionError(req, decision)
	}
	if approver == nil {
		return true, &DeniedError{Tool: req.Tool, Reason: "approval required but no approver is configured"}
	}
	req.PolicyChecked = true
	decision, err := approver.RequestApproval(ctx, req)
	if err != nil {
		return true, err
	}
	recheck := *s
	recheck.policy = s.host
	recheck.mode = s.host.Mode()
	if current, resolved := recheck.decide(req, ask); resolved && !current.Approved {
		return true, decisionError(req, current)
	}
	return true, decisionError(req, decision)
}

func decisionError(req Request, d Decision) error {
	if !d.Approved {
		return &DeniedError{Tool: req.Tool, Reason: d.Reason}
	}
	return nil
}

func (s *callState) denial(req Request) (Decision, bool) {
	s.policy.mu.Lock()
	store, err := s.policy.store, s.policy.loadErr
	s.policy.mu.Unlock()
	if err != nil && s.mode != Manual {
		return Decision{Reason: "standing rules unavailable: " + err.Error()}, true
	}
	return store.DenyOnly(req)
}

func (s *callState) decide(req Request, ask bool) (Decision, bool) {
	if d, ok := s.denial(req); ok {
		return d, true
	}
	if s.mode == Plan && blockedInPlan(req) {
		return Decision{Reason: "Plan permits only reads and confined verification with read-only sources"}, true
	}
	if s.pinned {
		return s.pinnedDecision(req, ask)
	}
	s.policy.mu.Lock()
	store, publish, network := s.policy.store, s.policy.publish, s.policy.network
	s.policy.mu.Unlock()
	if s.mode == YOLO {
		return Decision{Approved: true, Reason: "YOLO session policy"}, true
	}
	if s.mode == Manual && !ask {
		return Decision{Approved: true}, true
	}
	if needsScopedApproval(s.mode, req, publish, network) {
		return Decision{}, false
	}
	if d, ok := store.Decide(req); ok {
		return d, true
	}
	if autoApproved(s.mode, req) {
		return Decision{Approved: true, Reason: string(s.mode) + " session policy"}, true
	}
	return Decision{}, false
}

// pinnedDecision answers a call from a turn nobody local started: what its tool
// declared automatic and what the execution owner classified as a read run;
// everything else is answered only by the party that pinned the turn. Plan's
// block and standing denials are refused by decide before this runs.
func (s *callState) pinnedDecision(req Request, ask bool) (Decision, bool) {
	if !ask || readOnlyCall(req) {
		return Decision{Approved: true}, true
	}
	return Decision{}, false
}

// readTiers are the classifier's read-only tiers: the shell's read-only command
// tier and the code interpreter's read-workspace tier.
var readTiers = []string{constants.ShellTierReadonly, constants.CodeInterpreterTierReadWorkspace}

// readOnlyCall is the execution owner's read-only marking: native tools state it
// with ReadOnly, shell and the code interpreter name it in Tier (whose Writes
// flag only reports that the sandbox mounted the workspace writable). Opaque
// execution, network, publishing, protected writes and outside reads never get
// it.
func readOnlyCall(req Request) bool {
	if req.RequiresScopedApproval || req.Network || req.Publishes || req.Protected || req.Outside {
		return false
	}
	if req.ReadOnly {
		return true
	}
	if strings.TrimSpace(req.Tier) == "" {
		return false
	}
	return !slices.ContainsFunc(strings.Split(req.Tier, ","), func(tier string) bool {
		return !slices.Contains(readTiers, strings.TrimSpace(tier))
	})
}

// autoApproved is the session policy's own yes for reads and confined work;
// a read outside the workspace never gets it.
func autoApproved(mode Mode, req Request) bool {
	return mode != Manual && !req.Outside && (req.ReadOnly || req.Confined)
}

func blockedInPlan(req Request) bool {
	return !req.ReadOnly && !req.Confined || req.Writes || req.Publishes
}

func needsScopedApproval(mode Mode, req Request, publish, network bool) bool {
	// An allowed publish (a push) needs the network it publishes over.
	networkAllowed := network || req.NetworkPreapproved || req.Publishes && publish
	return mode == Auto && (req.Protected || req.RequiresScopedApproval || req.Publishes && !publish || req.Network && !networkAllowed) || mode == Plan && req.Network
}
