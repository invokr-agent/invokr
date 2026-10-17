package memory

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/invokr-ai/invokr/support/strutil"

	"github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/capability/memory/forgetting"
	"github.com/invokr-ai/invokr/capability/memory/markdown"
	"github.com/invokr-ai/invokr/capability/memory/policy"
	memoryrecall "github.com/invokr-ai/invokr/capability/memory/recall"
	"github.com/invokr-ai/invokr/capability/memory/sqlite"
	"github.com/invokr-ai/invokr/capability/memory/summary"
	memzerfoo "github.com/invokr-ai/invokr/capability/memory/zerfoo"
	"github.com/invokr-ai/invokr/capability/sessionstore"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/events"

	"go.uber.org/zap"
)

type Manager interface {
	Close() error
	PromptBlocks(ctx context.Context, req core.PromptRequest) (core.PromptBlocks, error)
	Save(ctx context.Context, rec core.Record) (core.Record, error)
	Update(ctx context.Context, id string, record core.Record) (core.Record, error)
	// UpdateMetadata refreshes a record's metadata without touching its content,
	// so bookkeeping (e.g. a doc record's file size/mtime probe) costs no re-embed.
	// Empty metadata is rejected: unlike Update, where nil means "leave metadata
	// alone", metadata is this call's entire payload, so an empty one is a caller
	// bug rather than a request to clear the column.
	UpdateMetadata(ctx context.Context, id string, metadata map[string]string) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, filter core.Query) ([]core.Record, error)
	Recall(ctx context.Context, req core.Query) ([]core.Hit, error)
	SearchArchive(ctx context.Context, query string, limit int) ([]core.Record, error)
	RestoreArchived(ctx context.Context, ids []string) ([]string, error)
}

type TieredManager struct {
	markdown   *markdown.Memory
	sqlite     *sqlite.Memory
	sessions   *sessionstore.Store
	recall     *memoryrecall.Orchestrator
	worker     memoryWorker
	summarizer memorySummarizer
	ownsWorker bool
	zerfoo     config.ZerfooMemoryConfig
	forgetting config.ForgettingConfig
	// noTitles disables session titling at shutdown.
	noTitles bool
	events   events.Dispatcher
	curator  *curator
}

// forgettingConfig projects the app-level forgetting config into the pure
// forgetting package's Config. When Enabled is false every forgetting path is a
// no-op, so behavior is identical to the pre-forgetting store.
func (m TieredManager) forgettingConfig() forgetting.Config { return forgetting.Config(m.forgetting) }

type closer interface {
	Close() error
}

type memoryWorker interface {
	closer
	Summarize(context.Context, summary.StructuredSummaryInput) (summary.MemorySummary, error)
	Embed(context.Context, []string) ([][]float32, error)
	EmbedQueries(context.Context, []string) ([][]float32, error)
}

func (m TieredManager) Close() error {
	var errs []error
	// Stop the curator's worker first so in-flight summarization finishes
	// before the stores it writes to are closed.
	if m.curator != nil {
		errs = append(errs, m.curator.Close())
	}
	// Best-effort final decay pass before the DB closes (no-op when disabled).
	m.sweep(context.Background())
	if m.sqlite != nil {
		errs = append(errs, m.sqlite.Close())
	}
	if m.worker != nil && m.ownsWorker {
		errs = append(errs, m.worker.Close())
	}
	return errors.Join(errs...)
}

func (m TieredManager) PromptBlocks(ctx context.Context, req core.PromptRequest) (core.PromptBlocks, error) {
	return m.orchestrator().PromptBlocks(ctx, req)
}

// HandleEvent is the memory manager's event-driven write path, delegated to
// curator (curator.go). A manager built as a bare struct literal (no curator
// assigned, e.g. in tests) gets an ephemeral curator with no background
// worker, so summarization runs synchronously.
func (m TieredManager) HandleEvent(ctx context.Context, ev events.Event) error {
	c := m.curator
	if c == nil {
		c = newCurator(m)
	}
	return c.HandleEvent(ctx, ev)
}

