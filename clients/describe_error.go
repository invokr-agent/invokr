package clients

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	inference "github.com/rivt-ai/go-inference-router"
)

// errorWording is the plain-words summary and next step for each failure
// kind. %s takes " (HTTP n)" when the provider sent a status.
var errorWording = map[inference.Kind]string{
	inference.KindEmptyResponse:  "The model server closed the reply without sending anything%s. It may have crashed, or it sent something invokr can't read; check its logs.",
	inference.KindStalled:        "The model server stopped sending mid-reply%s. Send the message again, or check whether it is overloaded.",
	inference.KindAuth:           "The model server rejected the credentials%s. Check the API key for this provider (`invokr provider`).",
	inference.KindRateLimit:      "The provider is rate limiting requests%s. Wait a moment and send again.",
	inference.KindInvalidRequest: "The model server rejected the request%s.",
	inference.KindUnavailable:    "The model server returned an error%s. Try again, or check its logs.",
	inference.KindProtocol:       "The model server sent a reply invokr couldn't read%s.",
}

// DescribeError restates a model call failure as one line a user can act on:
// what went wrong in plain words, then what to check. The router's own text
// (profile name, error kind, the Go transport chain) is for logs, not for the
// transcript. Errors that are not provider failures pass through unchanged.
func DescribeError(err error) string {
	var provider *inference.Error
	switch {
	case err == nil:
		return ""
	case !errors.As(err, &provider):
		return err.Error()
	case provider.Kind == inference.KindTransport:
		if host, ok := unreachableHost(err); ok {
			return fmt.Sprintf("Can't reach the model server at %s. Is it running? Check provider.base_url, or run `invokr doctor`.", host)
		}
		return "Lost the connection to the model server mid-reply. Send the message again once it is back."
	case provider.Kind == inference.KindInvalidRequest && provider.Status == 404:
		return "The model server answered 404 Not Found. Check provider.base_url (it usually ends in /v1) and the model name."
	}
	wording, ok := errorWording[provider.Kind]
	if !ok {
		return err.Error()
	}
	detail := ""
	if provider.Status != 0 {
		detail = fmt.Sprintf(" (HTTP %d)", provider.Status)
	}
	if message := strings.TrimSpace(provider.Message); message != "" && provider.Kind != inference.KindEmptyResponse {
		detail += ": " + message
	}
	return fmt.Sprintf(wording, detail)
}

// unreachableHost reports the host of a request that never connected: a
// refused connection, an unknown host or a dial failure. A connection that
// opened and then dropped is a different failure (mid-reply), so it is false.
func unreachableHost(err error) (string, bool) {
	text := err.Error()
	if !strings.Contains(text, "connection refused") && !strings.Contains(text, "no such host") && !strings.Contains(text, "dial tcp") {
		return "", false
	}
	var request *url.Error
	if errors.As(err, &request) {
		if parsed, parseErr := url.Parse(request.URL); parseErr == nil && parsed.Host != "" {
			return parsed.Host, true
		}
	}
	return "the configured address", true
}
