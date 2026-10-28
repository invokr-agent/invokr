package approval

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/invokr-agent/invokr/support/fsutil"
)

// This file implements *standing grants*: a persistent, scoped, expiring
// auto-approval policy ("always allow `shell` running `git status*` for 24h").

// Scope values for a standing grant.
const (
	ScopeSession   = "session"   // in-memory only, never written to disk
	ScopeWorkspace = "workspace" // applies only inside Grant.Workspace
	ScopeGlobal    = "global"    // applies anywhere
)

// alwaysExcluded lists tools no standing grant may ever cover. These are the
// worst instances of the confused-deputy problem: they author or delegate new
// capability (create_tool, delegate_task) or run arbitrary model-authored code
// (code_interpreter), rather than performing one bounded, patternable action.
var alwaysExcluded = []string{"create_tool", "delegate_task", "code_interpreter"}

// ErrExcluded reports that a tool is on the standing-grant exclusion list.
type ErrExcluded struct{ Tool string }

func (e *ErrExcluded) Error() string {
	return fmt.Sprintf("tool %q cannot be covered by a standing grant", e.Tool)
}

// ErrInvalidPattern reports a rejected argument pattern.
type ErrInvalidPattern struct{ Pattern, Reason string }

func (e *ErrInvalidPattern) Error() string {
	return fmt.Sprintf("invalid grant pattern %q: %s", e.Pattern, e.Reason)
}

// Grant is one standing approval policy. With Deny set it is a standing
// *denial* — the same shape (scope, expiry, pattern, audit) applied in the
// opposite direction. Denials are resolved before grants (deny-overrides).
type Grant struct {
	Deny       bool      `json:"deny,omitempty"`
	ID         string    `json:"id"`
	Tool       string    `json:"tool"`
	Tier       string    `json:"tier,omitempty"` // empty = any tier
	ArgPattern string    `json:"arg_pattern"`
	Scope      string    `json:"scope"`
	Workspace  string    `json:"workspace,omitempty"` // required for ScopeWorkspace
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Reason     string    `json:"reason,omitempty"`
}

func (g Grant) expired(now time.Time) bool { return !now.Before(g.ExpiresAt) }

// grantsFile is the on-disk shape: a JSON *object*, not the bare JSON array an
// invokr predating standing denials wrote and read. That matters for safety,
// not tidiness. Deny is `omitempty` inside a record the old binary otherwise
// understands, so a downgraded binary reading the array form would see a stored
// denial as a plain grant and silently auto-approve exactly what the user chose
// to always deny — a safety decision inverted, not merely lost. An object
// instead fails to parse there, and Load refuses a file it cannot parse, so the
// downgrade falls back to prompting (fail closed) rather than to allowing.
//
// The same asymmetry is why this is a hard format break rather than a tolerant
// read of both shapes: accepting the legacy array would leave no way to tell a
// genuine old file from one a downgraded binary rewrote after dropping the deny
// flags. Standing rules carry a mandatory expiry (the TUI writes 24h), so the
// cost of re-adding them once is small next to a silent inversion.
type grantsFile struct {
	Version int     `json:"version"` // 2 = grants and denials
	Rules   []Grant `json:"rules"`
}

// Store holds the standing grants and persists them to disk.
// The zero value is not usable; construct with NewStore.
type Store struct {
	mu        sync.Mutex
	path      string
	grants    []Grant
	excluded  map[string]bool
	workspace string
	audit     func(Grant, Request, string)
}

// NewStore returns a store backed by path (typically ~/.invokr/grants.json).
// An empty path disables persistence; the store still works in memory.
func NewStore(path string) *Store {
	s := &Store{path: path, excluded: map[string]bool{}}
	s.Exclude(alwaysExcluded...)
	return s
}

// Exclude adds tool names to the exclusion list, on top of alwaysExcluded.
func (s *Store) Exclude(tools ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range tools {
		if name = strings.TrimSpace(name); name != "" {
			s.excluded[name] = true
		}
	}
}

func (s *Store) SetWorkspace(dir string) {
	s.mu.Lock()
	s.workspace = dir
	s.mu.Unlock()
}

// SetAuditor installs the callback invoked when a rule is created ("created")
// and on every rule *use* ("used"). A standing rule that silently decides is
// not auditable.
func (s *Store) SetAuditor(fn func(Grant, Request, string)) {
	s.mu.Lock()
	s.audit = fn
	s.mu.Unlock()
}

