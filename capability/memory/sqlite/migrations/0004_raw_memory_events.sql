-- +goose Up
CREATE TABLE IF NOT EXISTS raw_memory_events (
    uuid TEXT PRIMARY KEY,
    session_id TEXT,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    metadata_json TEXT,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_raw_memory_events_session_created
    ON raw_memory_events(session_id, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_raw_memory_events_session_created;
DROP TABLE IF EXISTS raw_memory_events;
