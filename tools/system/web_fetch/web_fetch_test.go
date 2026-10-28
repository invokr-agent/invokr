package web_fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/tools/core"
)

type fakeRegistrar struct {
	spec    core.ToolSpec
	handler core.NativeHandler
}

func (r *fakeRegistrar) RegisterNative(spec core.ToolSpec, handler core.NativeHandler) {
	r.spec, r.handler = spec, handler
}

func (r *fakeRegistrar) HasNative(string) bool { return false }

func (r *fakeRegistrar) NativeSource(string) string { return "" }

func (r *fakeRegistrar) ReplaceNativeToolsBySource(string, []core.DynamicNativeTool) []string {
	return nil
}

// call runs the tool body (past approval) against args, returning the decoded
// response plus whatever error the service reported.
func call(t *testing.T, service *fetchService, raw string) (toolResponse, error) {
	t.Helper()
	args, err := core.DecodeOptionalToolArgs[toolArgs](&core.ToolCall{Name: constants.WebFetchToolName, Arguments: json.RawMessage(raw)})
	if err != nil {
		t.Fatalf("decode args: %v", err)
	}
	if args, err = prepareArgs(context.Background(), args); err != nil {
		return toolResponse{}, err
	}
	out, err := service.fetch(context.Background(), args)
	var toolErr *core.ToolError
	if errors.As(err, &toolErr) {
		out = toolErr.Payload
	}
	var resp toolResponse
	if out != "" {
		if decodeErr := json.Unmarshal([]byte(out), &resp); decodeErr != nil {
			t.Fatalf("decode response %q: %v", out, decodeErr)
		}
	}
	return resp, err
}

func allowingService() *fetchService {
	return &fetchService{cfg: config.WebSearchConfig{AllowPrivateHosts: true}}
}

func write(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := io.WriteString(w, body); err != nil {
		t.Fatalf("write fixture body: %v", err)
	}
}

func serve(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func TestFetchReturnsReadableTextFromHTML(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		write(t, w, "<html><head><title>t</title><style>a{}</style></head><body><h2>Title</h2><p>Hello &amp; welcome</p><script>evil()</script></body></html>")
	})
	resp, err := call(t, allowingService(), `{"url":"`+server.URL+`"}`)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if resp.Status != 200 || resp.Truncated {
		t.Fatalf("unexpected response envelope: %#v", resp)
	}
	if !strings.Contains(resp.Content, "## Title") || !strings.Contains(resp.Content, "Hello & welcome") {
		t.Fatalf("content not readable: %q", resp.Content)
	}
	for _, unwanted := range []string{"<p>", "evil()", "a{}"} {
		if strings.Contains(resp.Content, unwanted) {
			t.Fatalf("content kept %q: %q", unwanted, resp.Content)
		}
	}
}

func TestOversizedResponseIsTruncatedButSucceeds(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		write(t, w, strings.Repeat("x", 5000))
	})
	resp, err := call(t, allowingService(), `{"url":"`+server.URL+`","max_bytes":600}`)
	if err != nil {
		t.Fatalf("truncated fetch must still succeed, got %v", err)
	}
	if !resp.Truncated || !strings.Contains(resp.Content, "[truncated: response exceeded 600 bytes;") {
		t.Fatalf("missing truncation marker: %#v", resp)
	}
	if body, _, _ := strings.Cut(resp.Content, "\n"); len(body) != 600 {
		t.Fatalf("kept %d bytes, want 600", len(body))
	}
}

func TestNonSuccessStatusIsFlaggedWithPayload(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		write(t, w, "no such page")
	})
	resp, err := call(t, allowingService(), `{"url":"`+server.URL+`"}`)
	var toolErr *core.ToolError
	if !errors.As(err, &toolErr) || toolErr.Payload == "" {
		t.Fatalf("want flagged tool error with payload, got %v", err)
	}
	if toolErr.Code != "http 404" || resp.Status != 404 || !strings.Contains(resp.Content, "no such page") {
		t.Fatalf("unexpected flagged result: code=%q resp=%#v", toolErr.Code, resp)
	}
}