// Load reads the grants file, dropping expired entries. A missing file is not
// an error. A corrupt file leaves the store empty and returns the parse error,
// so a damaged file can never be read as a permissive policy.
func (s *Store) Load() error {
	if s.path == "" {
		return nil
	}
	file, existed, err := fsutil.ReadJSON[grantsFile](s.path)
	if err != nil {
		if existed {
			return fmt.Errorf("parse grants file %s: %w", s.path, err)
		}
		return fmt.Errorf("read grants file: %w", err)
	}
	now := time.Now()
	s.mu.Lock()
	s.grants = s.grants[:0]
	for _, g := range file.Rules {
		if !g.expired(now) && s.validate(&g) == nil {
			s.grants = append(s.grants, g)
		}
	}
	s.mu.Unlock()
	return nil
}

// Add validates, stores, persists, and audits a standing rule, returning the
// stored copy.
func (s *Store) Add(g Grant) (Grant, error) {
	g.Tool, g.ArgPattern = strings.TrimSpace(g.Tool), strings.TrimSpace(g.ArgPattern)
	g.Scope = cmp.Or(strings.TrimSpace(g.Scope), ScopeGlobal)
	if g.ID == "" {
		g.ID = uuid.NewString()
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now()
	}
	s.mu.Lock()
	if err := s.validate(&g); err != nil {
		s.mu.Unlock()
		return Grant{}, err
	}
	s.grants = append(s.grants, g)
	if err := s.save(); err != nil {
		// Roll back: a caller that saw an error must not be left with a live
		// in-memory rule that decides for the rest of the session.
		s.grants = s.grants[:len(s.grants)-1]
		s.mu.Unlock()
		return Grant{}, err
	}
	// The auditor runs after the unlock: it may call back into the store.
	audit := s.audit
	s.mu.Unlock()
	if audit != nil {
		audit(g, Request{Tool: g.Tool, Subject: g.ArgPattern}, "created")
	}
	return g, nil
}

// validate enforces every creation-time rule. Callers must hold s.mu (Load
// calls it while holding the lock too).
func (s *Store) validate(g *Grant) error {
	if g.Tool == "" {
		return fmt.Errorf("grant requires a tool name")
	}
	if s.excluded[g.Tool] && !g.Deny {
		return &ErrExcluded{Tool: g.Tool}
	}
	if err := validatePattern(g.ArgPattern); err != nil {
		return err
	}
	switch g.Scope {
	case ScopeSession, ScopeGlobal:
	case ScopeWorkspace:
		if strings.TrimSpace(g.Workspace) == "" {
			return fmt.Errorf("workspace-scoped grant requires a workspace")
		}
	default:
		return fmt.Errorf("unknown grant scope %q", g.Scope)
	}
	if g.ExpiresAt.IsZero() {
		return fmt.Errorf("grant requires an expiry")
	}
	return nil
}

// Revoke removes a grant by ID, reporting whether one was removed and whether
// the removal was persisted. A revoke that fails to persist still takes effect
// in memory — the grant must stop auto-approving immediately — but returns the
// error, because the grant is still in the file and would come back on the next
// load. Callers must surface that: a silently resurrected standing approval is
// the worst failure this store has.
func (s *Store) Revoke(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.grants, func(g Grant) bool { return g.ID == id })
	if i < 0 {
		return false, nil
	}
	s.grants = slices.Delete(s.grants, i, i+1)
	return true, s.save()
}

// List returns the unexpired grants, newest last.
func (s *Store) List() []Grant {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.DeleteFunc(append([]Grant{}, s.grants...), func(g Grant) bool { return g.expired(now) })
}

// Match reports whether a standing rule (grant or denial) covers req. It is
// deny-by-default: any doubt (nil store, empty subject, excluded tool, expired
// rule, wrong workspace) yields false. A match emits an audit event first.
func (s *Store) Match(req Request) (Grant, bool) {
	if s == nil || strings.TrimSpace(req.Subject) == "" {
		return Grant{}, false
	}
	now := time.Now()
	s.mu.Lock()
	grant, ok := s.find(req, now)
	audit := s.audit
	s.mu.Unlock()
	if ok && audit != nil {
		audit(grant, req, "used")
	}
	return grant, ok
}

// Decide reports the standing decision for req, if any. It is the single place
// the standing decision reasons are spelled, so every non-interactive path
// (exec, plain mode) resolves a call exactly the way the TUI queue does.
func (s *Store) Decide(req Request) (Decision, bool) {
	rule, ok := s.Match(req)
	if !ok {
		return Decision{}, false
	}
	if rule.Deny {
		return Decision{Reason: "standing denial " + rule.ID}, true
	}
	return Decision{Approved: true, Reason: "standing grant " + rule.ID}, true
}

