package retrieval

// Doc discovery and synchronization into the memory store (ADR 0015). The
// former on-disk JSON index, staleness ledger, and embedding cache are gone:
// each doc is one memory record of kind "doc", staleness is decided per sync
// from the file metadata mirrored into the record's metadata, and vectors ride
// the memory manager's existing embedding path.

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/invokr-ai/invokr/capability/approval"
	memorycore "github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/support/strutil"
	"github.com/invokr-ai/invokr/tools/core"

	"go.uber.org/zap"
)

const (
	lazyMaxFiles              = 100
	lazyMaxSourceBytes        = 5 << 20
	maxIndexedFileBytes int64 = 5 << 20
	docListLimit              = 10000

	// docMetaPath and docMetaWorkspace are the store's shared identity keys:
	// the sqlite store folds both into a doc record's dedupe identity, and
	// every query this tool issues filters on the workspace so one user-global
	// memory.db can serve many repositories without cross-talk.
	docMetaPath      = memorycore.MetadataKeyPath
	docMetaWorkspace = memorycore.MetadataKeyWorkspace
	docMetaHash      = "source_hash"
	docMetaSize      = "file_size"
	docMetaModTime   = "file_mod_time"
	docMetaLines     = "line_count"
)

type candidateFile struct {
	absPath, relPath, modTime string
	size                      int64
}

type sourceDoc struct {
	pathOrID, text, fileModTime string
	fileSize                    int64
}

type BuildOptions struct {
	AllowLarge bool
}

type BuildResult struct {
	SourceCount, StaleCount int
	IndexPath               string
	Changed                 bool
}

type syncOptions struct {
	// approveInitial gates the very first doc sync (no doc records yet) behind
	// an approval request — the lazy in-search path. The explicit BuildIndex
	// path instead gates large builds unless AllowLarge is set.
	approveInitial bool
	allowLarge     bool
}

type syncState struct {
	sourceCount int
	stale       map[string]bool
	changed     bool
}

// BuildIndex re-syncs every workspace doc into the memory store. It backs the
// `invokr index` command; large workspaces require AllowLarge or approval.
func (s *Service) BuildIndex(ctx context.Context, opts BuildOptions) (BuildResult, error) {
	started := s.now()
	sync, err := s.syncDocs(ctx, syncOptions{allowLarge: opts.AllowLarge})
	result := BuildResult{SourceCount: sync.sourceCount, StaleCount: len(sync.stale), IndexPath: s.storePath(), Changed: sync.changed}
	s.dispatchAudit(ctx, ActionIndex, Args{}, result.SourceCount, 0, result.StaleCount, s.now().Sub(started), core.Outcome(err), err)
	return result, err
}

func (s *Service) syncDocs(ctx context.Context, opts syncOptions) (syncState, error) {
	st := syncState{stale: map[string]bool{}}
	if s.memory == nil {
		return st, nil
	}
	files, totalBytes, err := s.discoverProjectDocs()
	if err != nil {
		return st, err
	}
	st.sourceCount = len(files)
	// Scoped to this workspace: another repository's doc records must not be
	// visible here, and must never be pruned or overwritten by this sync.
	existing, err := s.listDocRecords(ctx)
	if err != nil {
		return st, err
	}
	// Known, accepted: two instances doing a concurrent *first* sync of this
	// workspace both see initial == true, so both prompt and both embed the
	// full corpus. The rows dedupe; the embedding cost does not.
	initial := len(existing) == 0
	full, err := s.approveSync(ctx, opts, initial, len(files), totalBytes)
	if err != nil {
		return st, err
	}
	return s.applyDocChanges(ctx, files, existing, full)
}

