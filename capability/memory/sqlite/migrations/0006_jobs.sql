-- +goose Up
CREATE TABLE IF NOT EXISTS jobs (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    schedule        TEXT NOT NULL,
    prompt          TEXT NOT NULL,
    approval_policy TEXT NOT NULL DEFAULT 'deny',
    created_at      TEXT NOT NULL,
    last_run_at     TEXT,
    last_status     TEXT
);

-- +goose Down
DROP TABLE IF EXISTS jobs;
