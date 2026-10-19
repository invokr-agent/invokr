package clients

import (
	"context"

	inference "github.com/rivt-ai/go-inference-router"
)

type LLMProvider interface {
	Completion(ctx context.Context, req inference.Request) (*inference.Response, error)
	Metadata(ctx context.Context, model string) (ProviderMetadata, error)
}

type ProviderMetadata struct {
	ContextWindowTokens int64
	Source              string
	// Reasoning is the provider's effort scale, empty when none is known;
	// ReasoningEffort reports a llama.cpp chat template reads the field, and
	// EnableThinking that it keys on the chat_template_kwargs switch instead.
	Reasoning                       ReasoningKind
	ReasoningEffort, EnableThinking bool
	// NoVision reports the server said the model takes no image input
	// (llama.cpp /props modalities); false when unknown.
	NoVision bool
}

type SummaryInput struct {
	Prompt string
	JSON   bool
	Model  string
	// MaxTokens caps the completion's output tokens when > 0 (used by the
	// recall gate's cheap probe). Zero leaves the provider default.
	MaxTokens int
}

type EmbeddingInput struct {
	Texts []string
}
