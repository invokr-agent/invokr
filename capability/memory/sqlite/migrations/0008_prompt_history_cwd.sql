-- +goose Up
ALTER TABLE prompt_history ADD COLUMN cwd TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_prompt_history_cwd_updated_at
    ON prompt_history(cwd, updated_at, id);

-- +goose Down
DROP INDEX IF EXISTS idx_prompt_history_cwd_updated_at;
ALTER TABLE prompt_history DROP COLUMN cwd;