func NewEventHandler(manager Manager) events.Handler {
	if handler, ok := manager.(events.Handler); ok {
		return handler
	}
	return events.HandlerFunc(func(context.Context, events.Event) error { return nil })
}

func (m TieredManager) Save(ctx context.Context, rec core.Record) (core.Record, error) {
	m.dispatch(ctx, events.Event{Type: events.EventBeforeSave, Save: &events.SavePayload{Record: rec}})
	if rec.Source != core.SourceMarkdown {
		saved, err := m.sqlite.Save(ctx, rec)
		if err != nil {
			return core.Record{}, err
		}
		if err := m.saveVector(ctx, saved); err != nil {
			_ = m.sqlite.Delete(ctx, saved.ID)
			return core.Record{}, err
		}
		m.dispatch(ctx, events.Event{Type: events.EventAfterSave, Save: &events.SavePayload{Record: rec, Saved: &saved}})
		return saved, nil
	}

	doc, err := m.markdown.Document(rec.Scope)
	if err != nil {
		return core.Record{}, err
	}
	rec.Content = strings.TrimSpace(rec.Content)
	if rec.Content == "" {
		return core.Record{}, fmt.Errorf("content is required")
	}
	if err := doc.Add(ctx, rec.Content); err != nil {
		return core.Record{}, err
	}

	rec.ID = markdown.EntryID(rec.Content)
	rec.Source = core.SourceMarkdown
	saved := rec
	m.dispatch(ctx, events.Event{Type: events.EventAfterSave, Save: &events.SavePayload{Record: rec, Saved: &saved}})
	return rec, nil
}

// Update merges the caller's fields onto the stored record: an update naming
// only id and content keeps the stored kind, scope, session and created_at
// (issue #299). An unknown id is an error naming the id.
func (m TieredManager) Update(ctx context.Context, id string, record core.Record) (core.Record, error) {
	if record.Source != core.SourceMarkdown {
		updated, found, err := m.updateSQLiteRecord(ctx, id, record)
		if err != nil {
			return core.Record{}, err
		}
		if found {
			return updated, nil
		}
	}
	return m.updateMarkdownRecord(ctx, id, record)
}

// updateMarkdownRecord replaces a pinned markdown entry addressed by its stable
// id (or, for ids minted before stable ids, its literal text) in whichever
// document holds it.
func (m TieredManager) updateMarkdownRecord(ctx context.Context, id string, record core.Record) (core.Record, error) {
	if strings.TrimSpace(record.Content) == "" {
		return core.Record{}, fmt.Errorf("content is required")
	}
	for _, scope := range []config.MemoryScope{config.ScopeUser, config.ScopeProject} {
		doc, err := m.markdown.Document(scope)
		if err != nil {
			return core.Record{}, err
		}
		replaced, err := doc.ReplaceByID(ctx, id, record.Content)
		if err != nil {
			return core.Record{}, err
		}
		if !replaced {
			continue
		}
		record.Scope = scope
		record.ID = markdown.EntryID(record.Content)
		// Markdown entries carry no kind of their own; they list as facts.
		record.Kind = core.KindFact
		record.Source = core.SourceMarkdown
		return record, nil
	}
	return core.Record{}, fmt.Errorf("memory record %q not found", id)
}

// mergeRecord layers the caller-supplied fields of update onto the stored
// record. Empty fields mean "unchanged", so callers must not pre-fill defaults.
func mergeRecord(stored, update core.Record) core.Record {
	merged := stored
	if update.Scope != "" {
		merged.Scope = update.Scope
	}
	if update.Kind != "" {
		merged.Kind = update.Kind
	}
	merged.Content = strutil.FirstNonEmpty(update.Content, stored.Content)
	merged.Summary = strutil.FirstNonEmpty(update.Summary, stored.Summary)
	merged.SessionID = strutil.FirstNonEmpty(update.SessionID, stored.SessionID)
	if update.Metadata != nil {
		merged.Metadata = update.Metadata
	}
	return merged
}

