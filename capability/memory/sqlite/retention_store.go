package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/invokr-agent/invokr/capability/memory/core"
	"github.com/invokr-agent/invokr/capability/memory/forgetting"
)

// statusArchived is the cold-storage state for forgotten runtime records. Like
// statusDeleted it is excluded from the live FTS index by the status triggers
// and from every recall path; unlike deleted rows it retains content and
// vectors and can be restored via the recall_archive tool.
const statusArchived = "archived"

// maxSQLiteParams is SQLite's default host-parameter limit (SQLITE_MAX_VARIABLE_NUMBER).
// IN (?) clauses over larger id sets must be batched to stay under it. We reserve
// a few slots per statement for the fixed leading parameters (status, timestamps).
const maxSQLiteParams = 999

// cleanIDs trims and drops empty ids, preserving order.
func cleanIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// placeholders builds a comma-separated run of "?" for an IN clause and the
// matching []any argument slice. Callers pass ids already cleaned via cleanIDs.
func placeholders(ids []string) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", "), args
}

// TouchRecall reinforces the given active records: bumps recall_count, resets
// last_recalled_at to now, and scales strength by factor (capped at
// maxStrength). A non-positive maxStrength disables the cap. Records not in the
// active state are untouched.
func (s *Memory) TouchRecall(ctx context.Context, ids []string, now time.Time, factor, maxStrength float64) error {
	if err := s.check(); err != nil {
		return err
	}
	cleaned := cleanIDs(ids)
	if len(cleaned) == 0 {
		return nil
	}
	if factor <= 0 {
		factor = 1.0
	}
	strengthCap := maxStrength
	if strengthCap <= 0 {
		strengthCap = 1e308
	}
	nowStr := now.UTC().Format(time.RFC3339Nano)
	// Reserve 4 leading params: last_recalled_at, factor, cap, status.
	return s.chunkedUpdate(ctx, "touch recall", `UPDATE memory_records
			 SET recall_count = recall_count + 1,
			     last_recalled_at = ?,
			     strength = MIN(strength * ?, ?)
			 WHERE status = ? AND uuid IN (%s)`,
		[]any{nowStr, factor, strengthCap, statusActive}, cleaned)
}

// chunkedUpdate runs one UPDATE per id chunk inside a single transaction,
// splitting the ids so each statement stays under SQLite's host-parameter
// limit. lead holds the fixed leading bind parameters and queryFmt takes the
// IN-clause placeholders; label names the operation in every error.
func (s *Memory) chunkedUpdate(ctx context.Context, label, queryFmt string, lead []any, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite memory store: %s: begin transaction: %w", label, err)
	}
	defer func() { _ = tx.Rollback() }()
	for chunk := range slices.Chunk(ids, maxSQLiteParams-len(lead)) {
		in, args := placeholders(chunk)
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(queryFmt, in), slices.Concat(lead, args)...); err != nil {
			return fmt.Errorf("sqlite memory store: %s: %w", label, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite memory store: %s: commit transaction: %w", label, err)
	}
	return nil
}

// SweepCandidates returns the retention projection of every active runtime
// record. Markdown and non-runtime records are pinned and never decay, so they
// are excluded here.
func (s *Memory) SweepCandidates(ctx context.Context) ([]forgetting.Record, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	query := `SELECT uuid, kind, strength, recall_count, last_recalled_at, updated_at
	          FROM memory_records
	          WHERE status = ? AND source = ?`
	rows, err := s.db.QueryContext(ctx, query, statusActive, string(core.SourceRuntime))
	if err != nil {
		return nil, fmt.Errorf("sqlite memory store: sweep candidates: %w", err)
	}
	return collectRows(rows, scanCandidate)
}

