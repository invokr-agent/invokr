package sqlite

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/invokr-agent/invokr/support/strutil"

	"github.com/invokr-agent/invokr/capability/memory/core"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/support/mathutil"

	"go.uber.org/zap"

	"github.com/google/uuid"
	"github.com/stytchauth/sqx"
	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	statusActive  = "active"
	statusDeleted = "deleted"

	defaultLimit = 20

	vectorCandidateFloor      = 200
	vectorCandidateMultiplier = 20
	vectorCandidateCap        = 2000
)

var ErrRecordNotFound = errors.New("sqlite memory store: record not found")

var (
	_ core.RecordStore  = (*Memory)(nil)
	_ core.LexicalStore = (*Memory)(nil)
	_ core.VectorStore  = (*Memory)(nil)
	_ core.EventStore   = (*Memory)(nil)
)

type Config struct {
	Dir string
}

type Memory struct {
	db *sql.DB
	mu sync.Mutex
}

func NewStore(cfg Config) (*Memory, error) {
	dbPath := filepath.Join(cfg.Dir, "memory.db")
	db, err := InitDbAtPath(dbPath)
	if err != nil {
		return nil, err
	}

	return &Memory{db: db}, nil
}

func (s *Memory) Close() error { return CloseDB(s.db) }

// CloseDB closes db; a store that never opened one has nothing to close.
func CloseDB(db *sql.DB) error {
	if db == nil {
		return nil
	}
	return db.Close()
}

func CheckOpen(db *sql.DB, store string) error {
	if db == nil {
		return fmt.Errorf("%s: nil store", store)
	}
	return nil
}

func normalizeRecord(rec core.Record) (core.Record, error) {
	rec.Content = strings.TrimSpace(rec.Content)
	if rec.Content == "" {
		return rec, fmt.Errorf("sqlite memory store: content is required")
	}
	rec.Summary = strings.TrimSpace(rec.Summary)
	rec.Scope = cmp.Or(rec.Scope, config.ScopeProject)
	rec.Kind = cmp.Or(rec.Kind, core.KindFact)
	rec.Source = cmp.Or(rec.Source, core.SourceSQLite)
	return rec, nil
}

func (s *Memory) Save(ctx context.Context, rec core.Record) (core.Record, error) {
	if err := s.check(); err != nil {
		return core.Record{}, err
	}
	var err error
	if rec, err = normalizeRecord(rec); err != nil {
		return core.Record{}, err
	}

	hash := strutil.HashHexTrimmed(core.DedupeIdentity(rec))
	id, err := uuid.NewRandom()
	if err != nil {
		return core.Record{}, fmt.Errorf("sqlite memory store: generate uuid: %w", err)
	}
	idString := id.String()

	now := core.UTCNow()

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = sqx.Write(ctx).WithQueryable(s.db).Insert("memory_records").
		Columns("uuid", "scope", "kind", "content", "summary", "source",
			"session_id", "status", "content_hash", "metadata_json", "created_at", "updated_at").
		Values(idString, string(rec.Scope), string(rec.Kind), rec.Content, nullableString(rec.Summary),
			string(rec.Source), nullableString(rec.SessionID), statusActive, hash,
			metadataToJSON(rec.Metadata), now, now).DoResult()

	if err != nil {
		if IsUniqueConstraint(err) {
			existing, findErr := s.findActiveByHash(ctx, rec.Scope, rec.Kind, hash)
			if findErr != nil {
				return core.Record{}, fmt.Errorf("sqlite memory store: find duplicate: %w", findErr)
			}
			return existing, nil
		}
		return core.Record{}, fmt.Errorf("sqlite memory store: save record: %w", err)
	}

	rec.ID = idString
	rec.CreatedAt = parseDBTime(now)
	rec.UpdatedAt = rec.CreatedAt

	return rec, nil
}

func IsUniqueConstraint(err error) bool {
	var sqliteErr *sqlitedriver.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}

func (s *Memory) List(ctx context.Context, filter core.Query) ([]core.Record, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(filter.Query) != "" {
		rows, err := s.queryFTSRows(ctx, filter)
		if err != nil {
			return nil, fmt.Errorf("sqlite memory store: list fts: %w", err)
		}
		return rowsToRecords(rows), nil
	}

	builder := sqx.Read[recordRow](ctx).WithQueryable(s.db).Select(recordColumns("")...).From("memory_records").
		Where(sqx.Eq{"status": statusActive}).OrderBy("updated_at DESC", "id DESC").
		Limit(uint64(normalizedLimit(filter.Limit)))

	builder = applyRecordFilters(builder, "", filter, filter.SessionID)

	rows, err := builder.All()
	if err != nil {
		return nil, fmt.Errorf("sqlite memory store: list records: %w", err)
	}

	return rowsToRecords(rows), nil
}

