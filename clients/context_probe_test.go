package clients

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestClient_MetadataFallsBackToModelList: a server that answers no /props still
// reports its window on the model list, which is what fills the footer's ctx
// limit for a routed provider.
func TestClient_MetadataFallsBackToModelList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"ornith","object":"model","context":262144},{"id":"other","n_ctx":4096}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	metadata, err := client.Metadata(context.Background(), "ornith")
	if err != nil {
		t.Fatalf("Metadata() returned error: %v", err)
	}
	if metadata.ContextWindowTokens != 262144 {
		t.Fatalf("expected context window 262144, got %d", metadata.ContextWindowTokens)
	}
	if metadata.Source != "models list" {
		t.Fatalf("expected metadata source, got %q", metadata.Source)
	}
}
