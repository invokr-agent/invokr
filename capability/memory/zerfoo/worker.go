// Package zerfoo provides Invokr's built-in memory model runtime backed by
// github.com/zerfoo/zerfoo. It loads GGUF models in-process (no llama-server,
// no CGo, no Python) to embed memory chunks and summarize transcripts.
//
// The Worker satisfies the small Invokr-owned interfaces consumed by the
// memory manager:
//
//   - MemoryEmbedder: EmbedDocuments / EmbedQueries (and Embed, an alias for
//     documents, used by the retrieval tool's EmbeddingProvider).
//   - MemorySummarizer: Summarize.
//   - io.Closer: Close.
//
// Models are loaded lazily on first use and the loads are concurrency-safe, so
// importing this package or constructing a Worker during unrelated commands or
// tests does not load anything.
package zerfoo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	zf "github.com/zerfoo/zerfoo"

	"github.com/invokr-ai/invokr/capability/memory/summary"
	"github.com/invokr-ai/invokr/config"
)

// queryPrefix is the LFM2.5-Embedding instruction applied to query text so that
// queries and stored documents take distinct embedding paths (the model's
// asymmetric query/document convention). Documents are embedded raw. zerfoo's
// Embed mean-pools and L2-normalizes token embeddings, so the prefix effect is
// modest, but it honors the convention and keeps the paths genuinely distinct.
// See docs/memory.md.
const queryPrefix = "query: "

var errClosed = errors.New("zerfoo memory worker is closed")

// embedModel and genModel are the minimal slices of *zerfoo.Model the worker
// needs. Keeping them as interfaces prevents zerfoo types from leaking into the
// rest of Invokr and lets tests inject fakes without loading real models.
type embedModel interface {
	Embed(texts []string) ([][]float32, error)
	Close() error
}

type genModel interface {
	Generate(ctx context.Context, prompt string, maxTokens int) (string, error)
	Close() error
}

// Loaders builds the underlying models from a model ID or local GGUF path.
// Overridable in tests.
type Loaders struct {
	Embed func(ref string) (embedModel, error)
	Gen   func(ref string) (genModel, error)
}

// Worker is a lazily-loaded, concurrency-safe memory model runtime.
type Worker struct {
	cfg     config.ZerfooMemoryConfig
	loaders Loaders

	// mu guards the cached models and closed flag; it is only ever held briefly.
	// loadEmbed/loadGen are size-1 channel semaphores that serialize the slow
	// load of each model so concurrent callers don't trigger duplicate GGUF
	// loads. They are acquired with a ctx-aware select so a cancelled caller
	// returns immediately instead of blocking on an in-progress load.
	// embedMu/genMu serialize inference on each model, whose GGUF runner keeps
	// mutable per-call state (KV cache, scratch buffers) and is not safe for
	// concurrent use.
	mu        sync.Mutex
	embed     embedModel
	gen       genModel
	closed    bool
	loadEmbed chan struct{}
	loadGen   chan struct{}
	embedMu   sync.Mutex
	genMu     sync.Mutex
}

func NewWorker(cfg config.ZerfooMemoryConfig) *Worker {
	return NewWorkerWithLoaders(cfg, defaultLoaders())
}

// NewWorkerWithLoaders is used by tests to inject fake models.
func NewWorkerWithLoaders(cfg config.ZerfooMemoryConfig, loaders Loaders) *Worker {
	return &Worker{cfg: cfg.WithDefaults(), loaders: loaders, loadEmbed: make(chan struct{}, 1), loadGen: make(chan struct{}, 1)}
}

// defaultLoaders wraps zerfoo's loader; on failure the returned model is never
// used, because every caller checks the error first.
func defaultLoaders() Loaders {
	return Loaders{
		Embed: func(ref string) (embedModel, error) { m, err := zf.Load(ref); return zfEmbed{m}, err },
		Gen:   func(ref string) (genModel, error) { m, err := zf.Load(ref); return zfGen{m}, err }}
}

type zfEmbed struct{ m *zf.Model }

