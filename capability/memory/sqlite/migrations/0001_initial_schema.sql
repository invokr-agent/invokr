-- +goose Up
CREATE TABLE IF NOT EXISTS memory_records (
                                              id INTEGER PRIMARY KEY,
                                              uuid TEXT NOT NULL UNIQUE,
                                              scope TEXT NOT NULL,
                                              kind TEXT NOT NULL,
                                              content TEXT NOT NULL,
                                              summary TEXT,
                                              source TEXT NOT NULL,
                                              session_id TEXT,
                                              status TEXT NOT NULL DEFAULT 'active',
                                              content_hash TEXT NOT NULL,
                                              metadata_json TEXT,
                                              created_at TEXT NOT NULL,
                                              updated_at TEXT NOT NULL,
                                              deleted_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_memory_records_scope_kind
    ON memory_records(scope, kind, status, updated_at);

CREATE INDEX IF NOT EXISTS idx_memory_records_session
    ON memory_records(session_id, status, updated_at);

CREATE UNIQUE INDEX IF NOT EXISTS idx_memory_records_dedupe
    ON memory_records(scope, kind, content_hash)
    WHERE status = 'active';

CREATE VIRTUAL TABLE IF NOT EXISTS memory_records_fts USING fts5(
  content,
  summary,
  tokenize = 'porter unicode61'
);

-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS memory_records_ai
AFTER INSERT ON memory_records
WHEN new.status = 'active'
BEGIN
INSERT INTO memory_records_fts(rowid, content, summary)
VALUES (new.id, new.content, COALESCE(new.summary, ''));
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS memory_records_ad
AFTER DELETE ON memory_records
BEGIN
DELETE FROM memory_records_fts WHERE rowid = old.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS memory_records_au
AFTER UPDATE ON memory_records
WHEN old.status = 'active' OR new.status = 'active'
BEGIN
DELETE FROM memory_records_fts WHERE rowid = old.id;
INSERT INTO memory_records_fts(rowid, content, summary)
SELECT new.id, new.content, COALESCE(new.summary, '')
WHERE new.status = 'active';
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS memory_records_au;
DROP TRIGGER IF EXISTS memory_records_ad;
DROP TRIGGER IF EXISTS memory_records_ai;

DROP TABLE IF EXISTS memory_records_fts;

DROP INDEX IF EXISTS idx_memory_records_dedupe;
DROP INDEX IF EXISTS idx_memory_records_session;
DROP INDEX IF EXISTS idx_memory_records_scope_kind;

DROP TABLE IF EXISTS memory_records;