func (m TieredManager) updateSQLiteRecord(ctx context.Context, id string, record core.Record) (core.Record, bool, error) {
	stored, found, err := m.sqlite.Get(ctx, id)
	if err != nil || !found {
		return core.Record{}, false, err
	}
	record = mergeRecord(stored, record)
	vector, err := m.vectorForRecord(ctx, record)
	if err != nil {
		return core.Record{}, false, err
	}
	var updated core.Record
	if vector == nil {
		updated, err = m.sqlite.Update(ctx, id, record)
	} else {
		updated, err = m.sqlite.UpdateWithVector(ctx, id, record, vector)
	}
	if errors.Is(err, sqlite.ErrRecordNotFound) {
		return core.Record{}, false, nil
	}
	// The stored creation time is not part of the UPDATE statement, so carry it
	// into the response instead of returning a zero timestamp.
	updated.CreatedAt = stored.CreatedAt
	return updated, err == nil, err
}

// UpdateMetadata writes a record's metadata only. Unlike Update it neither
// re-hashes the record nor recomputes its vector, so a caller refreshing
// bookkeeping does not pay for an embedding it does not need.
func (m TieredManager) UpdateMetadata(ctx context.Context, id string, metadata map[string]string) error {
	if m.sqlite == nil {
		return nil
	}
	return m.sqlite.UpdateMetadata(ctx, id, metadata)
}

func (m TieredManager) saveVector(ctx context.Context, rec core.Record) error {
	vector, err := m.vectorForRecord(ctx, rec)
	if err != nil || vector == nil {
		return err
	}
	return m.sqlite.SaveVector(ctx, rec.ID, vector)
}

func (m TieredManager) vectorForRecord(ctx context.Context, rec core.Record) ([]float32, error) {
	if m.worker == nil || rec.Source == core.SourceMarkdown {
		return nil, nil
	}
	text := strings.TrimSpace(strutil.FirstNonEmpty(rec.Summary, rec.Content))
	if text == "" {
		return nil, nil
	}
	vectors, err := m.worker.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("expected one memory vector, got %d", len(vectors))
	}
	return vectors[0], nil
}

// DeleteSession purges everything stored for one session. It stays off the
// Manager interface: only the resume picker needs it, and putting it there
// would force every test double to grow a method it never calls.
func (m TieredManager) DeleteSession(ctx context.Context, sessionID string) error {
	if m.sqlite == nil {
		return nil
	}
	return m.sqlite.DeleteSession(ctx, sessionID)
}

func (m TieredManager) Delete(ctx context.Context, id string) error {
	if err := m.sqlite.Delete(ctx, id); err != nil {
		return err
	}
	if _, err := m.markdown.User().RemoveByID(ctx, id); err != nil {
		return err
	}
	_, err := m.markdown.Project().RemoveByID(ctx, id)
	return err
}

func (m TieredManager) List(ctx context.Context, filter core.Query) ([]core.Record, error) {
	records, err := m.sqlite.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(records))
	for _, r := range records {
		seen[memoryrecall.RecordKey(r)] = struct{}{}
	}
	markdownLimit := filter.Limit
	if markdownLimit > 0 {
		markdownLimit -= len(records)
		if markdownLimit <= 0 {
			return records, nil
		}
	}
	markdownFilter := filter
	markdownFilter.Limit = markdownLimit
	markdownRecords, err := (pinnedMarkdownSource{markdown: m.markdown}).Records(ctx, markdownFilter)
	if err != nil {
		return nil, err
	}
	for _, rec := range markdownRecords {
		key := memoryrecall.RecordKey(rec)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		records = append(records, rec)
		if filter.Limit > 0 && len(records) >= filter.Limit {
			return records, nil
		}
	}
	return records, nil
}

