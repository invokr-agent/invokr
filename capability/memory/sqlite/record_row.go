package sqlite

import (
	"encoding/json"
	"strings"

	"github.com/invokr-agent/invokr/capability/memory/core"
	"github.com/invokr-agent/invokr/config"
)

type recordRow struct {
	ID        string  `db:"uuid"`
	Scope     string  `db:"scope"`
	Kind      string  `db:"kind"`
	Content   string  `db:"content"`
	Summary   string  `db:"summary"`
	Source    string  `db:"source"`
	SessionID string  `db:"session_id"`
	Metadata  string  `db:"metadata_json"`
	CreatedAt string  `db:"created_at"`
	UpdatedAt string  `db:"updated_at"`
	Rank      float64 `db:"rank"`
}

type vectorRecordRow struct {
	recordRow
	VectorJSON string  `db:"vector_json"`
	VectorBlob []byte  `db:"vector_blob"`
	VectorDims int     `db:"vector_dims"`
	VectorNorm float64 `db:"vector_norm"`
}

func recordFromRow(row recordRow) core.Record {
	return core.Record{
		ID: row.ID, Scope: config.MemoryScope(row.Scope), Kind: core.Kind(row.Kind),
		Content: row.Content, Summary: row.Summary, Source: core.Source(row.Source),
		SessionID: row.SessionID, Metadata: metadataFromJSON(row.Metadata),
		CreatedAt: parseDBTime(row.CreatedAt), UpdatedAt: parseDBTime(row.UpdatedAt)}
}

// metadataFromJSON decodes a metadata_json column value; malformed or empty
// payloads yield nil rather than failing the whole row.
func metadataFromJSON(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var meta map[string]string
	if err := json.Unmarshal([]byte(raw), &meta); err != nil || len(meta) == 0 {
		return nil
	}
	return meta
}

// metadataToJSON encodes record metadata for the metadata_json column; empty
// metadata stores NULL.
func metadataToJSON(meta map[string]string) any {
	if len(meta) == 0 {
		return nil
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return nil
	}
	return string(data)
}

func rowsToRecords(rows []recordRow) []core.Record {
	records := make([]core.Record, 0, len(rows))
	for _, row := range rows {
		records = append(records, recordFromRow(row))
	}
	return records
}
