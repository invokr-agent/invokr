-- +goose Up
ALTER TABLE memory_vectors ADD COLUMN vector_blob BLOB;
ALTER TABLE memory_vectors ADD COLUMN vector_dims INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memory_vectors ADD COLUMN vector_norm REAL NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_memory_vectors_dims
    ON memory_vectors(vector_dims);

-- +goose Down
DROP INDEX IF EXISTS idx_memory_vectors_dims;

ALTER TABLE memory_vectors DROP COLUMN vector_norm;
ALTER TABLE memory_vectors DROP COLUMN vector_dims;
ALTER TABLE memory_vectors DROP COLUMN vector_blob;
