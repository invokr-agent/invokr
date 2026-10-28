// Package web_fetch fetches one http(s) URL and returns its readable text. It
// shares the web_network policy tier and the sensitive-term / private-host
// settings of config.WebSearchConfig with web_search, so one policy governs
// both outbound web tools.
package web_fetch

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/support/mathutil"
	"github.com/invokr-agent/invokr/support/strutil"
	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/system/web_search"
)

type toolArgs struct {
	URL       string `json:"url" desc:"Absolute http(s) URL to fetch."`
	MaxBytes  int    `json:"max_bytes,omitempty" desc:"Optional response byte cap; content past it is truncated with a marker."`
	TimeoutMS int    `json:"timeout_ms,omitempty" desc:"Optional request timeout in milliseconds."`
}

type toolResponse struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url,omitempty"`
	Status      int    `json:"status,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	Error       string `json:"error,omitempty"`
	Content     string `json:"content"`
}

type fetchService struct {
	cfg        config.WebSearchConfig
	dispatcher events.Dispatcher
	// resolver overrides DNS and deny overrides the address classifier; both are
	// test seams and are nil in production.
	resolver *net.Resolver
	deny     func(netip.Addr) bool
}

func (s *fetchService) denies(ip netip.Addr) bool {
	if s.deny != nil {
		return s.deny(ip)
	}
	return blockedAddr(ip)
}

// transport clones the default transport, so HTTP_PROXY/HTTPS_PROXY/NO_PROXY
// still apply, and installs the dial-time guard. AllowPrivateHosts is the only
// thing that lifts the guard.
func (s *fetchService) transport() http.RoundTripper {
	base := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Resolver: s.resolver}
	if !s.cfg.AllowPrivateHosts {
		dialer.Control = s.guardDial
	}
	base.DialContext = dialer.DialContext
	return base
}

// blockedAddrError marks a refusal made at the dial, so the tool can classify
// it via errors.As instead of matching on the message text.
type blockedAddrError struct{ ip netip.Addr }

func (e *blockedAddrError) Error() string {
	return fmt.Sprintf("refused to fetch private or loopback host %q", e.ip)
}

// guardDial is the security boundary: it runs after DNS resolution and before
// connect, so it sees the address actually being dialed. That defeats names
// that resolve into private space, DNS rebinding and alternate IPv4 spellings,
// covers every redirect hop with no per-hop logic, and works with either
// resolver build.
func (s *fetchService) guardDial(_, address string, _ syscall.RawConn) error {
	addr, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("refused to dial unparsable address %q: %w", address, err)
	}
	if ip := addr.Addr().Unmap(); s.denies(ip) {
		return &blockedAddrError{ip: ip}
	}
	return nil
}

// RegisterWebFetchTool registers web_fetch, which always asks on the
// web_network tier so `invokr exec` denies it unless --allow web_network (or a
// standing grant) covers the call.
func RegisterWebFetchTool(c core.NativeRegistrar, deps core.CallerDeps) {
	service := &fetchService{cfg: deps.App.WebSearch, dispatcher: deps.Dispatcher}
	core.Register(c, core.Tool[toolArgs]{
		Spec: core.ToolSpec{
			Name: constants.WebFetchToolName, Description: "Fetch one http(s) URL and return the page as readable text (HTML tags stripped, headings kept). Size-capped with an explicit truncation marker. Uses web_network policy.",
			Example:    `{"name":"web_fetch","arguments":{"url":"https://example.com/page"}}`,
			Parameters: core.SchemaFor[toolArgs](), Metadata: core.NetworkAskMetadata("network", constants.WebSearchPolicyTier, core.ToolCapabilities{Network: true}),
		},
		Prepare:  prepareArgs,
		Approve:  service.approve,
		Approver: deps.Approver,
		Execute:  service.fetch,
	})
}

func prepareArgs(_ context.Context, args toolArgs) (toolArgs, error) {
	args.URL = strings.TrimSpace(args.URL)
	parsed, err := url.Parse(args.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return args, fmt.Errorf("url must be an absolute http or https URL, got %q", args.URL)
	}
	args.MaxBytes = mathutil.ClampInt(args.MaxBytes, constants.WebFetchDefaultMaxBytes, constants.WebFetchMaxBytesCap)
	args.TimeoutMS = mathutil.ClampInt(args.TimeoutMS, constants.WebFetchDefaultTimeoutMS, constants.WebFetchMaxTimeoutMS)
	return args, nil
}

func (s *fetchService) approve(_ context.Context, args toolArgs) core.ApprovalDecision {
	reason := "outbound web fetch"
	if web_search.IsSensitiveTarget(args.URL, s.cfg) {
		reason = "url matched sensitive web policy"
	}
	return core.Ask(approval.Request{
		ReadOnly: true, Network: true,
		Tool: constants.WebFetchToolName, Reason: reason, Tier: constants.WebSearchPolicyTier,
		Summary: "Fetch " + args.URL, Subject: args.URL,
		Details: fmt.Sprintf("URL: %s\nMax bytes: %d\nTimeout ms: %d", args.URL, args.MaxBytes, args.TimeoutMS),
	})
}

func (s *fetchService) fetch(ctx context.Context, args toolArgs) (out string, err error) {
	resp := toolResponse{URL: args.URL}
	defer func() { s.audit(ctx, resp, err) }()
	// Refuse before any request is made: a blocked host must never be dialed.
	if host := s.blockedHost(args.URL); host != "" {
		return "", flagged("blocked_private_host", resp, fmt.Errorf("refused to fetch private or loopback host %q", host))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, args.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", constants.WebFetchUserAgent)
	// http.DefaultTransport honors HTTP_PROXY/HTTPS_PROXY/NO_PROXY.
	client := &http.Client{Timeout: time.Duration(args.TimeoutMS) * time.Millisecond, CheckRedirect: s.checkRedirect, Transport: s.transport()}
	httpResp, err := client.Do(req)
	if err != nil {
		return "", flagged(failureCode(err), resp, err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, int64(args.MaxBytes)+1))
	if err != nil {
		return "", flagged("read_failed", resp, err)
	}
	resp.Status, resp.ContentType, resp.FinalURL = httpResp.StatusCode, httpResp.Header.Get("Content-Type"), httpResp.Request.URL.String()
	resp.Content, resp.Truncated = render(string(body), resp.ContentType, args.MaxBytes)
	if resp.Status/100 != 2 {
		return "", flagged(fmt.Sprintf("http %d", resp.Status), resp, fmt.Errorf("%s returned HTTP %d", args.URL, resp.Status))
	}
	return core.MarshalResult(constants.WebFetchToolName, resp)
}

// flagged returns a completed-but-flagged tool error: Payload is always
// non-empty, so the model still sees the failure detail (CLAUDE.md gotcha).
func flagged(code string, resp toolResponse, err error) error {
	resp.Error = err.Error()
	payload, marshalErr := core.MarshalResult(constants.WebFetchToolName, resp)
	if marshalErr != nil {
		return err
	}
	return &core.ToolError{Code: code, Payload: payload, Err: err}
}

func (s *fetchService) audit(ctx context.Context, resp toolResponse, err error) {
	core.DispatchAudit(ctx, s.dispatcher, &events.AuditPayload{
		Action: constants.WebFetchToolName, Tool: constants.WebFetchToolName, Outcome: core.Outcome(err),
		Details: map[string]any{
			"policy_tier": constants.WebSearchPolicyTier, "url_hash": strutil.HashHexTrimmed(resp.URL),
			"status": resp.Status, "truncated": resp.Truncated, "content_type": resp.ContentType,
		},
	}, err)
}

func failureCode(err error) string {
	var blocked *blockedAddrError
	if errors.As(err, &blocked) {
		return "blocked_private_host"
	}
	return "fetch_failed"
}

// checkRedirect caps the chain at constants.WebFetchMaxRedirects hops. Private
// redirect targets need no check here: every hop opens its own connection and
// so passes through guardDial.
func (s *fetchService) checkRedirect(_ *http.Request, via []*http.Request) error {
	if len(via) > constants.WebFetchMaxRedirects {
		return fmt.Errorf("stopped after %d redirects", constants.WebFetchMaxRedirects)
	}
	return nil
}

// blockedHost is a cheap pre-flight check that turns the obvious cases into a
// clear error before any socket is opened. It is NOT the security boundary —
// guardDial is, because only the dial sees where a name actually resolves.
func (s *fetchService) blockedHost(raw string) string {
	if s.cfg.AllowPrivateHosts {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	host := strings.ToLower(parsed.Hostname())
	ip, ipErr := netip.ParseAddr(host)
	private := host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal")
	if (ipErr == nil && s.denies(ip)) || (ipErr != nil && private) {
		return host
	}
	return ""
}

// extraPrivateNets are ranges netip's classifiers do not cover: CGNAT and the
// benchmarking range, both routable-looking but never legitimate fetch targets.
var extraPrivateNets = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("198.18.0.0/15")}

// blockedAddr reports whether a resolved address must never be connected to.
func blockedAddr(ip netip.Addr) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() ||
		slices.ContainsFunc(extraPrivateNets, func(p netip.Prefix) bool { return p.Contains(ip) })
}

// render caps the body at maxBytes, converts HTML to readable text, and appends
// an explicit truncation marker when the cap was hit.
func render(body, contentType string, maxBytes int) (string, bool) {
	truncated := len(body) > maxBytes
	if truncated {
		body = strutil.TruncateUTF8(body, maxBytes)
	}
	if strings.Contains(strings.ToLower(contentType), "html") {
		body = htmlToText(body)
	}
	out := strings.TrimSpace(body)
	if truncated {
		out += fmt.Sprintf("\n\n[truncated: response exceeded %d bytes; the rest was not fetched — if the answer may be further down, call web_fetch again with a larger max_bytes or omit it for the %d-byte default]", maxBytes, constants.WebFetchDefaultMaxBytes)
	}
	return out, truncated
}

var (
	// RE2 has no backreferences, so the close tag is an alternation too; the
	// non-greedy body still stops at the first close tag of any dropped kind.
	dropElements = regexp.MustCompile(`(?is)<(?:script|style|noscript|svg|template|head)\b[^>]*>.*?</(?:script|style|noscript|svg|template|head)\s*>`)
	// The byte cap can cut the body inside one of those elements, leaving it
	// unterminated; without this the whole stylesheet or script would survive
	// dropElements and reach the model as page text.
	dropUnterminated = regexp.MustCompile(`(?is)<(?:script|style|noscript|svg|template|head)\b[^>]*>.*\z`)
	htmlComments     = regexp.MustCompile(`(?s)<!--.*?-->`)
	// mainContent prefers the article body when the page marks one and it is
	// not cut off by the byte cap.
	mainContent = regexp.MustCompile(`(?is)<(main|article)\b[^>]*>(.*)</(?:main|article)\s*>`)
	// cssNoise matches a line that is only style/script punctuation and
	// identifiers with no spaces — leftover minified CSS or JS, never prose.
	cssNoise      = regexp.MustCompile(`^[^\s]*[{};][^\s]*$`)
	headingOpen   = regexp.MustCompile(`(?i)<h([1-6])\b[^>]*>`)
	blockBoundary = regexp.MustCompile(`(?i)<(?:br|/p|/div|/li|/tr|/h[1-6]|/table)\b[^>]*>`)
	anyTag        = regexp.MustCompile(`(?s)<[^>]*>`)
	// Every Unicode space (incl. &nbsp;), matching the trimming the per-line
	// TrimSpace this replaced used to do at line edges.
	spaceRuns  = regexp.MustCompile(`[\p{Z}\t\r\v\f\x{0085}]+`)
	lineEdges  = regexp.MustCompile(` ?\n ?`)
	blankLines = regexp.MustCompile(`\n{3,}`)
)

// htmlToText converts markup to readable, lightly markdown-flavored text:
// script/style content is dropped, headings become "# " lines, block ends
// become newlines, remaining tags are stripped and entities unescaped.
func htmlToText(in string) string {
	stripped := dropUnterminated.ReplaceAllString(dropElements.ReplaceAllString(htmlComments.ReplaceAllString(in, "\n"), "\n"), "\n")
	if m := mainContent.FindStringSubmatch(stripped); m != nil {
		stripped = m[2]
	}
	out := headingOpen.ReplaceAllStringFunc(stripped, func(tag string) string {
		return "\n" + strings.Repeat("#", int(headingOpen.FindStringSubmatch(tag)[1][0]-'0')) + " "
	})
	out = anyTag.ReplaceAllString(blockBoundary.ReplaceAllString(out, "\n"), "")
	out = lineEdges.ReplaceAllString(spaceRuns.ReplaceAllString(html.UnescapeString(out), " "), "\n")
	return strings.TrimSpace(blankLines.ReplaceAllString(dropCSSNoise(out), "\n\n"))
}

// dropCSSNoise removes lines that are only minified style or script tokens,
// which survive when markup nests them in a way the element regexes miss.
func dropCSSNoise(in string) string {
	return strings.Join(slices.DeleteFunc(strings.Split(in, "\n"), cssNoise.MatchString), "\n")
}