// QueryRetentionRecords returns the retention projection for only the given
// active runtime records, chunked to respect SQLite's host-parameter limit.
// Unlike SweepCandidates it does not scan every active row — the recall hot path
// passes just the recall hit IDs, avoiding an O(all-active-records) scan.
func (s *Memory) QueryRetentionRecords(ctx context.Context, ids []string) ([]forgetting.Record, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	cleaned := cleanIDs(ids)
	if len(cleaned) == 0 {
		return nil, nil
	}
	out := make([]forgetting.Record, 0, len(cleaned))
	// Reserve 2 leading params: status, source.
	for chunk := range slices.Chunk(cleaned, maxSQLiteParams-2) {
		in, args := placeholders(chunk)
		query := fmt.Sprintf(
			`SELECT uuid, kind, strength, recall_count, last_recalled_at, updated_at
			 FROM memory_records
			 WHERE status = ? AND source = ? AND uuid IN (%s)`, in)
		execArgs := append([]any{statusActive, string(core.SourceRuntime)}, args...)
		rows, err := s.db.QueryContext(ctx, query, execArgs...)
		if err != nil {
			return nil, fmt.Errorf("sqlite memory store: query retention records: %w", err)
		}
		recs, err := collectRows(rows, scanCandidate)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

func scanCandidate(rows *sql.Rows) (forgetting.Record, error) {
	var rec forgetting.Record
	var lastRecalled sql.NullString
	var updated string
	if err := rows.Scan(&rec.ID, &rec.Kind, &rec.Strength, &rec.RecallCount, &lastRecalled, &updated); err != nil {
		return rec, fmt.Errorf("sqlite memory store: scan candidate: %w", err)
	}
	if lastRecalled.Valid {
		rec.LastRecalledAt = parseDBTime(lastRecalled.String)
	}
	rec.UpdatedAt = parseDBTime(updated)
	return rec, nil
}

// Archive demotes the given active records to cold storage. updated_at records
// the archive time (used as the purge anchor). The status triggers drop the
// rows from the live FTS index automatically.
func (s *Memory) Archive(ctx context.Context, ids []string) error {
	return s.setStatus(ctx, ids, statusActive, statusArchived, false)
}

// Restore returns archived records to active with strength reset to 1.0 and a
// fresh last_recalled_at — a restored memory re-earns its persistence. The
// status triggers re-index the rows into FTS.
func (s *Memory) Restore(ctx context.Context, ids []string) error {
	return s.setStatus(ctx, ids, statusArchived, statusActive, true)
}

func (s *Memory) setStatus(ctx context.Context, ids []string, from, to string, resetStrength bool) error {
	if err := s.check(); err != nil {
		return err
	}
	cleaned := cleanIDs(ids)
	if len(cleaned) == 0 {
		return nil
	}
	// core.UTCNow already returns an RFC3339Nano string, keeping updated_at
	// lexicographically comparable with the timestamps PurgeArchivedBefore filters on.
	nowStr := core.UTCNow()
	set := "status = ?, updated_at = ?"
	lead := []any{to, nowStr}
	if resetStrength {
		set += ", strength = 1.0, last_recalled_at = ?"
		lead = append(lead, nowStr)
	}
	lead = append(lead, from)
	query := "UPDATE memory_records SET " + set + " WHERE status = ? AND uuid IN (%s)"
	return s.chunkedUpdate(ctx, "set status "+to, query, lead, cleaned)
}

// SearchArchive is the only reader of archived rows. It runs a lexical LIKE
// scan over archived content/summary — archived rows are excluded from the live
// FTS index, so this queries memory_records directly. At archive scale this is
// acceptable and keeps the live index clean. Doc records never appear here by
// construction, not by filter: archiving only touches source = 'runtime' rows
// and docs are written with SourceSQLite.
func (s *Memory) SearchArchive(ctx context.Context, query string, limit int) ([]core.Record, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	limit = normalizedLimit(limit)
	sqlQuery := `SELECT uuid, scope, kind, content, COALESCE(summary, ''), COALESCE(source, ''),
	                    COALESCE(session_id, ''), created_at, updated_at
	             FROM memory_records WHERE status = ?`
	args := []any{statusArchived}
	for _, term := range strings.Fields(query) {
		sqlQuery += " AND (content LIKE ? OR summary LIKE ?)"
		like := "%" + term + "%"
		args = append(args, like, like)
	}
	sqlQuery += " ORDER BY updated_at DESC, id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite memory store: search archive: %w", err)
	}
	return collectRows(rows, func(rows *sql.Rows) (core.Record, error) {
		var r recordRow
		if err := rows.Scan(&r.ID, &r.Scope, &r.Kind, &r.Content, &r.Summary, &r.Source,
			&r.SessionID, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return core.Record{}, fmt.Errorf("sqlite memory store: scan archive row: %w", err)
		}
		return recordFromRow(r), nil
	})
}

// PurgeArchivedBefore hard-deletes archived rows whose archive time (updated_at)
// predates cutoff, along with their vectors. Returns the number of records
// removed.
func (s *Memory) PurgeArchivedBefore(ctx context.Context, cutoff time.Time) (int, error) {
	if err := s.check(); err != nil {
		return 0, err
	}
	cut := cutoff.UTC().Format(time.RFC3339Nano)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM memory_vectors WHERE record_uuid IN (
		    SELECT uuid FROM memory_records WHERE status = ? AND updated_at < ?)`,
		statusArchived, cut); err != nil {
		return 0, fmt.Errorf("sqlite memory store: purge archived vectors: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM memory_records WHERE status = ? AND updated_at < ?`, statusArchived, cut)
	if err != nil {
		return 0, fmt.Errorf("sqlite memory store: purge archived records: %w", err)
	}
	affected, _ := res.RowsAffected()
	return int(affected), nil
}

// PruneMemoryEvents deletes raw memory events older than cutoff. The
// pre-threshold recall fallback only needs recent turns, so old summarized
// events are safe to drop.
func (s *Memory) PruneMemoryEvents(ctx context.Context, cutoff time.Time) (int, error) {
	if err := s.check(); err != nil {
		return 0, err
	}
	cut := cutoff.UTC().Format(time.RFC3339Nano)
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM raw_memory_events WHERE created_at < ?`, cut)
	if err != nil {
		return 0, fmt.Errorf("sqlite memory store: prune memory events: %w", err)
	}
	affected, _ := res.RowsAffected()
	return int(affected), nil
}
