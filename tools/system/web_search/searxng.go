package web_search

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/support/strutil"
)

// searxngSettings enables the JSON API (off by default) and turns off the bot
// limiter, which otherwise blocks a headless client. Everything else is the
// image default, except that bing/yandex/seznam are explicitly enabled: the
// default general-category engines (brave, duckduckgo, google, startpage,
// qwant) are all currently CAPTCHA/rate-limit suspended from this host, so a
// plain default query returns zero results; these three still answer.
const searxngSettings = `use_default_settings: true
server:
  secret_key: %q
  limiter: false
search:
  formats: [html, json]
engines:
  - name: bing
    disabled: false
  - name: yandex
    disabled: false
  - name: seznam
    disabled: false
`

// searxngEngines is the engine set the first attempt asks for; the engines the
// settings template enables explicitly, because the defaults are usually
// CAPTCHA/rate-limit suspended from this host.
const searxngEngines = "bing,yandex,seznam"

// searxngFallbackEngines is asked for on the one retry: throttling hits the
// engines, not the instance, so a different set often answers straight away.
const searxngFallbackEngines = "duckduckgo,brave,startpage,google"

// searxngRetryDelay is how long the retry waits for the throttle to lift.
const searxngRetryDelay = 2 * time.Second

// NewSearXNGProvider queries a SearXNG instance. With cfg.Endpoint empty the
// provider starts a local container on first use and reuses it after that.
func NewSearXNGProvider(cfg config.SearXNGSearchConfig, cacheDir string, client *http.Client) *jsonSearchProvider {
	endpoint := strutil.FirstNonEmptyTrimmed(cfg.Endpoint, fmt.Sprintf("http://127.0.0.1:%d/search", cfg.Port))
	var mu sync.Mutex
	started := false
	ensure := func(ctx context.Context, endpoint string) error {
		if strings.TrimSpace(cfg.Endpoint) != "" {
			return nil
		}
		// A failed start is retried on the next search rather than cached: the
		// first attempt can lose to a cancelled ctx, a daemon that is briefly
		// down, or a cold image pull past the readiness deadline, and the
		// container is usually up by then.
		mu.Lock()
		defer mu.Unlock()
		if started {
			return nil
		}
		if err := ensureSearXNG(ctx, cfg, cacheDir, endpoint); err != nil {
			return err
		}
		started = true
		return nil
	}
	return &jsonSearchProvider{name: "searxng", endpoint: endpoint, client: cmp.Or(client, http.DefaultClient), now: time.Now, build: searxngBuilder(ensure, searxngEngines), retryBuild: searxngBuilder(ensure, searxngFallbackEngines), retryDelay: searxngRetryDelay, decode: searxngDecoder()}
}

func searxngBuilder(ensure func(context.Context, string) error, engines string) func(context.Context, string, string, SearchRequest) (*http.Request, error) {
	return func(ctx context.Context, endpoint, _ string, req SearchRequest) (*http.Request, error) {
		if err := ensure(ctx, endpoint); err != nil {
			return nil, err
		}
		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("parse searxng endpoint: %w", err)
		}
		q := u.Query()
		q.Set("q", braveQuery(req))
		q.Set("format", "json")
		q.Set("engines", engines)
		if req.RecencyDays > 0 {
			q.Set("time_range", searxngTimeRange(req.RecencyDays))
		}
		u.RawQuery = q.Encode()
		return newProviderRequest(ctx, http.MethodGet, u.String(), nil, map[string]string{"Accept": "application/json"})
	}
}

