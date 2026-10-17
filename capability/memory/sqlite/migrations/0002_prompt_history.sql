-- +goose Up
CREATE TABLE IF NOT EXISTS prompt_history (
    id INTEGER PRIMARY KEY,
    text TEXT NOT NULL,
    content_hash TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_prompt_history_updated_at
    ON prompt_history(updated_at, id);

-- +goose Down
DROP INDEX IF EXISTS idx_prompt_history_updated_at;
DROP TABLE IF EXISTS prompt_history;
