package markdown

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"slices"
	"strings"
)

// entryIDPrefix marks ids minted by EntryID so a caller can tell a markdown
// record id from a sqlite uuid.
const entryIDPrefix = "md-"

// EntryID is a markdown record's stable id: a digest of its normalized text.
// Records used to be keyed by their content, which made an id indistinguishable
// from the record body and broke delete-by-id (issue #299).
func EntryID(content string) string {
	sum := sha256.Sum256([]byte(normalizeMemoryEntry(content)))
	return entryIDPrefix + hex.EncodeToString(sum[:])[:16]
}

const memoryEntrySeparator = "\n"

const (
	MemoryKindProject MemoryKind = "project"
	MemoryKindUser    MemoryKind = "user"
	MemoryKindAgents  MemoryKind = "agents"
)

type MemoryKind string

type Document struct {
	kind        MemoryKind
	title, path string
	limit       int
	file        *atomicFile
}

func newMarkdownMemory(kind MemoryKind, title, path string, limit int) *Document {
	return &Document{kind: kind, title: title, path: path, limit: limit, file: &atomicFile{path: path, fileMod: 0o600, dirMod: 0o700}}
}

func (m *Document) Add(ctx context.Context, content string) error {
	content = normalizeMemoryEntry(content)
	if content == "" {
		return nil
	}

	return m.mutateEntries(ctx, func(entries []string) ([]string, bool, error) {
		if slices.Contains(entries, content) || containsNearDuplicate(entries, content) {
			return entries, false, nil
		}
		return append(entries, content), true, nil
	})
}

// ReplaceByID replaces the entry addressed by its stable id, reporting whether
// one matched.
func (m *Document) ReplaceByID(ctx context.Context, id, newContent string) (bool, error) {
	newContent = normalizeMemoryEntry(newContent)
	if newContent == "" {
		return false, nil
	}
	return m.mutateByID(ctx, id, func(entries []string, i int) []string {
		entries[i] = newContent
		return entries
	})
}

// RemoveByID removes the entry addressed by its stable id, reporting whether
// one matched.
func (m *Document) RemoveByID(ctx context.Context, id string) (bool, error) {
	return m.mutateByID(ctx, id, func(entries []string, i int) []string {
		return append(entries[:i], entries[i+1:]...)
	})
}

// mutateByID applies mutate to the entry addressed by id — its stable EntryID
// or, for ids minted before EntryID existed, its literal text.
func (m *Document) mutateByID(ctx context.Context, id string, mutate func([]string, int) []string) (bool, error) {
	if strings.TrimSpace(id) == "" {
		return false, nil
	}
	matched := false
	err := m.mutateEntries(ctx, func(entries []string) ([]string, bool, error) {
		index := indexOfEntry(entries, id)
		if index < 0 {
			return entries, false, nil
		}
		matched = true
		return mutate(entries, index), true, nil
	})
	return matched, err
}

// indexOfEntry resolves an id to an entry position, accepting either the stable
// EntryID or the entry text itself.
func indexOfEntry(entries []string, id string) int {
	id = strings.TrimSpace(id)
	normalized := normalizeMemoryEntry(id)
	for i, entry := range entries {
		if EntryID(entry) == id || normalizeMemoryEntry(entry) == normalized {
			return i
		}
	}
	return -1
}

// List is intentionally lock-free for v1. It is weakly consistent and may
// observe any last committed file state while writers use atomic replacement.
func (m *Document) List(_ context.Context) ([]string, error) {
	b, err := m.file.Read()
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	return parseMemoryEntries(string(b)), nil
}

func (m *Document) renderSection(ctx context.Context) (string, error) {
	entries, err := m.List(ctx)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", nil
	}

	body := serializeMemoryEntries(entries)
	used := len(body)

	percent := 0
	if m.limit > 0 {
		percent = used * 100 / m.limit
	}

	return fmt.Sprintf("%s [%d%% - %d/%d chars]\n%s", m.title, percent, used, m.limit, body), nil
}

func (m *Document) mutateEntries(ctx context.Context, mutate func([]string) ([]string, bool, error)) (err error) {
	return m.file.WithLock(ctx, func(current []byte) ([]byte, bool, error) {
		entries := parseMemoryEntries(string(current))
		entries, changed, err := mutate(entries)
		if err != nil || !changed {
			return nil, false, err
		}

		content := serializeMemoryEntries(entries)
		if len(content) > m.limit {
			return nil, false, fmt.Errorf("%s memory would exceed %d chars", m.kind, m.limit)
		}

		if content != "" {
			content += "\n"
		}

		return []byte(content), true, nil
	})
}

func parseMemoryEntries(content string) []string {
	split := strings.Split(content, memoryEntrySeparator)
	out := make([]string, 0, len(split))
	for _, entry := range split {
		entry = normalizeMemoryEntry(strings.TrimPrefix(strings.TrimSpace(entry), "- "))
		if entry == "" {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func containsNearDuplicate(entries []string, want string) bool {
	wantWords := memoryWordSet(want)
	if len(wantWords) < 4 {
		return false
	}
	for _, entry := range entries {
		words := memoryWordSet(entry)
		intersection := 0
		for word := range wantWords {
			if _, ok := words[word]; ok {
				intersection++
			}
		}
		union := len(wantWords) + len(words) - intersection
		if union > 0 && float64(intersection)/float64(union) >= 0.8 {
			return true
		}
	}
	return false
}

func memoryWordSet(value string) map[string]struct{} {
	words := map[string]struct{}{}
	for _, word := range strings.Fields(strings.ToLower(normalizeMemoryEntry(value))) {
		word = strings.Trim(word, ",:;()[]{}\"'")
		if word == "" || slices.Contains([]string{"a", "an", "the", "this"}, word) {
			continue
		}
		words[word] = struct{}{}
	}
	return words
}

func normalizeMemoryEntry(entry string) string { return strings.Join(strings.Fields(entry), " ") }

func serializeMemoryEntries(entries []string) string {
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		entry = normalizeMemoryEntry(entry)
		if entry != "" {
			lines = append(lines, "- "+entry)
		}
	}
	return strings.Join(lines, "\n")
}