// approveSync gates the write and reports whether the caller may read the whole
// corpus. An explicit `invokr index` always reads everything (that is what the
// command is for, and an approval granted for a large build must not silently
// yield a partial one); the lazy in-search path reads everything only on the
// first index for this workspace, and stays inside the incremental byte budget
// afterwards.
func (s *Service) approveSync(ctx context.Context, opts syncOptions, initial bool, fileCount int, totalBytes int64) (bool, error) {
	if !opts.approveInitial {
		if !opts.allowLarge && (fileCount > lazyMaxFiles || totalBytes > lazyMaxSourceBytes) {
			if err := s.approveIndexWrite(ctx, "large workspace retrieval index build",
				fmt.Sprintf("Build retrieval index for %d sources", fileCount),
				fmt.Sprintf("Doc store: %s\nWorkspace: %s\nProject files: %d\nProject source bytes: %d",
					filepath.ToSlash(s.storePath()), s.workspaceKey(), fileCount, totalBytes)); err != nil {
				return false, err
			}
		}
		return true, nil
	}
	if initial && fileCount > 0 {
		if err := s.approveIndexWrite(ctx,
			"retrieval index creation writes doc records to memory", "Create retrieval doc index",
			"Doc store: "+filepath.ToSlash(s.storePath())+"\nWorkspace: "+s.workspaceKey()); err != nil {
			return false, err
		}
	}
	return initial, nil
}

func (s *Service) approveIndexWrite(ctx context.Context, reason, summary, details string) error {
	return core.EnforceApproval(ctx, s.approver, core.ToolSpec{
		Name:     ToolName,
		Metadata: core.NativeMetadata("", []string{IndexWritePolicyTier}, constants.ApprovalPolicyAsk, core.ToolCapabilities{}),
	}, core.Ask(approval.Request{Tool: ToolName, Reason: reason, Tier: IndexWritePolicyTier, Summary: summary, Details: details}))
}

// applyDocChanges walks the discovered files against this workspace's stored
// doc records, saving new docs, updating changed ones, deleting records whose
// files are gone, and marking unreadable-but-known docs stale. Incremental
// syncs cap re-read bytes so one search never re-ingests a large corpus;
// `existing` is already workspace-scoped, so the prune loop can only ever
// delete records belonging to this workspace.
func (s *Service) applyDocChanges(ctx context.Context, files []candidateFile, existing map[string]memorycore.Record, unlimited bool) (syncState, error) {
	st := syncState{sourceCount: len(files), stale: map[string]bool{}}
	budget := int64(lazyMaxSourceBytes)
	if unlimited {
		budget = math.MaxInt64
	}
	seen := make(map[string]bool, len(files))
	for _, file := range files {
		seen[file.relPath] = true
		changed, stale, spent := s.syncDocFile(ctx, file, existing, budget)
		budget -= spent
		if stale {
			st.stale[file.relPath] = true
		}
		if changed {
			st.changed = true
		}
	}
	for path, rec := range existing {
		if seen[path] {
			continue
		}
		if err := s.memory.Delete(ctx, rec.ID); err != nil {
			return st, fmt.Errorf("delete doc record for %q: %w", path, err)
		}
		st.changed = true
	}
	return st, nil
}

// syncDocFile reconciles one file with its stored record. Per-file failures
// (oversized, unreadable, store write conflicts) never fail the sync; a known
// doc that could not be refreshed is reported stale instead.
func (s *Service) syncDocFile(ctx context.Context, file candidateFile, existing map[string]memorycore.Record, budget int64) (changed bool, stale bool, spent int64) {
	old, ok := existing[file.relPath]
	if file.size > maxIndexedFileBytes || file.size > budget {
		return false, ok && !docMetadataMatches(old, file), 0
	}
	if ok && docMetadataMatches(old, file) {
		return false, false, 0
	}
	doc, err := s.readProjectDoc(file)
	if err != nil {
		return false, ok, file.size
	}
	rec := s.docRecord(doc)
	if rec.Content == "" {
		return false, ok, file.size
	}
	changed, stale = s.writeDocRecord(ctx, rec, old, ok)
	return changed, stale, file.size
}