// searxngDecoder wraps the mapped decoder so a searxng whose engines are all
// suspended (rate-limited, captcha) fails loudly instead of returning an empty
// result set that reads as "no matches" and sends the model into a retry loop.
func searxngDecoder() func([]byte) ([]providerItem, error) {
	inner := mappedDecoder("searxng", map[string]string{"results": "$.results", "title": "$.title", "url": "$.url", "snippet": "$.content", "published_at": "$.publishedDate", "score": "$.score"})
	return func(body []byte) ([]providerItem, error) {
		items, err := inner(body)
		if err != nil || len(items) > 0 {
			return items, err
		}
		var suspended []string
		gjson.GetBytes(body, "unresponsive_engines").ForEach(func(_, v gjson.Result) bool {
			suspended = append(suspended, v.Get("0").String()+": "+v.Get("1").String())
			return true
		})
		if len(suspended) == 0 {
			return items, nil
		}
		return nil, fmt.Errorf("searxng returned no results and reports unresponsive engines (%s); this is a transient throttle, not a bad query or a dead web — the same search usually works again shortly, so try it again with different keywords or a narrower phrasing", strings.Join(suspended, "; "))
	}
}

func searxngTimeRange(days int) string { return recencyBucket(days, "day", "week", "month", "year") }

// ensureSearXNG makes the named container run: reuse it if running, start it
// if stopped, otherwise create it. Then wait until the JSON API answers.
// ponytail: the container is never stopped; add a stop command when someone asks.
func ensureSearXNG(ctx context.Context, cfg config.SearXNGSearchConfig, cacheDir, endpoint string) error {
	engine := searxngEngine(cfg)
	if _, err := exec.LookPath(engine); err != nil {
		return fmt.Errorf("searxng: container engine %q not found: %w", engine, err)
	}
	state, _ := exec.CommandContext(ctx, engine, "inspect", "-f", "{{.State.Running}}", constants.SearXNGContainerName).Output() //nolint:gosec
	switch strings.TrimSpace(string(state)) {
	case "true":
	case "false":
		if out, err := exec.CommandContext(ctx, engine, "start", constants.SearXNGContainerName).CombinedOutput(); err == nil { //nolint:gosec
			break
		} else if !strings.Contains(string(out), "mount") {
			return fmt.Errorf("searxng: start container: %w: %s", err, strings.TrimSpace(string(out)))
		}
		// A stale container whose settings bind mount no longer exists (an
		// old temp dir) can never start again; drop it and create a fresh one.
		_ = exec.CommandContext(ctx, engine, "rm", "-f", constants.SearXNGContainerName).Run() //nolint:gosec
		fallthrough
	default:
		settings, err := writeSearXNGSettings(filepath.Join(filepath.Dir(cacheDir), "searxng", "settings.yml"))
		if err != nil {
			return err
		}
		argv := searxngRunArgv(cfg, settings)
		if out, err := exec.CommandContext(ctx, engine, argv...).CombinedOutput(); err != nil { //nolint:gosec
			return fmt.Errorf("searxng: run container: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return waitForSearXNG(ctx, endpoint)
}

// searxngEngine is the container binary the provider shells out to; the
// registration gate and the start path must agree on it.
func searxngEngine(cfg config.SearXNGSearchConfig) string {
	return strutil.FirstNonEmptyTrimmed(cfg.Engine, constants.SearXNGDefaultEngine)
}

func searxngRunArgv(cfg config.SearXNGSearchConfig, settingsPath string) []string {
	return []string{"run", "-d", "--name", constants.SearXNGContainerName, "-p", "127.0.0.1:" + strconv.Itoa(cfg.Port) + ":" + strconv.Itoa(constants.SearXNGContainerPort), "-v", settingsPath + ":/etc/searxng/settings.yml:ro", cfg.Image}
}

func writeSearXNGSettings(path string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("searxng: settings dir: %w", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("searxng: secret: %w", err)
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf(searxngSettings, hex.EncodeToString(secret))), 0o600); err != nil {
		return "", fmt.Errorf("searxng: write settings: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("searxng: settings path: %w", err)
	}
	return abs, nil
}

// waitForSearXNG polls the JSON endpoint until it answers 200; a fresh
// container takes a few seconds, a cold image pull considerably longer.
func waitForSearXNG(ctx context.Context, endpoint string) error {
	deadline := time.Now().Add(90 * time.Second)
	probe := endpoint + "?q=ping&format=json"
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, probe, nil)
		if err != nil {
			return fmt.Errorf("searxng: probe %s: %w", probe, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("searxng: %s did not become ready", endpoint)
		}
		time.Sleep(time.Second)
	}
}