// DenyOnly never consumes/audits an allow grant while checking a hard denial.
func (s *Store) DenyOnly(req Request) (Decision, bool) {
	if s == nil {
		return Decision{}, false
	}
	s.mu.Lock()
	i := slices.IndexFunc(s.grants, func(g Grant) bool { return g.Deny && s.applies(g, req, time.Now()) })
	var rule Grant
	if i >= 0 {
		rule = s.grants[i]
	}
	audit := s.audit
	s.mu.Unlock()
	if i < 0 {
		return Decision{}, false
	}
	if audit != nil {
		audit(rule, req, "used")
	}
	return Decision{Reason: "standing denial " + rule.ID}, true
}

// find resolves precedence: denials are scanned before grants, so an applicable
// standing denial always wins over an applicable standing grant. The exclusion
// list only blocks grants — a tool that may never be auto-approved may still be
// auto-denied.
func (s *Store) find(req Request, now time.Time) (Grant, bool) {
	first := func(deny bool) (Grant, bool) {
		i := slices.IndexFunc(s.grants, func(g Grant) bool { return g.Deny == deny && s.applies(g, req, now) })
		if i < 0 {
			return Grant{}, false
		}
		return s.grants[i], true
	}
	if g, ok := first(true); ok {
		return g, true
	}
	if s.excluded[req.Tool] {
		return Grant{}, false
	}
	return first(false)
}

func (s *Store) applies(g Grant, req Request, now time.Time) bool {
	if g.expired(now) || g.Tool == "" || g.Tool != req.Tool {
		return false
	}
	if g.Tier != "" && g.Tier != req.Tier {
		return false
	}
	if g.Scope == ScopeWorkspace && (s.workspace == "" || g.Workspace != s.workspace) {
		return false
	}
	if validatePattern(g.ArgPattern) != nil {
		return false
	}
	return matchesRequest(g, req)
}

func matchesRequest(g Grant, req Request) bool {
	if g.Deny && slices.ContainsFunc(req.Paths, func(path string) bool { return globMatch(g.ArgPattern, path) }) {
		return true
	}
	if req.SubjectIsPathList {
		if len(req.Paths) > 0 {
			match := func(path string) bool { return globMatch(g.ArgPattern, path) }
			if g.Deny {
				return slices.ContainsFunc(req.Paths, match)
			}
			return !slices.ContainsFunc(req.Paths, func(path string) bool { return !match(path) })
		}
		return matchPathList(g.ArgPattern, req.Subject, g.Deny)
	}
	return globMatch(g.ArgPattern, req.Subject)
}

// matchPathList treats subject as a space-separated path list and matches the
// pattern against each path on its own — never against the joined string, where
// a '*' expansion could swallow the separator and let `docs/*` cover a patch
// that also touches `.github/workflows/ci.yml`.
//
// The quantifier is the direction of the rule, and getting it backwards fails
// open: a grant must cover *every* path to approve the call, while a denial
// fires if it matches *any* path. Requiring every path for a denial would let
// the model escape it by adding one unrelated file to the patch.
func matchPathList(pattern, subject string, deny bool) bool {
	paths := strings.Fields(subject)
	if len(paths) == 0 {
		return false
	}
	matches := func(p string) bool { return globMatch(pattern, p) }
	if deny {
		return slices.ContainsFunc(paths, matches)
	}
	return !slices.ContainsFunc(paths, func(p string) bool { return !matches(p) })
}

// save persists non-session grants atomically at 0600. Callers must hold s.mu.
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	persist := slices.DeleteFunc(append([]Grant{}, s.grants...), func(g Grant) bool { return g.Scope == ScopeSession })
	if err := fsutil.SaveJSON(s.path, grantsFile{Version: 2, Rules: persist}); err != nil {
		return fmt.Errorf("write grants file: %w", err)
	}
	return nil
}

// patternMetachars are rejected inside a grant pattern and are never crossed by
// a '*'. They are the characters that let one approved command smuggle another.
const patternMetachars = ";&|<>$`\\\n\r"

// Compound reports a subject that chains, pipes or redirects: a standing
// grant can never match it (a wildcard never spans a metacharacter), so
// "always" would approve this call once and store a rule that never fires.
func Compound(subject string) bool { return strings.ContainsAny(subject, patternMetachars) }

