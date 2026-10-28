package zerfoo

import (
	"os"
	"strings"

	"github.com/invokr-agent/invokr/config"

	"github.com/zerfoo/zerfoo/model/huggingface"
)

// ModelStatus reports whether one configured Zerfoo model is already on disk.
// Bytes is the GGUF size when Cached; when it is not cached, Bytes is 0 and the
// file is downloaded on first memory use.
type ModelStatus struct {
	Role   string // "embedding" or "summarization"
	Ref    string // the configured model ID or local path
	Path   string // resolved GGUF path, empty when nothing is cached yet
	Bytes  int64
	Cached bool
	Err    error // set only when a configured local path is unusable
}

// CacheStatus resolves each configured model exactly the way the worker's
// loader does — local *_path overrides are stat'd directly, HuggingFace IDs are
// looked up in ~/.cache/zerfoo/manifest.json — so callers can report the cache
// state without loading (or downloading) anything.
func CacheStatus(cfg config.ZerfooMemoryConfig) []ModelStatus {
	cfg = cfg.WithDefaults()
	manifest, err := huggingface.LoadManifest()
	if err != nil {
		manifest = &huggingface.CacheManifest{}
	}
	return []ModelStatus{modelStatus("embedding", cfg.EmbeddingRef(), manifest),
		modelStatus("summarization", cfg.SummarizationRef(), manifest)}
}

func modelStatus(role, ref string, manifest *huggingface.CacheManifest) ModelStatus {
	s := ModelStatus{Role: role, Ref: ref}
	if isLocalPath(ref) {
		s.Path = ref
		info, err := os.Stat(ref)
		if err != nil {
			s.Err = err
			return s
		}
		s.Bytes, s.Cached = info.Size(), true
		return s
	}
	cached, ok := manifest.FindByRepo(repoID(ref))
	if !ok {
		return s
	}
	s.Path = cached.Path
	info, err := os.Stat(cached.Path)
	if err != nil {
		// Manifest entry without the file: zerfoo re-downloads on next load.
		return s
	}
	s.Bytes, s.Cached = info.Size(), true
	return s
}

// repoID strips the optional "/QUANT" suffix, matching zerfoo's parseModelID:
// the manifest is keyed by "owner/model", not by quant.
func repoID(ref string) string {
	if parts := strings.Split(ref, "/"); len(parts) == 3 {
		return parts[0] + "/" + parts[1]
	}
	return ref
}

// isLocalPath mirrors zerfoo.Load's rule verbatim: only "/", "./" and "../"
// prefixes are treated as files on disk; everything else is a HuggingFace model
// ID. Deliberately not filepath.IsAbs — the upstream rule is prefix-based, and
// diverging would make doctor disagree with the loader.
func isLocalPath(ref string) bool {
	return strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "../")
}
