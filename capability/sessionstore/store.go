package sessionstore

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/invokr-ai/invokr/support/strutil"

	"github.com/invokr-ai/invokr/platform/turn"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Message is one transcript line. The JSONL format is deliberately additive:
// tool fields are optional and omitted when empty, so files written before they
// existed still load, and older readers ignore them as unknown JSON keys.
type Message struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Role      string    `json:"role"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
	// ToolCalls is the runner's outcome ledger for the turn (ADR 0001) — the
	// authoritative record of what ran, not model prose.
	ToolCalls         []turn.ToolOutcome       `json:"tool_calls,omitempty"`
	ToolCallCount     int                      `json:"tool_call_count,omitempty"`
	ExecutionEvidence []turn.ExecutionEvidence `json:"execution_evidence"`
	// Cwd is the workspace root the message was written from. Empty on
	// transcripts written before sessions were directory-scoped.
	Cwd string `json:"cwd,omitempty"`
	// Interrupted marks a turn that was cut short (SIGINT/SIGTERM) rather than
	// answered. It is what makes a killed run resumable: the transcript is
	// partial by construction, and the reader has to know that.
	Interrupted bool `json:"interrupted,omitempty"`
}

// ToolRecord carries the runner ledger for a turn into the transcript.
type ToolRecord struct {
	Evidence []turn.ExecutionEvidence
	Calls    []turn.ToolOutcome
	Count    int
}

type Store struct {
	dir, cwd string
	now      func() time.Time
	logger   *zap.Logger
}

// Cwd is the workspace root new messages are stamped with, the one InCwd
// filtering compares against.
func (s *Store) Cwd() string { return s.cwd }

type SessionInfo struct {
	ID        string    `json:"id"`
	UpdatedAt time.Time `json:"updated_at"`
	Messages  int       `json:"messages"`
	// Title is the generated one-line label, empty when none has been written.
	Title string `json:"title,omitempty"`
	// Preview is the first user message, truncated — the fallback label when
	// Title is empty, so a listing is never reduced to bare UUIDs.
	Preview string `json:"preview,omitempty"`
	// Last is the most recent message in the transcript, truncated. It answers
	// "where did I leave off?", which the first message cannot: Preview is
	// already spoken for as the label fallback.
	Last string `json:"last,omitempty"`
	// Cwd is the workspace root the session was started in, empty for
	// transcripts written before sessions were directory-scoped.
	Cwd string `json:"cwd,omitempty"`
	// Interrupted reports that the transcript ends on a cut-short turn: the
	// session was killed mid-run and has unfinished work.
	Interrupted bool `json:"interrupted,omitempty"`
}

// InCwd reports whether the session belongs to root. A session without a
// recorded cwd belongs to no directory, so it only ever shows in an "all" view.
func (s SessionInfo) InCwd(root string) bool { return s.Cwd != "" && s.Cwd == root }

// FilterByCwd keeps only the sessions belonging to root, preserving order.
func FilterByCwd(sessions []SessionInfo, root string) []SessionInfo {
	return slices.DeleteFunc(append([]SessionInfo{}, sessions...), func(info SessionInfo) bool { return !info.InCwd(root) })
}

// Label is the best human-readable name for the session: the generated title,
// falling back to the first-message preview, falling back to the ID.
func (s SessionInfo) Label() string { return cmp.Or(s.Title, s.Preview, s.ID) }

type SearchHit struct {
	SessionID string    `json:"session_id"`
	MessageID string    `json:"message_id"`
	Role      string    `json:"role"`
	Snippet   string    `json:"snippet"`
	Score     int       `json:"score"`
	CreatedAt time.Time `json:"created_at"`
}

const maxTranscriptLineBytes = 10 << 20

// previewLimit caps a session label so a picker row stays on one line.
const previewLimit = 72

// NewForWorkspace is New with the workspace root that scopes the store already
// set. Prefer it over New wherever a workspace root is at hand.
func NewForWorkspace(dir string, root string, logger ...*zap.Logger) *Store {
	s := New(dir, logger...)
	s.cwd = root
	return s
}

func New(dir string, logger ...*zap.Logger) *Store {
	l := zap.NewNop()
	if len(logger) > 0 && logger[0] != nil {
		l = logger[0]
	}
	return &Store{dir: filepath.Join(dir, "sessions"), now: func() time.Time { return time.Now().UTC() }, logger: l}
}

func (s *Store) Append(ctx context.Context, sessionID string, role string, text string) (Message, error) {
	return s.AppendTurn(ctx, sessionID, role, text, ToolRecord{})
}

// AppendTurn appends a message together with the turn's tool ledger.
func (s *Store) AppendTurn(ctx context.Context, sessionID string, role string, text string, record ToolRecord) (Message, error) {
	return s.append(ctx, sessionID, role, text, record, false)
}

// AppendInterrupted appends a turn that was cut short instead of answered, so
// the partial transcript survives a SIGTERM and `--last` can find it.
func (s *Store) AppendInterrupted(ctx context.Context, sessionID string, role string, text string, record ToolRecord) (Message, error) {
	return s.append(ctx, sessionID, role, text, record, true)
}

func (s *Store) append(ctx context.Context, sessionID string, role string, text string, record ToolRecord, interrupted bool) (Message, error) {
	if err := ctx.Err(); err != nil {
		return Message{}, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return Message{}, fmt.Errorf("session id is required")
	}
	msg := Message{
		ID: uuid.NewString(), SessionID: sessionID, Role: strings.TrimSpace(role), Text: text, CreatedAt: s.now(),
		ToolCalls: record.Calls, ToolCallCount: record.Count, ExecutionEvidence: record.Evidence, Cwd: s.cwd, Interrupted: interrupted}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return Message{}, fmt.Errorf("session store: create dir: %w", err)
	}
	f, err := os.OpenFile(s.path(sessionID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return Message{}, fmt.Errorf("session store: open transcript: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err := json.NewEncoder(f).Encode(msg); err != nil {
		return Message{}, fmt.Errorf("session store: append transcript: %w", err)
	}
	return msg, nil
}

func (s *Store) Load(_ context.Context, sessionID string) ([]Message, error) {
	var messages []Message
	if err := s.forEachMessage(sessionID, func(line int, msg Message, parseErr error) {
		if parseErr != nil {
			s.logger.Warn("session store: skipping unparseable transcript line", zap.String("session_id", sessionID), zap.Int("line", line), zap.Error(parseErr))
			return
		}
		messages = append(messages, msg)
	}); err != nil {
		return nil, err
	}
	return messages, nil
}

// forEachMessage streams one transcript line by line, handing each decoded
// message (or the decode error) to fn. Callers decide what a corrupt line costs.
func (s *Store) forEachMessage(sessionID string, fn func(line int, msg Message, parseErr error)) error {
	f, err := os.Open(s.path(sessionID))
	if err != nil {
		return fmt.Errorf("session store: open transcript: %w", err)
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), maxTranscriptLineBytes)
	line := 0
	for scanner.Scan() {
		line++
		var msg Message
		parseErr := json.Unmarshal(scanner.Bytes(), &msg)
		fn(line, msg, parseErr)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("session store: read transcript: %w", err)
	}
	return nil
}

// eachTranscript calls fn for every .jsonl transcript in the store directory.
// A missing directory is an empty store, not an error.
func (s *Store) eachTranscript(fn func(entry os.DirEntry, id string)) error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		fn(entry, strings.TrimSuffix(entry.Name(), ".jsonl"))
	}
	return nil
}

// LastResumableInCwd is the session "resume the last one" should attach to:
// the newest interrupted session in this store's workspace root, falling back
// to the newest session of any status. The preference matters because an
// interrupted run is the one with unfinished work — without it, `--last`
// silently reattached to an older, already-finished task in the same directory
// and answered about that instead.
//
// It reads every transcript rather than only their mtimes, because the cwd
// lives inside the file — the same cost the picker already pays.
func (s *Store) LastResumableInCwd() (SessionInfo, error) {
	sessions, err := s.List()
	if err != nil {
		return SessionInfo{}, err
	}
	sessions = FilterByCwd(sessions, s.cwd) // List is sorted newest-first
	if len(sessions) == 0 {
		return SessionInfo{}, fmt.Errorf("no sessions found in %s", s.cwd)
	}
	i := max(slices.IndexFunc(sessions, func(info SessionInfo) bool { return info.Interrupted }), 0)
	return sessions[i], nil
}

func (s *Store) List() ([]SessionInfo, error) {
	sessions := []SessionInfo{}
	err := s.eachTranscript(func(entry os.DirEntry, id string) {
		info, err := entry.Info()
		if err != nil {
			return
		}
		scan, err := s.scanTranscript(id)
		if err != nil {
			return
		}
		sessions = append(sessions, SessionInfo{ID: id, UpdatedAt: info.ModTime(), Messages: scan.count,
			Title: s.Title(id), Preview: scan.preview, Last: scan.last, Cwd: scan.cwd, Interrupted: scan.interrupted})
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(sessions, func(a, b SessionInfo) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	return sessions, nil
}

// transcriptScan is what one pass over a transcript yields.
type transcriptScan struct {
	count              int
	preview, last, cwd string
	// interrupted mirrors the last message's flag: a session is interrupted
	// only while its final turn is the cut-short one, so resuming clears it.
	interrupted bool
}

// scanTranscript counts lines and grabs the first user message, the last
// message of any role, and the recorded workspace root in one pass. The count
// already required reading the whole file, so the rest is close to free.
func (s *Store) scanTranscript(sessionID string) (transcriptScan, error) {
	var scan transcriptScan
	err := s.forEachMessage(sessionID, func(line int, msg Message, parseErr error) {
		scan.count = line
		if parseErr != nil {
			return // a corrupt line costs a preview, never the listing
		}
		if scan.cwd == "" {
			scan.cwd = msg.Cwd
		}
		scan.interrupted = msg.Interrupted
		if text := truncateLabel(strings.TrimSpace(msg.Text)); text != "" {
			scan.last = text
			if scan.preview == "" && msg.Role == "user" {
				scan.preview = text
			}
		}
	})
	if err != nil {
		return transcriptScan{}, err
	}
	return scan, nil
}

// truncateLabel collapses whitespace and clips to a single short line. It cuts
// on runes, not bytes, so a multi-byte character is never split in half.
func truncateLabel(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= previewLimit {
		return text
	}
	return strings.TrimSpace(string(runes[:previewLimit])) + "…"
}

// titlePath is the sidecar holding the generated label. It sits beside the
// transcript; List and LastSessionID only consider ".jsonl", so it is inert.
func (s *Store) titlePath(sessionID string) string {
	return filepath.Join(s.dir, filepath.Base(strings.TrimSpace(sessionID))+".title")
}

// Title returns the stored label, or "" when none has been written.
func (s *Store) Title(sessionID string) string {
	data, err := os.ReadFile(s.titlePath(sessionID))
	if err != nil {
		return ""
	}
	return truncateLabel(string(data))
}

// Delete removes a session's transcript and its title sidecar. A missing
// sidecar is not an error — most sessions never get one. Curated memory records
// are a separate tier and are deliberately left alone.
func (s *Store) Delete(sessionID string) error {
	if err := os.Remove(s.path(sessionID)); err != nil {
		return fmt.Errorf("session store: delete transcript: %w", err)
	}
	if err := os.Remove(s.titlePath(sessionID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("session store: delete title: %w", err)
	}
	return nil
}

// SetTitle writes the label sidecar, replacing any existing one.
func (s *Store) SetTitle(sessionID, title string) error {
	title = truncateLabel(title)
	if title == "" {
		return fmt.Errorf("session store: empty title")
	}
	// No MkdirAll: a title is only ever written for a session whose transcript
	// already exists, and Append created the directory for that.
	if err := os.WriteFile(s.titlePath(sessionID), []byte(title), 0600); err != nil {
		return fmt.Errorf("session store: write title: %w", err)
	}
	return nil
}

// Search scans every session. SearchInCwd is the directory-scoped variant.
func (s *Store) Search(ctx context.Context, query string, maxResults int) ([]SearchHit, error) {
	return s.search(ctx, query, maxResults, false)
}

// SearchInCwd searches only sessions belonging to this store's workspace root.
func (s *Store) SearchInCwd(ctx context.Context, query string, maxResults int) ([]SearchHit, error) {
	return s.search(ctx, query, maxResults, true)
}

// messagesInCwd reports whether a transcript was recorded under root. It reads
// the first stamped message, matching how List derives a session's cwd.
func messagesInCwd(messages []Message, root string) bool {
	i := slices.IndexFunc(messages, func(msg Message) bool { return msg.Cwd != "" })
	return i >= 0 && messages[i].Cwd == root
}

func (s *Store) search(ctx context.Context, query string, maxResults int, scoped bool) ([]SearchHit, error) {
	tokens := strings.Fields(strings.ToLower(query))
	for i, part := range tokens {
		tokens[i] = strings.Trim(part, ".,:;!?()[]{}\"'")
	}
	tokens = slices.DeleteFunc(tokens, func(t string) bool { return t == "" })
	if len(tokens) == 0 {
		return nil, fmt.Errorf("query is required")
	}
	if maxResults <= 0 {
		maxResults = 5
	}
	var hits []SearchHit
	if err := s.eachTranscript(func(_ os.DirEntry, id string) {
		messages, err := s.Load(ctx, id)
		if err != nil {
			return
		}
		if scoped && !messagesInCwd(messages, s.cwd) {
			return
		}
		hits = append(hits, searchMessages(messages, tokens)...)
	}); err != nil {
		return nil, err
	}
	slices.SortFunc(hits, func(a, b SearchHit) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), b.CreatedAt.Compare(a.CreatedAt))
	})
	if len(hits) > maxResults {
		hits = hits[:maxResults]
	}
	return hits, nil
}

func (s *Store) path(sessionID string) string {
	return filepath.Join(s.dir, filepath.Base(strings.TrimSpace(sessionID))+".jsonl")
}

func searchMessages(messages []Message, tokens []string) []SearchHit {
	hits := make([]SearchHit, 0)
	for _, msg := range messages {
		lower := strings.ToLower(msg.Text)
		score := 0
		for _, token := range tokens {
			score += strings.Count(lower, token)
		}
		if score == 0 {
			continue
		}
		hits = append(hits, SearchHit{SessionID: msg.SessionID, MessageID: msg.ID, Role: msg.Role, Snippet: snippet(msg.Text, tokens[0]), Score: score, CreatedAt: msg.CreatedAt})
	}
	return hits
}

func snippet(text string, token string) string {
	const limit = 240
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	lower := strings.ToLower(text)
	index, start := strings.Index(lower, strings.ToLower(token)), 0
	if index >= 0 {
		// Lowercasing can change byte widths; map the rune position back first.
		remaining := utf8.RuneCountInString(lower[:index])
		for offset := range text {
			if remaining == 0 {
				start = max(offset-limit/2, 0)
				break
			}
			remaining--
		}
	}
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	return strings.TrimSpace(strutil.TruncateUTF8(text[start:], limit))
}