// validatePattern enforces the literal, deny-by-default pattern grammar:
// printable ASCII only (no unicode lookalikes), '*' is the only metacharacter,
// no leading '*' and no "**" (both are --allow-all with extra steps), no ".."
// (path traversal), no shell metacharacters, and at least 4 literal characters.
func validatePattern(pattern string) error {
	reject := func(reason string) error { return &ErrInvalidPattern{Pattern: pattern, Reason: reason} }
	switch {
	case strings.TrimSpace(pattern) == "":
		return reject("pattern is empty")
	case !printableASCII(pattern):
		return reject("pattern must be printable ASCII")
	case strings.HasPrefix(pattern, "*"):
		return reject("pattern may not start with '*'")
	case strings.Contains(pattern, "**"):
		return reject("pattern may not contain '**'")
	case strings.Contains(pattern, ".."):
		return reject("pattern may not contain '..'")
	case strings.ContainsAny(pattern, patternMetachars):
		return reject("pattern may not contain shell metacharacters")
	case len(strings.ReplaceAll(pattern, "*", "")) < 4:
		return reject("pattern needs at least 4 literal characters")
	}
	return nil
}

func printableASCII(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r > 0x7e })
}

// globMatch reports whether subject matches pattern. '*' is the only
// metacharacter and matches any run of characters that is itself printable
// ASCII and free of shell metacharacters, so `git status*` can never cover
// `git status; rm -rf /`. Interior segments match leftmost-first without
// backtracking: an ambiguous pattern fails closed rather than open.
func globMatch(pattern, subject string) bool {
	segments := strings.Split(pattern, "*")
	if !strings.HasPrefix(subject, segments[0]) {
		return false
	}
	rest := subject[len(segments[0]):]
	prev := lastByte(segments[0])
	for _, segment := range segments[1:] {
		if segment == "" { // trailing '*'
			return wildcardSafe(prev, rest)
		}
		idx := strings.Index(rest, segment)
		if idx < 0 || !wildcardSafe(prev, rest[:idx]) {
			return false
		}
		rest = rest[idx+len(segment):]
		prev = lastByte(segment)
	}
	return rest == ""
}

func lastByte(s string) byte {
	if s == "" {
		return 0
	}
	return s[len(s)-1]
}

// wildcardSafe reports whether s is an acceptable expansion of a '*' that
// follows the literal byte prev. Beyond the metacharacter and ASCII rules, a
// wildcard may not extend a word: if the literal before it ends in an
// alphanumeric character, the expansion must start with a non-alphanumeric one,
// so `git status*` covers `git status --short` but not `git statusx --force`.
func wildcardSafe(prev byte, s string) bool {
	switch {
	case !printableASCII(s), strings.ContainsAny(s, patternMetachars), dotDotSegment(s):
		return false
	case s == "" || !alphanumeric(prev):
		return true
	default:
		return !alphanumeric(s[0])
	}
}

// dotDotSegment reports a ".." path segment (traversal); Go's "./..." is not one.
func dotDotSegment(s string) bool {
	return slices.Contains(strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == ' ' || r == '\t' }), "..")
}

func alphanumeric(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// DenyReason spells the reason recorded when a non-interactive path refuses a
// request. It names the policy and the remedy on purpose: a bare "denied" reads
// to the model as a missing capability rather than a configuration choice.
//
// Tier can be a comma-joined list (code_interpreter asks for three at once) but
// --allow takes one tier and repeats, so the remedy is rendered that way.
func DenyReason(req Request) string {
	flags := make([]string, 0, 3)
	for tier := range strings.SplitSeq(strings.TrimSpace(req.Tier), ",") {
		if tier = strings.TrimSpace(tier); tier != "" {
			flags = append(flags, "--allow "+tierFlagName(tier))
		}
	}
	if len(flags) == 0 {
		flags = append(flags, "--allow <tier>")
	}
	hint := "approval required: re-run with " + strings.Join(flags, " ")
	if req.Tool == "shell" && testCommand(req.Subject) {
		hint += " (or --allow tests, for test runs only)"
	}
	return hint + ", or add a standing grant"
}

// tierFlagName is the name --allow takes for a tier: its alias when it has
// one, so the hint never spells an internal tier name.
func tierFlagName(tier string) string {
	for alias, name := range tierAliases {
		if name == tier {
			return alias
		}
	}
	return tier
}

// DerivePattern turns a request subject into a grant pattern that
// validatePattern will accept: the first two words of the first pipeline
// segment (the subject cut at the first shell metacharacter) plus " *", so
// `go test ./...` grants `go test *`. It reports false when nothing grantable
// remains — a subject that starts with a metacharacter, a word containing
// "..", or a prefix too short to be a meaningful grant.
func DerivePattern(subject string) (string, bool) {
	if i := strings.IndexAny(subject, patternMetachars); i >= 0 {
		subject = subject[:i]
	}
	words := strings.Fields(subject)
	pattern := strings.Join(words[:min(2, len(words))], " ") + " *"
	if len(words) == 0 || validatePattern(pattern) != nil {
		return "", false
	}
	return pattern, true
}