func TestUnreachableHostIsFlaggedWithPayload(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close() // nothing is listening now: connect fails
	resp, err := call(t, allowingService(), `{"url":"`+url+`","timeout_ms":1000}`)
	var toolErr *core.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != "fetch_failed" || toolErr.Payload == "" {
		t.Fatalf("want flagged fetch_failed with payload, got %v", err)
	}
	if resp.Error == "" {
		t.Fatalf("payload must carry the failure detail: %#v", resp)
	}
}

// redirectServer hops /0 -> /1 -> ... -> /hops, then serves the body.
func redirectServer(t *testing.T, hops int) *httptest.Server {
	t.Helper()
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		var step int
		if _, err := fmt.Sscanf(r.URL.Path, "/%d", &step); err != nil || step >= hops {
			write(t, w, "landed")
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/%d", step+1), http.StatusFound)
	})
	return server
}

func TestRedirectChainWithinLimitIsFollowed(t *testing.T) {
	server := redirectServer(t, constants.WebFetchMaxRedirects)
	resp, err := call(t, allowingService(), `{"url":"`+server.URL+`/0"}`)
	if err != nil {
		t.Fatalf("5-hop redirect chain must be followed: %v", err)
	}
	if resp.Content != "landed" || !strings.HasSuffix(resp.FinalURL, "/5") {
		t.Fatalf("unexpected redirect result: %#v", resp)
	}
}

func TestRedirectChainBeyondLimitFails(t *testing.T) {
	server := redirectServer(t, constants.WebFetchMaxRedirects+2)
	_, err := call(t, allowingService(), `{"url":"`+server.URL+`/0"}`)
	var toolErr *core.ToolError
	if !errors.As(err, &toolErr) || !strings.Contains(toolErr.Error(), "stopped after 5 redirects") {
		t.Fatalf("want redirect-limit error, got %v", err)
	}
}

func TestPrivateHostIsRefusedWithoutAnyRequest(t *testing.T) {
	var hits int
	server := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		hits++
		write(t, w, "should never be served")
	})
	service := &fetchService{cfg: config.WebSearchConfig{}}
	for _, target := range []string{server.URL, "http://169.254.169.254/latest/meta-data", "http://10.1.2.3/", "http://printer.local/", "http://100.64.1.1/", "http://198.18.0.1/", "http://[fd00::1]/"} {
		resp, err := call(t, service, `{"url":"`+target+`"}`)
		var toolErr *core.ToolError
		if !errors.As(err, &toolErr) || toolErr.Code != "blocked_private_host" || toolErr.Payload == "" {
			t.Fatalf("%s: want flagged blocked_private_host, got %v", target, err)
		}
		if !strings.Contains(resp.Error, "refused to fetch private or loopback host") {
			t.Fatalf("%s: missing refusal detail: %#v", target, resp)
		}
	}
	if hits != 0 {
		t.Fatalf("blocked target was dialed %d times", hits)
	}
}

func TestInvalidURLIsRejectedBeforeApproval(t *testing.T) {
	for _, raw := range []string{`{"url":"file:///etc/passwd"}`, `{"url":"not a url"}`, `{"url":""}`} {
		if _, err := call(t, allowingService(), raw); err == nil || !strings.Contains(err.Error(), "absolute http or https URL") {
			t.Fatalf("%s: want scheme rejection, got %v", raw, err)
		}
	}
}

