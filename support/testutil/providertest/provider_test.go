package providertest

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

func TestCompletionStepFaultControls(t *testing.T) {
	called := false
	server := New(t, []CompletionStep{
		{Status: http.StatusTooManyRequests, Body: `{"error":"slow down"}`, RetryAfterSeconds: 2, BeforeResponse: func() { called = true }},
		{MalformedBody: "{bad"},
	})
	defer server.Close()

	resp := postCompletion(t, server.URL())
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "2" || !called {
		t.Fatalf("error response = status %d retry-after %q callback %v", resp.StatusCode, resp.Header.Get("Retry-After"), called)
	}
	_ = resp.Body.Close()

	resp = postCompletion(t, server.URL())
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if string(body) != "{bad" || resp.ContentLength != 4 {
		t.Fatalf("malformed response body=%q length=%d", body, resp.ContentLength)
	}

	snapshot := server.Snapshot()
	if len(snapshot.CompletionRequests) != 2 || snapshot.CompletionRequests[0].ReceivedAt.IsZero() {
		t.Fatalf("requests = %#v", snapshot.CompletionRequests)
	}
	server.AssertClean(t)
	server.AssertExhausted(t)
}

func postCompletion(t *testing.T, baseURL string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewBufferString(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Close = true
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