func (e zfEmbed) Embed(texts []string) ([][]float32, error) {
	embs, err := e.m.Embed(texts)
	if err != nil {
		return nil, err
	}
	out := make([][]float32, len(embs))
	for i, emb := range embs {
		out[i] = emb.Vector
	}
	return out, nil
}

func (e zfEmbed) Close() error { return e.m.Close() }

type zfGen struct{ m *zf.Model }

// Generate uses greedy decoding: no temperature is passed, so zerfoo decodes
// deterministically (temperature is only applied when > 0).
func (g zfGen) Generate(ctx context.Context, prompt string, maxTokens int) (string, error) {
	opts := []zf.GenerateOption{}
	if maxTokens > 0 {
		opts = append(opts, zf.WithGenMaxTokens(maxTokens))
	}
	res, err := g.m.Generate(ctx, prompt, opts...)
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

func (g zfGen) Close() error { return g.m.Close() }

func (w *Worker) embedder(ctx context.Context) (embedModel, error) {
	return loadModelOnce(ctx, w, &w.embed, w.loadEmbed, configuredLoad("embedding", w.cfg.EmbeddingRef(), w.loaders.Embed))
}

func (w *Worker) generator(ctx context.Context) (genModel, error) {
	return loadModelOnce(ctx, w, &w.gen, w.loadGen, configuredLoad("summarization", w.cfg.SummarizationRef(), w.loaders.Gen))
}

// configuredLoad refuses an unconfigured model ref and otherwise names the role
// in the load error, so both model kinds report failures the same way.
func configuredLoad[M any](role, ref string, load func(string) (M, error)) func() (M, error) {
	return func() (M, error) {
		var zero M
		if ref == "" {
			return zero, errors.New("zerfoo " + role + " model is not configured")
		}
		m, err := load(ref)
		if err != nil {
			return zero, fmt.Errorf("load %s model %q: %w", role, ref, err)
		}
		return m, nil
	}
}

// loadModelOnce lazily populates *slot with the result of load, caching it for
// future calls. sem is a size-1 channel semaphore that serializes the slow load
// per model so concurrent callers don't trigger duplicate GGUF loads: the first
// caller loads while the rest wait and then reuse the cached model. Acquisition
// is ctx-aware, so a cancelled caller returns immediately instead of blocking on
// an in-progress load. w.mu is only held briefly, so the load never blocks Close
// or the other model type.
//
// M must be an interface type (it is only ever instantiated with embedModel and
// genModel). The any(*slot) != nil checks below rely on that: for an interface
// value, any() is nil exactly when the value is unset. Do not instantiate M with
// a concrete pointer type, or a typed-nil would read as non-nil.
func loadModelOnce[M interface{ Close() error }](ctx context.Context, w *Worker, slot *M, sem chan struct{}, load func() (M, error)) (M, error) {
	var zero M
	if m, cached, err := loadedModel(w, slot); cached || err != nil {
		return m, err
	}

	// Acquire the per-model load semaphore, but bail out if ctx is cancelled
	// rather than blocking on another caller's in-progress load.
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return zero, ctx.Err()
	}

	// Re-check under the semaphore: a prior holder may have loaded the model.
	if m, cached, err := loadedModel(w, slot); cached || err != nil {
		return m, err
	}

	m, err := load()
	if err != nil {
		return zero, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		_ = m.Close()
		return zero, errClosed
	}
	*slot = m
	return m, nil
}

// loadedModel reports the already-loaded model in *slot, if any. The bool is
// true when the returned model is usable; a closed worker yields errClosed.
// See loadModelOnce for why M must be an interface type.
func loadedModel[M any](w *Worker, slot *M) (M, bool, error) {
	var zero M
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return zero, false, errClosed
	}
	if m := *slot; any(m) != nil {
		return m, true, nil
	}
	return zero, false, nil
}

// Embed embeds stored documents. It is an alias for EmbedDocuments and
// satisfies the retrieval tool's EmbeddingProvider.
func (w *Worker) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return w.EmbedDocuments(ctx, texts)
}

func (w *Worker) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	return w.embedTexts(ctx, texts, false)
}