func scopesForFilter(filter core.Query) []config.MemoryScope {
	if len(filter.Scopes) == 0 {
		return []config.MemoryScope{config.ScopeUser, config.ScopeProject}
	}

	scopes := make([]config.MemoryScope, 0, len(filter.Scopes))
	for _, scope := range filter.Scopes {
		if (scope == config.ScopeUser || scope == config.ScopeProject) && !slices.Contains(scopes, scope) {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

func (m TieredManager) Recall(ctx context.Context, req core.Query) ([]core.Hit, error) {
	m.dispatch(ctx, events.Event{Type: events.EventBeforeRecall, Recall: &events.RecallPayload{Request: req}})
	hits, err := m.orchestrator().Recall(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		// Raw turns only become searchable records after the summary threshold
		// (default 12 events). Until then, fall back to recent raw turns so
		// early-conversation recall isn't blind.
		hits, err = m.recallRawEvents(ctx, req)
		if err != nil {
			return nil, err
		}
	}
	hits = m.applyRetention(ctx, hits)
	m.dispatch(ctx, events.Event{Type: events.EventAfterRecall, Recall: &events.RecallPayload{Request: req, Hits: hits}})
	return hits, nil
}

// applyRetention re-ranks hits by their decay-adjusted retention and reinforces
// the recalled records (bump recall_count, reset last_recalled_at, scale
// strength). It is a no-op when forgetting is disabled, so ranking is byte-for-
// byte identical to today. Touch/scan failures are logged, never failing recall.
func (m TieredManager) applyRetention(ctx context.Context, hits []core.Hit) []core.Hit {
	cfg := m.forgettingConfig()
	if !cfg.Enabled || len(hits) == 0 || m.sqlite == nil {
		return hits
	}
	hitIDs := make([]string, len(hits))
	for i := range hits {
		hitIDs[i] = hits[i].Record.ID
	}
	cands, err := m.sqlite.QueryRetentionRecords(ctx, hitIDs)
	if err != nil {
		zap.L().Debug("memory retention re-rank scan failed", zap.Error(err))
		return hits
	}
	byID := make(map[string]forgetting.Record, len(cands))
	for _, rec := range cands {
		byID[rec.ID] = rec
	}
	now := time.Now().UTC()
	ids := make([]string, 0, len(hits))
	for i := range hits {
		if rec, ok := byID[hits[i].Record.ID]; ok {
			hits[i].Score *= forgetting.Retention(rec, now, cfg)
			ids = append(ids, hits[i].Record.ID)
		}
	}
	core.SortHitsByScore(hits)
	if err := m.sqlite.TouchRecall(ctx, ids, now, cfg.ReinforcementFactor, cfg.MaxStrength); err != nil {
		zap.L().Debug("memory recall reinforcement failed", zap.Error(err))
	}
	return hits
}

// SearchArchive is the only read path for forgotten (archived) records. It runs
// a lexical scan over cold storage; the recall_archive tool gates every call
// behind explicit user approval.
func (m TieredManager) SearchArchive(ctx context.Context, query string, limit int) ([]core.Record, error) {
	if m.sqlite == nil {
		return nil, nil
	}
	return m.sqlite.SearchArchive(ctx, query, limit)
}

// RestoreArchived moves archived records back to active (strength reset to 1.0)
// and emits an audit event naming them. It returns the record IDs restored.
func (m TieredManager) RestoreArchived(ctx context.Context, ids []string) ([]string, error) {
	if m.sqlite == nil {
		return nil, nil
	}
	restored := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			restored = append(restored, id)
		}
	}
	if len(restored) == 0 {
		return nil, nil
	}
	if err := m.sqlite.Restore(ctx, restored); err != nil {
		return nil, err
	}
	m.audit(ctx, "memory_restore", "restored", map[string]any{"restored": restored, "restored_count": len(restored)})
	return restored, nil
}

// recentContextScore marks raw-turn fallback hits as low-confidence recent
// context, keeping them distinguishable from real ranked recall hits.
const recentContextScore = 0.1

const (
	crossSessionRawLimit   = 10
	crossSessionRawMaxAge  = 30 * 24 * time.Hour
	crossSessionProvenance = "cross-session context"
)

func (m TieredManager) recallRawEvents(ctx context.Context, req core.Query) ([]core.Hit, error) {
	// This fallback only serves "recent conversation" recall: session-scoped
	// episodes. A query explicitly asking for other kinds or non-session scopes
	// must not receive raw turns.
	if (len(req.Kinds) > 0 && !slices.Contains(req.Kinds, core.KindEpisode)) ||
		(len(req.Scopes) > 0 && !slices.Contains(req.Scopes, config.ScopeSession)) {
		return nil, nil
	}
	limit := cmp.Or(max(req.Limit, 0), 20)
	memoryEvents, err := m.sqlite.RecentMemoryEvents(ctx, req.SessionID, limit)
	if err != nil {
		return nil, err
	}
	crossSession := false
	if len(memoryEvents) == 0 && strings.TrimSpace(req.SessionID) != "" {
		// The current session has no turns yet (e.g. the first message asks to
		// recall a prior conversation). Fall back to recent turns across all
		// sessions so cross-session recall isn't blind.
		memoryEvents, err = m.sqlite.RecentMemoryEvents(ctx, "", min(limit, crossSessionRawLimit))
		if err != nil {
			return nil, err
		}
		crossSession = true
		cutoff := time.Now().UTC().Add(-crossSessionRawMaxAge)
		memoryEvents = slices.DeleteFunc(memoryEvents, func(ev core.MemoryEventRecord) bool { return ev.CreatedAt.Before(cutoff) })
	}
	memoryEvents = filterEventsByQuery(memoryEvents, req.Query)
	provenance := ""
	if crossSession {
		provenance = crossSessionProvenance
	}
	hits := make([]core.Hit, 0, len(memoryEvents))
	for _, ev := range memoryEvents {
		hits = append(hits, core.Hit{Score: recentContextScore, Provenance: provenance, Record: core.Record{
			ID: ev.ID, Scope: config.ScopeSession, Kind: core.KindEpisode, Source: core.SourceRuntime,
			Content: ev.Content, SessionID: ev.SessionID, CreatedAt: ev.CreatedAt}})
	}
	return hits, nil
}

// filterEventsByQuery keeps events whose content matches any query term (>2
// chars, case-insensitive substring). If the query has no usable terms or no
// event matches, it returns the input unchanged so recency-only queries like
// "what did I last say" still work. ponytail: substring match; swap for FTS if
// precision matters.
func filterEventsByQuery(events []core.MemoryEventRecord, query string) []core.MemoryEventRecord {
	terms := slices.DeleteFunc(strings.Fields(strings.ToLower(query)), func(t string) bool { return len(t) <= 2 })
	if len(terms) == 0 {
		return events
	}
	matched := slices.DeleteFunc(slices.Clone(events), func(ev core.MemoryEventRecord) bool {
		content := strings.ToLower(ev.Content)
		return !slices.ContainsFunc(terms, func(t string) bool { return strings.Contains(content, t) })
	})
	if len(matched) == 0 {
		return events
	}
	return matched
}

// NewManagerFromConfigWithProvider wires both the (optional) zerfoo worker for
// embeddings/vector recall and the main chat provider for summarization. Pass a
// nil worker when zerfoo is disabled; chat summarization still runs.
func NewManagerFromConfigWithProvider(app config.AppConfig, dispatcher events.Dispatcher, worker memoryWorker, chat ChatSummarizer, models ModelSource, logger ...*zap.Logger) (Manager, error) {
	app = app.WithDefaults()
	var s memorySummarizer
	if chat != nil {
		// The chat summarizer hits the same provider endpoint as the agent, which
		// requires an explicit model name; without it llama.cpp rejects every call.
		model := strings.TrimSpace(app.Agent.SummarizerModel)
		if model == "" {
			model = app.Provider.Model
		} else {
			models = nil
		}
		s = chatSummarizer{client: chat, model: model, models: models}
	}
	return newManagerFromConfigWithEvents(app, dispatcher, worker, s, logger...)
}

func newManagerFromConfigWithEvents(app config.AppConfig, dispatcher events.Dispatcher, sharedWorker memoryWorker, summarizer memorySummarizer, logger ...*zap.Logger) (Manager, error) {
	app = app.WithDefaults()
	if dispatcher == nil {
		return nil, errors.New("memory manager requires event dispatcher")
	}

	sqliteMemory, err := sqlite.NewStore(sqlite.Config{Dir: app.Memory.Dir})
	if err != nil {
		return nil, err
	}
	markdownMemory, err := markdown.NewMemory(markdown.Config{Dir: app.Memory.Dir,
		ProjectDir: app.Memory.ProjectDir, WorkspaceRoot: app.Workspace.Root})
	if err != nil {
		_ = sqliteMemory.Close()
		return nil, err
	}
	var lexical memoryrecall.LexicalSource = sqliteLexicalSource{sqlite: sqliteMemory}
	var activeWorker memoryWorker
	ownsWorker := false
	recallPolicy := policy.Default
	if sharedWorker != nil {
		recallPolicy.Tier2.Backend = constants.MemoryRecallVectorBackend
		activeWorker = sharedWorker
		lexical = vectorSource{embedder: sharedWorker, store: sqliteMemory}
	} else if app.Memory.Zerfoo.Enabled {
		recallPolicy.Tier2.Backend = constants.MemoryRecallVectorBackend
		// Lazy: models load on first embed/summarize, not here.
		worker := memzerfoo.NewWorker(app.Memory.Zerfoo)
		activeWorker = worker
		ownsWorker = true
		lexical = vectorSource{embedder: worker, store: sqliteMemory}
	}
	m := TieredManager{markdown: markdownMemory, sqlite: sqliteMemory,
		sessions: sessionstore.NewForWorkspace(app.Memory.Dir, app.Workspace.Root, cmp.Or(append(slices.Clip(logger), zap.NewNop())...)),
		recall:   memoryrecall.NewOrchestrator(pinnedMarkdownSource{markdown: markdownMemory}, lexical, recallPolicy),
		worker:   activeWorker, summarizer: summarizer, ownsWorker: ownsWorker,
		zerfoo: app.Memory.Zerfoo, forgetting: app.Memory.Forgetting,
		noTitles: app.Memory.DisableSessionTitles, events: dispatcher}
	m.curator = newCurator(m)
	m.curator.startWorker()
	return m, nil
}

func (m TieredManager) orchestrator() *memoryrecall.Orchestrator {
	if m.recall != nil {
		return m.recall
	}
	return memoryrecall.NewOrchestrator(pinnedMarkdownSource{markdown: m.markdown}, sqliteLexicalSource{sqlite: m.sqlite}, policy.Default)
}

func (m TieredManager) dispatch(ctx context.Context, ev events.Event) { m.events.Dispatch(ctx, ev) }

// audit emits one audit event naming the action, its outcome and the records it
// touched.
func (m TieredManager) audit(ctx context.Context, action, outcome string, details map[string]any) {
	m.dispatch(ctx, events.Event{Type: events.EventAudit,
		Audit: &events.AuditPayload{Action: action, Outcome: outcome, Details: details}})
}

// sweep runs a best-effort decay maintenance pass and emits an audit event
// naming the archived/purged/pruned records. It is a no-op when forgetting is
// disabled. Errors never propagate — sweeping is a background lifecycle chore.
func (m TieredManager) sweep(ctx context.Context) {
	cfg := m.forgettingConfig()
	if !cfg.Enabled || m.sqlite == nil {
		return
	}
	res, err := forgetting.NewSweeper(m.sqlite, cfg).Sweep(ctx)
	if err != nil {
		zap.L().Debug("memory forgetting sweep failed", zap.Error(err))
		return
	}
	if res.Empty() {
		return
	}
	m.audit(ctx, "memory_forget", "swept", map[string]any{"archived": res.Archived,
		"archived_count": len(res.Archived), "purged": res.Purged, "pruned_events": res.Pruned})
}