// writeDocRecord persists one reconciled doc. A touched-but-unchanged file
// (checkout, formatter pass, mtime bump) must not cost a re-embed, so the
// stored content hash gates the update: only real content changes reach the
// store. A failed update leaves the stale record in place and says so; a failed
// insert simply leaves the doc unindexed for this pass.
func (s *Service) writeDocRecord(ctx context.Context, rec memorycore.Record, old memorycore.Record, exists bool) (changed bool, stale bool) {
	if !exists {
		_, err := s.memory.Save(ctx, rec)
		return err == nil, false
	}
	if rec.Metadata[docMetaHash] == old.Metadata[docMetaHash] {
		// Content is unchanged, but the size/mtime probe that let us skip this
		// file has drifted (a checkout or formatter rewrote it byte-identically).
		// Refresh the probe alone — a metadata-only write, so no re-hash and no
		// re-embed — otherwise this file is re-read and re-hashed on every later
		// sync, charging the incremental byte budget forever and crowding out
		// genuinely changed files.
		if err := s.memory.UpdateMetadata(ctx, old.ID, rec.Metadata); err != nil {
			return false, true
		}
		return false, false
	}
	if _, err := s.memory.Update(ctx, old.ID, rec); err != nil {
		return false, true
	}
	return true, false
}

// workspaceKey is the discriminator stored on every doc record and filtered on
// by every query this tool issues. memory.db is user-global (default
// ~/.invokr), so without it one repository would see, prune, and overwrite
// another's doc records.
// Known, accepted: the key is the resolved root as given, so launching from a
// subdirectory or through a symlink yields a different key and therefore a
// second, duplicate doc set for the same repository.
func (s *Service) workspaceKey() string {
	return filepath.ToSlash(s.workspace.Root)
}

func (s *Service) docQuery() memorycore.Query {
	return memorycore.Query{
		Scopes: []config.MemoryScope{config.ScopeProject}, Kinds: []memorycore.Kind{memorycore.KindDoc},
		Metadata: map[string]string{docMetaWorkspace: s.workspaceKey()},
	}
}

func (s *Service) listDocRecords(ctx context.Context) (map[string]memorycore.Record, error) {
	query := s.docQuery()
	query.Limit = docListLimit
	records, err := s.memory.List(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list doc records: %w", err)
	}
	if len(records) >= docListLimit {
		// A workspace with more docs than the cap syncs only the first page:
		// the rest re-save each pass and never prune. Loud enough to diagnose,
		// not fatal — search still works on what is indexed.
		zap.L().Warn("retrieval doc record listing hit its cap; docs beyond it are not tracked",
			zap.Int("limit", docListLimit), zap.String("workspace", s.workspaceKey()))
	}
	out := make(map[string]memorycore.Record, len(records))
	for _, rec := range records {
		if path := rec.Metadata[docMetaPath]; path != "" {
			out[path] = rec
		}
	}
	return out, nil
}

// docRecord builds the record for one doc. Content is trimmed here rather than
// left to the store's normalizeRecord, so the hash and line count describe
// exactly the text the record holds and the hit exposes.
func (s *Service) docRecord(doc sourceDoc) memorycore.Record {
	content := strings.TrimSpace(doc.text)
	return memorycore.Record{
		Scope: config.ScopeProject, Kind: memorycore.KindDoc, Content: content, Source: memorycore.SourceSQLite,
		Metadata: map[string]string{
			docMetaWorkspace: s.workspaceKey(), docMetaPath: doc.pathOrID,
			docMetaHash:    "sha256:" + strutil.HashHex(content),
			docMetaSize:    strconv.FormatInt(doc.fileSize, 10),
			docMetaModTime: doc.fileModTime,
			docMetaLines:   strconv.Itoa(countLines([]byte(content))),
		},
	}
}

func docMetadataMatches(rec memorycore.Record, file candidateFile) bool {
	meta := rec.Metadata
	return meta[docMetaSize] == strconv.FormatInt(file.size, 10) &&
		meta[docMetaModTime] != "" && meta[docMetaModTime] == file.modTime
}

func (s *Service) storePath() string {
	return filepath.Join(s.app.Memory.Dir, "memory.db")
}

func (s *Service) discoverProjectDocs() ([]candidateFile, int64, error) {
	if s.workspace.Root == "" {
		return nil, 0, fmt.Errorf("workspace root is not configured")
	}
	var files []candidateFile
	var total int64
	err := filepath.WalkDir(s.workspace.Root, func(path string, d os.DirEntry, err error) error {
		file, include, walkErr := s.discoverProjectDoc(path, d, err)
		if include && walkErr == nil {
			total += file.size
			files = append(files, file)
		}
		return walkErr
	})
	slices.SortFunc(files, func(a, b candidateFile) int { return cmp.Compare(a.relPath, b.relPath) })
	return files, total, err
}

