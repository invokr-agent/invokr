package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	sqliteinternal "github.com/invokr-ai/invokr/capability/memory/sqlite"
	"github.com/invokr-ai/invokr/config"
)

// OpenStore opens (creating if needed) the shared memory.db and returns a
// Store backed by it. Caller owns closing the returned *sql.DB.
func OpenStore(app config.AppConfig) (*sql.DB, *Store, error) {
	dbPath := filepath.Join(app.Memory.Dir, "memory.db")
	db, err := sqliteinternal.InitDbAtPath(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("jobs: open db: %w", err)
	}
	return db, NewStore(db), nil
}

// ErrDuplicateName is returned when a job with the same name already exists.
var ErrDuplicateName = errors.New("duplicate job name")

// Job represents a scheduled job record.
type Job struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Schedule       string     `json:"schedule"`
	Prompt         string     `json:"prompt"`
	ApprovalPolicy string     `json:"approval_policy"`
	CreatedAt      time.Time  `json:"created_at"`
	LastRunAt      *time.Time `json:"last_run_at"`
	LastStatus     *string    `json:"last_status"`
}

// Store provides persistence for scheduled jobs.
type Store struct {
	db *sql.DB
}

// NewStore returns a Store backed by db.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// scanner abstracts *sql.Row and *sql.Rows for scanJob.
type scanner interface {
	Scan(dest ...any) error
}

func scanJob(s scanner) (Job, error) {
	var j Job
	var createdAt string
	var lastRunAt, lastStatus sql.NullString
	if err := s.Scan(&j.ID, &j.Name, &j.Schedule, &j.Prompt, &j.ApprovalPolicy,
		&createdAt, &lastRunAt, &lastStatus); err != nil {
		return Job{}, fmt.Errorf("scan job: %w", err)
	}
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return Job{}, fmt.Errorf("parse created_at: %w", err)
	}
	j.CreatedAt = t
	if lastRunAt.Valid {
		rt, err := time.Parse(time.RFC3339, lastRunAt.String)
		if err != nil {
			return Job{}, fmt.Errorf("parse last_run_at: %w", err)
		}
		j.LastRunAt = &rt
	}
	if lastStatus.Valid {
		statusStr := lastStatus.String
		j.LastStatus = &statusStr
	}
	return j, nil
}

