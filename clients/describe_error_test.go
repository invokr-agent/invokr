package clients

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	inference "github.com/rivt-ai/go-inference-router"
)

func TestDescribeError(t *testing.T) {
	refused := &url.Error{Op: "Post", URL: "http://127.0.0.1:19201/v1/chat/completions",
		Err: errors.New("dial tcp 127.0.0.1:19201: connect: connection refused")}
	dropped := &url.Error{Op: "Post", URL: "http://127.0.0.1:19201/v1/chat/completions", Err: errors.New("unexpected EOF")}
	cases := []struct {
		name string
		err  error
		want string
		not  []string
	}{
		{"refused", &inference.Error{Kind: inference.KindTransport, Provider: "default", Err: refused},
			"Can't reach the model server at 127.0.0.1:19201", []string{"default:", "dial tcp"}},
		{"dropped", &inference.Error{Kind: inference.KindTransport, Provider: "default", Err: dropped},
			"Lost the connection to the model server mid-reply", nil},
		{"empty", fmt.Errorf("provider stream ended: %w", &inference.Error{Kind: inference.KindEmptyResponse, Status: 200}),
			"closed the reply without sending anything", []string{"empty_response"}},
		{"503", &inference.Error{Kind: inference.KindUnavailable, Status: 503, Message: "loading model"},
			"returned an error (HTTP 503): loading model.", []string{"unavailable"}},
		{"404", &inference.Error{Kind: inference.KindInvalidRequest, Status: 404, Message: "not found"},
			"Check provider.base_url (it usually ends in /v1)", nil},
		{"400", &inference.Error{Kind: inference.KindInvalidRequest, Status: 400, Message: "bad field"},
			"rejected the request (HTTP 400): bad field.", nil},
		{"auth", &inference.Error{Kind: inference.KindAuth, Status: 401}, "rejected the credentials (HTTP 401)", nil},
		{"plain", errors.New("something else"), "something else", nil},
	}
	for _, c := range cases {
		got := DescribeError(c.err)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q, want it to contain %q", c.name, got, c.want)
		}
		for _, bad := range c.not {
			if strings.Contains(got, bad) {
				t.Errorf("%s: got %q, must not contain %q", c.name, got, bad)
			}
		}
	}
	if DescribeError(nil) != "" {
		t.Error("nil error must describe as empty")
	}
}