func (s *Service) discoverProjectDoc(path string, d os.DirEntry, walkErr error) (candidateFile, bool, error) {
	if walkErr != nil {
		return candidateFile{}, false, nil
	}
	rel, err := filepath.Rel(s.workspace.Root, path)
	if err != nil {
		return candidateFile{}, false, err
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return candidateFile{}, false, nil
	}
	if d.IsDir() {
		if shouldSkipDir(rel) {
			return candidateFile{}, false, filepath.SkipDir
		}
		return candidateFile{}, false, nil
	}
	return s.discoverProjectFile(path, rel, d)
}

func (s *Service) discoverProjectFile(path string, rel string, d os.DirEntry) (candidateFile, bool, error) {
	if !s.workspace.Allows(path) {
		return candidateFile{}, false, fmt.Errorf("path %q is outside allowed workspace roots", rel)
	}
	if !isRetrievalDoc(rel) || d.Type()&os.ModeSymlink != 0 {
		return candidateFile{}, false, nil
	}
	info, err := d.Info()
	if err != nil {
		return candidateFile{}, false, nil
	}
	file := candidateFile{absPath: path, relPath: rel, size: info.Size(), modTime: info.ModTime().UTC().Format(time.RFC3339Nano)}
	if info.Size() > maxIndexedFileBytes {
		return file, true, nil
	}
	binary, err := fileLooksBinary(path)
	if err != nil || binary {
		return candidateFile{}, false, nil
	}
	return file, true, nil
}

func (s *Service) readProjectDoc(file candidateFile) (sourceDoc, error) {
	if strings.Contains(file.relPath, "..") || filepath.IsAbs(file.relPath) {
		return sourceDoc{}, fmt.Errorf("path traversal rejected: %q", file.relPath)
	}
	data, err := os.ReadFile(file.absPath)
	if err != nil {
		return sourceDoc{}, err
	}
	if !utf8.Valid(data) {
		return sourceDoc{}, fmt.Errorf("file %q is not valid utf-8", file.relPath)
	}
	return sourceDoc{pathOrID: file.relPath, text: string(data), fileSize: file.size, fileModTime: file.modTime}, nil
}

func isRetrievalDoc(rel string) bool {
	ext := strings.ToLower(filepath.Ext(rel))
	return ext == ".md" || ext == ".markdown" || ext == ".txt"
}

func shouldSkipDir(rel string) bool {
	return strings.HasPrefix(filepath.Base(rel), ".")
}

func fileLooksBinary(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 8000)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return false, err
	}
	return bytes.IndexByte(buf[:n], 0) >= 0, nil
}

func countLines(data []byte) int {
	lines := bytes.Count(data, []byte{'\n'})
	if !bytes.HasSuffix(data, []byte{'\n'}) {
		lines++
	}
	return max(lines, 1)
}

func snippetFor(text string, queryTokens []string) string {
	lines := strings.Split(text, "\n")
	best := 0
	bestScore := -1
	for i, line := range lines {
		lower := strings.ToLower(strings.TrimSuffix(line, "\r"))
		score := len(slices.DeleteFunc(slices.Clone(queryTokens), func(token string) bool {
			return !strings.Contains(lower, token)
		}))
		if score > bestScore {
			best = i
			bestScore = score
		}
	}
	start := max(best-1, 0)
	end := min(best+1, len(lines)-1)
	var b strings.Builder
	for i := start; i <= end; i++ {
		line := core.TruncateRunes(strings.TrimSuffix(lines[i], "\r"), 240)
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		_, _ = fmt.Fprintf(&b, "%d: %s", i+1, line)
	}
	return b.String()
}

func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
}

// IndexStatus reports how many doc records this workspace holds and when the
// newest of them was written (zero when there are none).
func (s *Service) IndexStatus(ctx context.Context) (int, time.Time, error) {
	records, err := s.listDocRecords(ctx)
	var last time.Time
	for _, rec := range records {
		if rec.UpdatedAt.After(last) {
			last = rec.UpdatedAt
		}
	}
	return len(records), last, err
}
