-- +goose Up
-- Forgetting extension: retention scoring columns on runtime records. These are
-- inert until memory.forgetting.enabled is set; existing rows default to
-- strength 1.0 / recall_count 0 / NULL last_recalled_at (falls back to
-- updated_at in the decay math). The 'archived' status reuses the existing FTS
-- status triggers (they only index status = 'active'), so archived rows drop out
-- of the live index exactly like soft-deleted rows and need no trigger change.
ALTER TABLE memory_records ADD COLUMN last_recalled_at TEXT;
ALTER TABLE memory_records ADD COLUMN recall_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memory_records ADD COLUMN strength REAL NOT NULL DEFAULT 1.0;

CREATE INDEX IF NOT EXISTS idx_memory_records_status_recalled
    ON memory_records(status, last_recalled_at);

-- +goose Down
DROP INDEX IF EXISTS idx_memory_records_status_recalled;
ALTER TABLE memory_records DROP COLUMN strength;
ALTER TABLE memory_records DROP COLUMN recall_count;
ALTER TABLE memory_records DROP COLUMN last_recalled_at;