func TestToolIsRegisteredOnWebNetworkTierWithSchema(t *testing.T) {
	registry := &fakeRegistrar{}
	RegisterWebFetchTool(registry, core.CallerDeps{App: config.AppConfig{}.WithDefaults()})
	spec := registry.spec
	if spec.Name != constants.WebFetchToolName || spec.Metadata.Policy.Tiers[0] != constants.WebSearchPolicyTier || spec.Metadata.Policy.Approval != constants.ApprovalPolicyAsk {
		t.Fatalf("unexpected web_fetch spec: %#v", spec)
	}
	if !spec.Metadata.AlwaysVisible || !spec.Metadata.Capabilities.Network {
		t.Fatalf("web_fetch must stay visible to tool_search as a network tool: %#v", spec.Metadata)
	}
	props, _ := spec.Parameters["properties"].(map[string]any)
	if _, ok := props["url"]; !ok {
		t.Fatalf("schema missing url property: %#v", spec.Parameters)
	}
	// No approver is wired, so the call must be refused rather than fetched.
	_, err := registry.handler(context.Background(), &core.ToolCall{Name: constants.WebFetchToolName, Arguments: json.RawMessage(`{"url":"https://example.com"}`)})
	if err == nil || !strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("want web_network approval gate, got %v", err)
	}
}

// aliasResolver starts a throwaway DNS responder that answers every A query
// with ip, so a public-looking name can be pointed at a loopback test server
// without touching /etc/hosts.
func aliasResolver(t *testing.T, ip net.IP) *net.Resolver {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen dns: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, readErr := conn.ReadFrom(buf)
			if readErr != nil {
				return
			}
			_, _ = conn.WriteTo(dnsAnswer(buf[:n], ip.To4()), from)
		}
	}()
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", conn.LocalAddr().String())
	}}
}

// dnsAnswer builds a minimal reply: one A record for the queried name, or an
// empty NOERROR answer for any other query type.
func dnsAnswer(query []byte, ip net.IP) []byte {
	end := 12
	for end < len(query) && query[end] != 0 {
		end += int(query[end]) + 1
	}
	if end+5 > len(query) {
		return query
	}
	out := append([]byte(nil), query[:end+5]...)
	out[2], out[3] = 0x81, 0x80 // response, recursion available
	// Drop every section count, including any EDNS OPT record we did not copy.
	out[6], out[7], out[8], out[9], out[10], out[11] = 0, 0, 0, 0, 0, 0
	if out[end+2] != 1 { // not an A query
		return out
	}
	out[7] = 1
	return append(out, append([]byte{0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 30, 0, 4}, ip...)...)
}

// hitServer serves body from addr and counts every request that reaches it.
func hitServer(t *testing.T, addr, body string, hits *int32) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp", addr+":0")
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(hits, 1)
		write(t, w, body)
	})
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	server.Start()
	t.Cleanup(server.Close)
	return server
}

// TestPublicNameResolvingToPrivateAddressIsRefused is the SSRF regression: the
// name is public-looking and passes every name-based check, but resolves to
// loopback. Only a dial-time check can catch it.
func TestPublicNameResolvingToPrivateAddressIsRefused(t *testing.T) {
	var hits int32
	server := hitServer(t, "127.0.0.1", "SECRET-loopback-content", &hits)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("split server url: %v", err)
	}
	service := &fetchService{cfg: config.WebSearchConfig{}, resolver: aliasResolver(t, net.IPv4(127, 0, 0, 1))}
	resp, err := call(t, service, `{"url":"http://evil-public.example:`+port+`/","timeout_ms":5000}`)
	t.Logf("preflight_blocked_host=%q status=%d content=%q hits=%d err=%v",
		service.blockedHost("http://evil-public.example/"), resp.Status, resp.Content, atomic.LoadInt32(&hits), err)
	var toolErr *core.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != "blocked_private_host" || toolErr.Payload == "" {
		t.Fatalf("want flagged blocked_private_host, got %v", err)
	}
	if strings.Contains(resp.Content, "SECRET") {
		t.Fatalf("loopback content leaked into the tool result: %#v", resp)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("blocked target was reached %d times", got)
	}
}

