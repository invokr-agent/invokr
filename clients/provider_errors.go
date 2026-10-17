package clients

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	inference "github.com/rivt-ai/go-inference-router"
)

const llamaContextOverflow = "context size has been exceeded"

// contextOverflowMarkers are the phrasings OpenAI-compatible providers use to
// reject a request that exceeds the model's context window.
var contextOverflowMarkers = []string{
	"context length", "context window", "context_length_exceeded", llamaContextOverflow,
	"exceeds the available context", "prompt is too long", "input is too long", "too many tokens",
}

// IsContextLengthExceeded reports whether err is a provider rejection of a
// request that exceeds the model's context window. The router has no
// dedicated kind for it: request rejections are KindInvalidRequest, while
// llama.cpp can report KV exhaustion as KindUnavailable mid-stream. Inspect
// that specific message here so unrelated server failures stay failures.
func IsContextLengthExceeded(err error) bool {
	var provider *inference.Error
	if !errors.As(err, &provider) {
		return false
	}
	message := strings.ToLower(provider.Message)
	if provider.Kind == inference.KindUnavailable {
		return strings.Contains(message, llamaContextOverflow)
	}
	return provider.Kind == inference.KindInvalidRequest && slices.ContainsFunc(contextOverflowMarkers, func(marker string) bool {
		return strings.Contains(message, marker)
	})
}

// normalizeProviderError restates a router failure in the wording invokr's
// operators and logs expect, while keeping the typed *inference.Error in the
// chain so callers still classify with inference.IsKind rather than by text.
// Since v0.7.0 the router's Error() already folds the underlying cause into
// its message, so only the kinds with invokr-specific operator guidance are
// reworded here.
func normalizeProviderError(err error) error {
	var provider *inference.Error
	if err == nil || !errors.As(err, &provider) {
		return err
	}
	switch provider.Kind {
	case inference.KindStalled:
		return fmt.Errorf("provider stalled: %s: %w", provider.Message, err)
	case inference.KindEmptyResponse:
		return fmt.Errorf(
			"provider stream ended without any completion chunks (malformed or empty response body): %w", err)
	}
	return err
}
