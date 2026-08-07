-- +goose Up
CREATE TABLE IF NOT EXISTS memory_vectors (
    record_uuid TEXT PRIMARY KEY,
    vector_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY(record_uuid) REFERENCES memory_records(uuid)
);

-- +goose Down
DROP TABLE IF EXISTS memory_vectors;