// TestRedirectToPrivateTargetIsRefused proves the private hop is stopped at the
// dial: neutering the guard lets the redirect through and fails this test. The
// deny seam marks 127.0.0.2 private so the first hop can stand in for a public
// origin while still running on loopback.
func TestRedirectToPrivateTargetIsRefused(t *testing.T) {
	var privateHits int32
	private := hitServer(t, "127.0.0.2", "SECRET-redirect-target", &privateHits)
	public := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, private.URL+"/loot", http.StatusFound)
	})
	service := &fetchService{cfg: config.WebSearchConfig{}, deny: func(ip netip.Addr) bool { return ip.String() == "127.0.0.2" }}
	resp, err := call(t, service, `{"url":"`+public.URL+`/start","timeout_ms":5000}`)
	t.Logf("status=%d content=%q private_hits=%d err=%v", resp.Status, resp.Content, atomic.LoadInt32(&privateHits), err)
	var toolErr *core.ToolError
	if !errors.As(err, &toolErr) || toolErr.Payload == "" {
		t.Fatalf("want flagged tool error for the private redirect hop, got %v", err)
	}
	if strings.Contains(resp.Content, "SECRET") {
		t.Fatalf("redirect target content leaked: %#v", resp)
	}
	if got := atomic.LoadInt32(&privateHits); got != 0 {
		t.Fatalf("private redirect target was reached %d times", got)
	}
}

// TestAlternateAddressFormsNeverReachTheTarget covers decimal/octal/hex/short
// IPv4 spellings of 127.0.0.1, which name-based checks miss entirely.
func TestAlternateAddressFormsNeverReachTheTarget(t *testing.T) {
	var hits int32
	server := hitServer(t, "127.0.0.1", "SECRET-alt-form", &hits)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("split server url: %v", err)
	}
	service := &fetchService{cfg: config.WebSearchConfig{}}
	for _, host := range []string{"2130706433", "0177.0.0.1", "0x7f000001", "127.1"} {
		resp, fetchErr := call(t, service, `{"url":"http://`+host+`:`+port+`/","timeout_ms":5000}`)
		if fetchErr == nil || strings.Contains(resp.Content, "SECRET") {
			t.Fatalf("%s: fetch must not succeed, got %#v (%v)", host, resp, fetchErr)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("alternate address forms reached the target %d times", got)
	}
}

func TestHTMLToTextTrimsUnicodeWhitespaceAtLineEdges(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"alpha&nbsp;<br>&nbsp;beta", "alpha\nbeta"},
		{"alpha<br>&nbsp;<br>beta", "alpha\n\nbeta"}, // nbsp-only line collapses to a blank line
		{"alpha\f<br>\vbeta", "alpha\nbeta"},
	} {
		if got := htmlToText(tc.in); got != tc.want {
			t.Fatalf("htmlToText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestHTMLToTextDropsStyleAndScriptNoise covers the S2 bench failure: a page
// whose stylesheet is large enough that the byte cap cuts inside it used to
// hand the model raw minified CSS as page text.
func TestHTMLToTextDropsStyleAndScriptNoise(t *testing.T) {
	bigCSS := strings.Repeat(".hdr__a{color:#fff;margin:0}", 400)
	cases := []struct {
		name, in string
	}{
		{"closed style and script", "<html><head><style>" + bigCSS + "</style></head><body><script>var a=1;</script><main><p>Markets closed higher.</p></main></body></html>"},
		{"style cut by the byte cap", "<html><head><style>" + bigCSS},
		{"svg and comment", "<body><!-- tracking --><svg><path d=\"M0{0};\"/></svg><article><p>Markets closed higher.</p></article></body>"},
		{"noscript and template", "<body><noscript><style>" + bigCSS + "</style></noscript><template><p>hidden</p></template><main><p>Markets closed higher.</p></main></body>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := htmlToText(tc.in)
			for _, banned := range []string{"hdr__a", "color:#fff", "var a=1", "tracking", "hidden"} {
				if strings.Contains(got, banned) {
					t.Fatalf("htmlToText leaked %q: %q", banned, got)
				}
			}
			if tc.name != "style cut by the byte cap" && !strings.Contains(got, "Markets closed higher.") {
				t.Fatalf("htmlToText dropped the article text: %q", got)
			}
		})
	}
}