func (s *Memory) Update(ctx context.Context, id string, rec core.Record) (core.Record, error) {
	return s.update(ctx, id, rec, nil)
}

// Get returns one active record by id. The bool reports whether it exists, so
// callers can merge an update onto the stored record instead of overwriting it
// with defaults.
func (s *Memory) Get(ctx context.Context, id string) (core.Record, bool, error) {
	if err := s.check(); err != nil {
		return core.Record{}, false, err
	}
	row, err := sqx.Read[recordRow](ctx).WithQueryable(s.db).Select(recordColumns("")...).From("memory_records").
		Where(sqx.Eq{"uuid": strings.TrimSpace(id), "status": statusActive}).One()
	if errors.Is(err, sql.ErrNoRows) {
		return core.Record{}, false, nil
	}
	if err != nil {
		return core.Record{}, false, fmt.Errorf("sqlite memory store: find record: %w", err)
	}
	return recordFromRow(*row), true, nil
}

// UpdateMetadata refreshes only a record's metadata_json. It exists so callers
// can persist bookkeeping — a doc record's file size/mtime probe — without
// touching content, which would re-hash the record and, one layer up, re-embed
// it. updated_at is deliberately left alone: nothing about the record's meaning
// changed, and recall orders on it.
//
// Empty metadata is an error, not a wholesale clear and not a silent no-op.
// update treats nil metadata as "unchanged" because it has other columns to
// write; here metadata is the entire payload, so an empty one is a caller bug.
// Both refusals serve the same invariant: a doc record's identity lives in its
// workspace/path metadata while content_hash still encodes them, so clearing it
// would orphan the row and make the next sync insert a duplicate. Failing loudly
// also stops a caller believing a probe was refreshed when nothing was written,
// which would silently reintroduce the re-read-forever bug this method fixes.
func (s *Memory) UpdateMetadata(ctx context.Context, id string, metadata map[string]string) error {
	if err := s.check(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrRecordNotFound
	}
	if len(metadata) == 0 {
		return fmt.Errorf("sqlite memory store: metadata is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := sqx.Write(ctx).WithQueryable(s.db).Update("memory_records").
		Set("metadata_json", metadataToJSON(metadata)).Where(sqx.Eq{"uuid": id, "status": statusActive}).DoResult()
	if err != nil {
		return fmt.Errorf("sqlite memory store: update metadata: %w", err)
	}
	return affectedOne(result, "update metadata rows affected")
}

// affectedOne reports ErrRecordNotFound when a targeted UPDATE matched no row;
// label names the operation in the RowsAffected failure.
func affectedOne(result sql.Result, label string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite memory store: %s: %w", label, err)
	}
	if affected == 0 {
		return ErrRecordNotFound
	}
	return nil
}

// UpdateWithVector updates a record and its vector in one transaction.
func (s *Memory) UpdateWithVector(ctx context.Context, id string, rec core.Record, vector []float32) (core.Record, error) {
	if len(vector) == 0 {
		return core.Record{}, fmt.Errorf("sqlite memory store: vector is required")
	}
	return s.update(ctx, id, rec, vector)
}

func (s *Memory) update(ctx context.Context, id string, rec core.Record, vector []float32) (core.Record, error) {
	if err := s.check(); err != nil {
		return core.Record{}, err
	}

	id = strings.TrimSpace(id)
	if id == "" {
		return core.Record{}, ErrRecordNotFound
	}

	var err error
	if rec, err = normalizeRecord(rec); err != nil {
		return core.Record{}, err
	}

	now := core.UTCNow()
	hash := strutil.HashHexTrimmed(core.DedupeIdentity(rec))
	var vectorBlob []byte
	var vectorNormValue float64
	if vector != nil {
		if vectorBlob, vectorNormValue, err = encodeVector(vector); err != nil {
			return core.Record{}, err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Record{}, fmt.Errorf("sqlite memory store: update record: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	updateBuilder := sqx.Write(ctx).WithQueryable(tx).Update("memory_records").Set("scope", string(rec.Scope)).
		Set("kind", string(rec.Kind)).Set("content", rec.Content).Set("summary", nullableString(rec.Summary)).
		Set("source", string(rec.Source)).Set("session_id", nullableString(rec.SessionID)).Set("content_hash", hash).
		Set("updated_at", now)
	// Nil metadata means "unchanged", never "clear it". Overwriting would drop a
	// doc record's workspace/path keys — which are its identity — for any caller
	// that updates without re-supplying them.
	if rec.Metadata != nil {
		updateBuilder = updateBuilder.Set("metadata_json", metadataToJSON(rec.Metadata))
	}

	result, err := updateBuilder.Where(sqx.Eq{"uuid": id, "status": statusActive}).DoResult()
	if err != nil {
		return core.Record{}, fmt.Errorf("sqlite memory store: update record: %w", err)
	}

	if err := affectedOne(result, "update rows affected"); err != nil {
		return core.Record{}, err
	}
	if err := updateVector(ctx, tx, id, vector, vectorBlob, vectorNormValue, now); err != nil {
		return core.Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Record{}, fmt.Errorf("sqlite memory store: update record: commit transaction: %w", err)
	}

	rec.ID = id
	rec.UpdatedAt = parseDBTime(now)
	return rec, nil
}

// updateVector upserts a record's vector inside the update transaction. A nil
// vector means "unchanged", so nothing is written.
func updateVector(ctx context.Context, tx *sql.Tx, id string, vector []float32, blob []byte, norm float64, now string) error {
	if vector == nil {
		return nil
	}
	if err := upsertVector(ctx, tx, id, len(vector), blob, norm, now); err != nil {
		return fmt.Errorf("sqlite memory store: update vector: %w", err)
	}
	return nil
}

func upsertVector(ctx context.Context, q sqx.Queryable, id string, dims int, blob []byte, norm float64, now string) error {
	return sqx.Write(ctx).WithQueryable(q).Insert("memory_vectors").
		Columns("record_uuid", "vector_json", "vector_blob", "vector_dims", "vector_norm", "created_at", "updated_at").
		Values(id, "[]", blob, dims, norm, now, now).
		Suffix("ON CONFLICT(record_uuid) DO UPDATE SET vector_json = excluded.vector_json, vector_blob = excluded.vector_blob, vector_dims = excluded.vector_dims, vector_norm = excluded.vector_norm, updated_at = excluded.updated_at").
		Do()
}

func (s *Memory) Delete(ctx context.Context, id string) error {
	if err := s.check(); err != nil {
		return err
	}

	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.markDeleted(ctx, sqx.Eq{"uuid": id, "status": statusActive}, "delete record")
}

// markDeleted soft-deletes every active record matching where, stamping the
// same timestamp into deleted_at and updated_at. Callers must hold s.mu.
func (s *Memory) markDeleted(ctx context.Context, where sqx.Eq, label string) error {
	now := core.UTCNow()
	if err := sqx.Write(ctx).WithQueryable(s.db).Update("memory_records").Set("status", statusDeleted).
		Set("deleted_at", now).Set("updated_at", now).Where(where).Do(); err != nil {
		return fmt.Errorf("sqlite memory store: %s: %w", label, err)
	}
	return nil
}

// DeleteSession removes everything this store holds for one session: curated
// records are soft-deleted, mirroring Delete, and the raw event substrate is
// removed outright because it carries no status column.
func (s *Memory) DeleteSession(ctx context.Context, sessionID string) error {
	if err := s.check(); err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.markDeleted(ctx, sqx.Eq{"session_id": sessionID, "status": statusActive}, "delete session records"); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM raw_memory_events WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("sqlite memory store: delete session events: %w", err)
	}
	return nil
}

func (s *Memory) RecallFTS(ctx context.Context, req core.Query) ([]core.Hit, error) {
	if err := s.check(); err != nil {
		return nil, err
	}

	rows, err := s.queryFTSRows(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("sqlite memory store: recall fts: %w", err)
	}

	hits := make([]core.Hit, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, core.Hit{Record: recordFromRow(row), Score: -row.Rank})
	}
	return hits, nil
}

func (s *Memory) queryFTSRows(ctx context.Context, req core.Query) ([]recordRow, error) {
	ftsQuery := buildFTSQuery(req.Query)
	if ftsQuery == "" {
		return nil, nil
	}
	builder := sqx.Read[recordRow](ctx).WithQueryable(s.db).
		Select(append(recordColumns("r"), "bm25(memory_records_fts) AS rank")...).From("memory_records_fts").
		Join("memory_records r ON r.id = memory_records_fts.rowid").Where(sqx.Eq{"r.status": statusActive}).
		Where("memory_records_fts MATCH ?", ftsQuery).OrderBy("rank", "r.updated_at DESC", "r.id DESC").
		Limit(uint64(normalizedLimit(req.Limit)))
	return applyRecallFilters(builder, req).All()
}

func (s *Memory) SaveVector(ctx context.Context, recordID string, vector []float32) error {
	if err := s.check(); err != nil {
		return err
	}
	recordID = strings.TrimSpace(recordID)
	if recordID == "" {
		return fmt.Errorf("sqlite memory store: record id is required")
	}
	if len(vector) == 0 {
		return fmt.Errorf("sqlite memory store: vector is required")
	}
	blob, norm, err := encodeVector(vector)
	if err != nil {
		return err
	}
	now := core.UTCNow()
	s.mu.Lock()
	defer s.mu.Unlock()
	return upsertVector(ctx, s.db, recordID, len(vector), blob, norm, now)
}

func (s *Memory) RecallVector(ctx context.Context, vector []float32, req core.Query) ([]core.Hit, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	if len(vector) == 0 {
		return nil, nil
	}
	queryNorm := mathutil.Norm(vector)
	if queryNorm == 0 {
		return nil, nil
	}
	builder := sqx.Read[vectorRecordRow](ctx).WithQueryable(s.db).
		Select(append(recordColumns("r"), "v.vector_json AS vector_json", "v.vector_blob AS vector_blob",
			"v.vector_dims AS vector_dims", "v.vector_norm AS vector_norm")...).From("memory_vectors v").
		Join("memory_records r ON r.uuid = v.record_uuid").Where(sqx.Eq{"r.status": statusActive}).
		Where("(v.vector_dims = ? OR v.vector_blob IS NULL)", len(vector)).OrderBy("r.updated_at DESC", "r.id DESC").
		Limit(uint64(vectorCandidateLimit(req.Limit)))

	rows, err := applyRecallFilters(builder, req).All()
	if err != nil {
		return nil, fmt.Errorf("sqlite memory store: recall vector: %w", err)
	}
	hits := make([]core.Hit, 0, len(rows))
	for _, row := range rows {
		rec := recordFromRow(row.recordRow)
		candidate, candidateNorm, err := vectorFromRow(row)
		if err != nil {
			return nil, err
		}
		hits = append(hits, core.Hit{Record: rec, Score: mathutil.CosineWithNorms(vector, queryNorm, candidate, candidateNorm)})
	}
	core.SortHitsByScore(hits)
	limit := normalizedLimit(req.Limit)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func vectorCandidateLimit(resultLimit int) int {
	wanted := normalizedLimit(resultLimit) * vectorCandidateMultiplier
	return min(max(wanted, vectorCandidateFloor), vectorCandidateCap)
}

func (s *Memory) AppendMemoryEvent(ctx context.Context, ev core.MemoryEventRecord) (core.MemoryEventRecord, error) {
	if err := s.check(); err != nil {
		return core.MemoryEventRecord{}, err
	}
	ev.Role = strings.TrimSpace(ev.Role)
	ev.Content = strings.TrimSpace(ev.Content)
	if ev.Role == "" {
		return core.MemoryEventRecord{}, fmt.Errorf("sqlite memory store: event role is required")
	}
	if ev.Content == "" {
		return core.MemoryEventRecord{}, fmt.Errorf("sqlite memory store: event content is required")
	}
	if strings.TrimSpace(ev.ID) == "" {
		id, err := uuid.NewRandom()
		if err != nil {
			return core.MemoryEventRecord{}, fmt.Errorf("sqlite memory store: generate event uuid: %w", err)
		}
		ev.ID = id.String()
	}
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now().UTC()
	}
	var metadata any
	if len(ev.Metadata) > 0 {
		data, err := json.Marshal(ev.Metadata)
		if err != nil {
			return core.MemoryEventRecord{}, err
		}
		metadata = string(data)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := sqx.Write(ctx).WithQueryable(s.db).Insert("raw_memory_events").
		Columns("uuid", "session_id", "role", "content", "metadata_json", "created_at").
		Values(ev.ID, nullableString(ev.SessionID), ev.Role, ev.Content, metadata, ev.CreatedAt.UTC().Format(time.RFC3339Nano)).
		Do(); err != nil {
		return core.MemoryEventRecord{}, fmt.Errorf("sqlite memory store: append memory event: %w", err)
	}
	return ev, nil
}

func (s *Memory) RecentMemoryEvents(ctx context.Context, sessionID string, limit int) ([]core.MemoryEventRecord, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	query, args := sessionFilter(`SELECT uuid, session_id, role, content, metadata_json, created_at FROM raw_memory_events`, sessionID)
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY created_at DESC LIMIT ?`, append(args, normalizedLimit(limit))...)
	if err != nil {
		return nil, fmt.Errorf("sqlite memory store: recent memory events: %w", err)
	}
	events, err := collectRows(rows, scanMemoryEvent)
	if err != nil {
		return nil, err
	}
	slices.Reverse(events)
	return events, nil
}

func sessionFilter(query, sessionID string) (string, []any) {
	if sessionID = strings.TrimSpace(sessionID); sessionID == "" {
		return query, nil
	}
	return query + ` WHERE session_id = ?`, []any{sessionID}
}

// collectRows drains rows through scan and closes them.
func collectRows[T any](rows *sql.Rows, scan func(*sql.Rows) (T, error)) ([]T, error) {
	defer func() { _ = rows.Close() }()
	var out []T
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanMemoryEvent(rows *sql.Rows) (core.MemoryEventRecord, error) {
	var ev core.MemoryEventRecord
	var sessionID, metadata sql.NullString
	var created string
	if err := rows.Scan(&ev.ID, &sessionID, &ev.Role, &ev.Content, &metadata, &created); err != nil {
		return ev, err
	}
	ev.SessionID = sessionID.String
	if metadata.Valid && strings.TrimSpace(metadata.String) != "" {
		if err := json.Unmarshal([]byte(metadata.String), &ev.Metadata); err != nil {
			return ev, err
		}
	}
	if ts, err := time.Parse(time.RFC3339Nano, created); err == nil {
		ev.CreatedAt = ts
	}
	return ev, nil
}

func (s *Memory) CountMemoryEvents(ctx context.Context, sessionID string) (int, error) {
	if err := s.check(); err != nil {
		return 0, err
	}
	query, args := sessionFilter(`SELECT COUNT(*) FROM raw_memory_events`, sessionID)
	var count int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("sqlite memory store: count memory events: %w", err)
	}
	return count, nil
}

func (s *Memory) findActiveByHash(ctx context.Context, scope config.MemoryScope, kind core.Kind, hash string) (core.Record, error) {
	row, err := sqx.Read[recordRow](ctx).WithQueryable(s.db).Select(recordColumns("")...).From("memory_records").
		Where(sqx.Eq{"scope": string(scope), "kind": string(kind), "content_hash": hash, "status": statusActive}).
		OrderBy("id ASC").Limit(1).First()
	if err != nil {
		return core.Record{}, err
	}

	return recordFromRow(*row), nil
}

func (s *Memory) check() error { return CheckOpen(s.db, "sqlite memory store") }

func recordColumns(alias string) []string {
	return []string{qualify(alias, "uuid") + " AS uuid",
		qualify(alias, "scope") + " AS scope", qualify(alias, "kind") + " AS kind",
		qualify(alias, "content") + " AS content", "COALESCE(" + qualify(alias, "summary") + ", '') AS summary",
		"COALESCE(" + qualify(alias, "source") + ", '') AS source",
		"COALESCE(" + qualify(alias, "session_id") + ", '') AS session_id",
		"COALESCE(" + qualify(alias, "metadata_json") + ", '') AS metadata_json",
		qualify(alias, "created_at") + " AS created_at", qualify(alias, "updated_at") + " AS updated_at"}
}

func qualify(alias, column string) string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return column
	}
	return alias + "." + column
}

// applyRecallFilters narrows a recall query aliased "r". Scope, kind and
// metadata filter normally, but the session id is a preference, not an
// exclusion: user- and project-scoped records carry no session_id and must
// still surface for a session-defaulted recall (issues #299, #321).
func applyRecallFilters[T any](builder sqx.SelectBuilder[T], req core.Query) sqx.SelectBuilder[T] {
	builder = applyRecordFilters(builder, "r", req, "")
	if session := strings.TrimSpace(req.SessionID); session != "" {
		builder = builder.Where("(r.session_id IS NULL OR r.session_id = ?)", session)
	}
	return builder
}

// applyRecordFilters narrows a record query by scope, kind, metadata, and
// (optionally) session. sessionID is passed separately because recall treats it
// as a preference rather than an exclusion; List passes filter.SessionID.
func applyRecordFilters[T any](builder sqx.SelectBuilder[T], alias string, filter core.Query, sessionID string) sqx.SelectBuilder[T] {
	if len(filter.Scopes) > 0 {
		builder = builder.Where(sqx.Eq{qualify(alias, "scope"): stringValues(filter.Scopes)})
	}

	if len(filter.Kinds) > 0 {
		builder = builder.Where(sqx.Eq{qualify(alias, "kind"): stringValues(filter.Kinds)})
	} else {
		// Doc records mirror workspace files (retrieval tool, ADR 0015). They
		// only surface when a query names the kind explicitly, so default
		// recall, prompt blocks, and listings stay doc-free.
		builder = builder.Where(qualify(alias, "kind")+" != ?", string(core.KindDoc))
	}

	builder = applyMetadataFilters(builder, alias, filter.Metadata)

	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		builder = builder.Where(sqx.Eq{qualify(alias, "session_id"): sessionID})
	}

	return builder
}

func unsafeMetadataRune(r rune) bool {
	return r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
}

// applyMetadataFilters restricts results to records whose metadata_json holds
// every requested key/value pair. Keys are matched against a conservative
// identifier pattern and interpolated into the JSON path; an unsafe key is
// dropped rather than injected, and values always ride as bind parameters.
func applyMetadataFilters[T any](builder sqx.SelectBuilder[T], alias string, metadata map[string]string) sqx.SelectBuilder[T] {
	for _, key := range slices.Sorted(maps.Keys(metadata)) {
		if key == "" || strings.ContainsFunc(key, unsafeMetadataRune) {
			// Fail CLOSED. This filter is an isolation boundary (it is what keeps
			// one workspace's doc records out of another's results), so a key we
			// cannot address safely must match nothing rather than silently drop
			// the predicate and run the query unscoped.
			zap.L().Warn("memory metadata filter rejected an unsafe key; query restricted to no rows",
				zap.String("key", key))
			return builder.Where("1 = 0")
		}
		builder = builder.Where("json_extract("+qualify(alias, "metadata_json")+", '$."+key+"') = ?", metadata[key])
	}
	return builder
}

func stringValues[T ~string](items []T) []string {
	values := make([]string, 0, len(items))
	for _, item := range items {
		values = append(values, string(item))
	}
	return values
}

func nullableString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func normalizedLimit(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	return limit
}

func buildFTSQuery(query string) string {
	fields := strings.Fields(query)
	terms := make([]string, 0, len(fields))
	for _, field := range fields {
		// strings.Fields already dropped the whitespace, so a field is never
		// empty; only a term carrying FTS syntax has to be quoted.
		if strings.ContainsFunc(field, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
		}) {
			field = `"` + strings.ReplaceAll(field, `"`, `""`) + `"`
		}
		terms = append(terms, field)
	}
	return strings.Join(terms, " ")
}

func parseDBTime(value string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", value); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func encodeVector(vector []float32) ([]byte, float64, error) {
	if len(vector) == 0 {
		return nil, 0, fmt.Errorf("sqlite memory store: vector is required")
	}
	b := make([]byte, len(vector)*4)
	for i, v := range vector {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(v))
	}
	return b, mathutil.Norm(vector), nil
}

func decodeVector(data []byte) ([]float32, error) {
	if len(data)%4 != 0 {
		return nil, fmt.Errorf("sqlite memory store: vector blob byte length %d is not divisible by 4", len(data))
	}
	vector := make([]float32, len(data)/4)
	for i := range vector {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return vector, nil
}

func vectorFromRow(row vectorRecordRow) ([]float32, float64, error) {
	if len(row.VectorBlob) > 0 {
		vector, err := decodeVector(row.VectorBlob)
		if err != nil {
			return nil, 0, err
		}
		return vector, row.VectorNorm, nil
	}
	var vector []float32
	if err := json.Unmarshal([]byte(row.VectorJSON), &vector); err != nil {
		return nil, 0, err
	}
	return vector, mathutil.Norm(vector), nil
}