// EmbedQueries embeds search queries with query semantics (instruction prefix).
func (w *Worker) EmbedQueries(ctx context.Context, texts []string) ([][]float32, error) {
	return w.embedTexts(ctx, texts, true)
}

func (w *Worker) embedTexts(ctx context.Context, texts []string, asQuery bool) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m, err := w.embedder(ctx)
	if err != nil {
		return nil, err
	}
	in := texts
	if asQuery {
		in = make([]string, len(texts))
		for i, t := range texts {
			in[i] = queryPrefix + t
		}
	}
	// Re-check after embedder(), which may have spent time loading the model.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Serialize inference: the GGUF runner is not safe for concurrent calls.
	// Re-check closed under embedMu so Close (which takes embedMu before freeing
	// the model) can't free it mid-Embed.
	w.embedMu.Lock()
	if w.isClosed() {
		w.embedMu.Unlock()
		return nil, errClosed
	}
	vectors, err := m.Embed(in)
	w.embedMu.Unlock()
	if err != nil {
		return nil, err
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("zerfoo embed returned %d vectors, want %d", len(vectors), len(texts))
	}
	if err := w.validate(vectors); err != nil {
		return nil, err
	}
	return vectors, nil
}

// validate enforces non-empty, finite, and (when configured) fixed-dimension
// embeddings. zerfoo returns L2-normalized vectors, so no extra normalization
// is performed here; see docs/memory.md.
func (w *Worker) validate(vectors [][]float32) error {
	for _, v := range vectors {
		if len(v) == 0 {
			return errors.New("zerfoo embed returned empty vector")
		}
		if w.cfg.EmbeddingDimensions > 0 && len(v) != w.cfg.EmbeddingDimensions {
			return fmt.Errorf("zerfoo embed returned vector dimension %d, want %d", len(v), w.cfg.EmbeddingDimensions)
		}
		for _, f := range v {
			if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
				return errors.New("zerfoo embed returned non-finite value")
			}
		}
	}
	return nil
}

// Summarize produces a faithful, concise memory summary using greedy decoding.
func (w *Worker) Summarize(ctx context.Context, input summary.StructuredSummaryInput) (summary.MemorySummary, error) {
	prompt, err := summary.RenderSummaryPrompt(input)
	if err != nil {
		return summary.MemorySummary{}, err
	}
	if err := ctx.Err(); err != nil {
		return summary.MemorySummary{}, err
	}
	g, err := w.generator(ctx)
	if err != nil {
		return summary.MemorySummary{}, err
	}
	if w.cfg.RequestTimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(w.cfg.RequestTimeoutMS)*time.Millisecond)
		defer cancel()
	}
	// Serialize inference: the GGUF runner is not safe for concurrent calls.
	// Re-check closed under genMu so Close can't free the model mid-Generate.
	w.genMu.Lock()
	if w.isClosed() {
		w.genMu.Unlock()
		return summary.MemorySummary{}, errClosed
	}
	raw, err := g.Generate(ctx, prompt, w.cfg.MaxSummaryTokens)
	w.genMu.Unlock()
	if err != nil {
		return summary.MemorySummary{}, err
	}
	return summary.ParseMemorySummary(raw)
}

func (w *Worker) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

// Close releases any loaded models. It is safe to call more than once.
//
// It marks the worker closed and detaches the model handles under w.mu, then
// releases w.mu and closes each model while holding that model's inference lock
// (embedMu/genMu). Taking the inference lock makes Close wait for any in-flight
// Embed/Generate to finish before freeing the model; new calls re-check closed
// under the same lock and bail with errClosed. w.mu is released first so a slow
// in-flight inference can't block Close from even starting.
func (w *Worker) Close() error {
	if w == nil { // Memory without a local worker has nothing to close.
		return nil
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	embed, gen := w.embed, w.gen
	w.embed, w.gen = nil, nil
	w.mu.Unlock()

	return errors.Join(closeUnder(&w.embedMu, embed), closeUnder(&w.genMu, gen))
}

// closeUnder frees a model while holding its inference lock, so no call is mid-run.
func closeUnder(mu *sync.Mutex, model io.Closer) error {
	if model == nil {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	return model.Close()
}