// Add inserts a new job and returns it. Returns ErrDuplicateName if name is taken.
func (s *Store) Add(ctx context.Context, name, schedule, prompt, approvalPolicy string) (Job, error) {
	id := uuid.NewString()
	createdAt := time.Now().UTC()
	const q = `INSERT INTO jobs (id, name, schedule, prompt, approval_policy, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, q, id, name, schedule, prompt, approvalPolicy, createdAt.Format(time.RFC3339))
	if err != nil {
		if sqliteinternal.IsUniqueConstraint(err) {
			return Job{}, fmt.Errorf("job %q already exists: %w", name, ErrDuplicateName)
		}
		return Job{}, fmt.Errorf("add job: %w", err)
	}

	return Job{ID: id, Name: name, Schedule: schedule, Prompt: prompt,
		ApprovalPolicy: approvalPolicy, CreatedAt: createdAt}, nil
}

// List returns all jobs ordered by created_at ascending.
func (s *Store) List(ctx context.Context) ([]Job, error) {
	const q = `SELECT id, name, schedule, prompt, approval_policy, created_at, last_run_at, last_status
		FROM jobs ORDER BY created_at ASC`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var jobs []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list jobs rows: %w", err)
	}
	return jobs, nil
}

// Get returns the job with the given ID, or sql.ErrNoRows if absent.
func (s *Store) Get(ctx context.Context, id string) (Job, error) { return s.getBy(ctx, "id", id) }

// GetByName returns the job with the given name, or sql.ErrNoRows if absent.
func (s *Store) GetByName(ctx context.Context, name string) (Job, error) {
	return s.getBy(ctx, "name", name)
}

// getBy fetches the single job matching column = value. column is a package
// literal, never user input, and also names the lookup in the error.
func (s *Store) getBy(ctx context.Context, column, value string) (Job, error) {
	const q = `SELECT id, name, schedule, prompt, approval_policy, created_at, last_run_at, last_status
		FROM jobs WHERE %s = ?`

	j, err := scanJob(s.db.QueryRowContext(ctx, fmt.Sprintf(q, column), value))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, sql.ErrNoRows
		}
		return Job{}, fmt.Errorf("get job by %s: %w", column, err)
	}
	return j, nil
}

// UpdateDenyJob changes schedule and prompt only while the job remains deny-only.
// This prevents a concurrent policy change from turning a tool-approved edit into
// an edit of a privileged job.
func (s *Store) UpdateDenyJob(ctx context.Context, id, schedule, prompt string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE jobs SET schedule = ?, prompt = ? WHERE id = ? AND approval_policy = 'deny'`, schedule, prompt, id)
	if err != nil {
		return fmt.Errorf("update job: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update job rows affected: %w", err)
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Delete removes a job by id. Idempotent: returns nil if the row is absent.
func (s *Store) Delete(ctx context.Context, id string) error {
	return s.exec(ctx, "delete job", `DELETE FROM jobs WHERE id = ?`, id)
}

// DeleteAll removes all jobs.
func (s *Store) DeleteAll(ctx context.Context) error {
	return s.exec(ctx, "delete all jobs", `DELETE FROM jobs`)
}

// RecordRun updates last_run_at and last_status for a job. Idempotent if the row is gone.
func (s *Store) RecordRun(ctx context.Context, id, status string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return s.exec(ctx, "record run", `UPDATE jobs SET last_run_at = ?, last_status = ? WHERE id = ?`, now, status, id)
}

// exec runs a statement that returns no rows, wrapping any failure as
// "<label>: <err>".
func (s *Store) exec(ctx context.Context, label, query string, args ...any) error {
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

// cronFieldPattern matches the character set cron fields may use: digits,
// '*' (any), ',' (list), '/' (step) and '-' (range).
var cronFieldPattern = regexp.MustCompile(`^[0-9*,/-]+$`)

// cronFieldRanges gives the inclusive numeric bounds for each of the 5
// standard cron fields, in order: minute, hour, day-of-month, month,
// day-of-week.
var cronFieldRanges = [5][2]int{{0, 59}, // minute
	{0, 23}, // hour
	{1, 31}, // day-of-month
	{1, 12}, // month
	{0, 7},  // day-of-week (0 and 7 both mean Sunday)
}

// SanitizeSchedule validates a cron expression structurally: exactly 5
// fields, each restricted to the cron character set, with any numeric
// tokens sanity-checked against that field's valid range.
func SanitizeSchedule(expr string) error {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return errors.New("schedule must not be empty")
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fmt.Errorf("schedule %q must have exactly 5 fields", expr)
	}
	for i, field := range fields {
		if !cronFieldPattern.MatchString(field) {
			return fmt.Errorf("schedule %q: field %d (%q) contains invalid characters", expr, i+1, field)
		}
		if err := validateCronFieldRange(field, cronFieldRanges[i]); err != nil {
			return fmt.Errorf("schedule %q: field %d: %w", expr, i+1, err)
		}
	}
	return nil
}

// validateCronFieldRange checks that every numeric token in a cron field
// (list entries, step values, and range bounds) falls within [min, max].
func validateCronFieldRange(field string, bounds [2]int) error {
	for _, item := range strings.Split(field, ",") {
		base, step, hasStep := strings.Cut(item, "/")
		if hasStep {
			if err := validateCronNumber(step, bounds); err != nil {
				return err
			}
		}
		lo, hi, hasRange := strings.Cut(base, "-")
		if base == "*" {
			continue
		}
		if err := validateCronNumber(lo, bounds); err != nil {
			return err
		}
		if hasRange {
			if err := validateCronNumber(hi, bounds); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateCronNumber parses a single numeric token and checks it against bounds.
func validateCronNumber(tok string, bounds [2]int) error {
	if tok == "" || tok == "*" {
		return nil
	}
	n, err := strconv.Atoi(tok)
	if err != nil {
		return fmt.Errorf("%q is not a number", tok)
	}
	if n < bounds[0] || n > bounds[1] {
		return fmt.Errorf("%d out of range [%d-%d]", n, bounds[0], bounds[1])
	}
	return nil
}
