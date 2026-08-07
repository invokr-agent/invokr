package toolbox

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/tools/core"
)

type resolvedPackage struct {
	Name, Version, URL, SHA256 string
	AgeDays                    int
}

type pipReport struct {
	Install []struct {
		Metadata     struct{ Name, Version string }
		DownloadInfo struct {
			URL         string `json:"url"`
			ArchiveInfo struct {
				Hashes map[string]string `json:"hashes"`
			} `json:"archive_info"`
		} `json:"download_info"`
	} `json:"install"`
}

func (p *executableTool) secureInstallPythonRequirements(ctx context.Context, pythonBin, reqPath, venvDir string, env []string) ([]string, error) {
	if p.approver == nil {
		return nil, fmt.Errorf("dependency installation requires an approver")
	}
	notice(ctx, p.dispatcher, "Preparing Python dependencies for "+p.Name)
	proxy, err := startPackageProxy(p.config.PackageIndexHosts, p.config.PackageArtifactHosts)
	if err != nil {
		return nil, fmt.Errorf("start package proxy: %w", err)
	}
	defer proxy.Close()
	pip := func(env []string, args ...string) error {
		return runSandboxedPip(ctx, p.config, pythonBin, args, filepath.Dir(reqPath), venvDir, env)
	}
	proxied := proxyEnv(env, proxy.URL())
	reportPath := filepath.Join(venvDir, ".pip-report.json")
	defer func() { _ = os.Remove(reportPath) }()
	notice(ctx, p.dispatcher, "Resolving Python dependencies for "+p.Name)
	if err := pip(proxied, "-m", "pip", "install", "--dry-run", "--ignore-installed", "--report", reportPath, "--only-binary=:all:", "--index-url", p.config.PackageIndexURL, "-r", reqPath); err != nil {
		return nil, fmt.Errorf("resolve requirements: %w", err)
	}
	packages, err := validatePipReport(ctx, reportPath, p.config, proxy.Client())
	if err != nil {
		return nil, fmt.Errorf("requirements rejected: %w", err)
	}
	err = core.EnforceApproval(ctx, p.approver, p.ToolSpec, core.Ask(approval.Request{Tool: p.Name, Subject: "install " + p.dir, Writes: true, Protected: true, Network: true, Tier: "dangerous", Reason: "install resolved Python dependencies", Summary: "Install Python dependencies for " + p.Name, Details: packageApprovalDetails(packages)}))
	if err != nil {
		return nil, err
	}
	lockPath := filepath.Join(filepath.Dir(reqPath), "requirements.lock")
	if err := os.WriteFile(lockPath, []byte(renderRequirementsLock(packages)), 0600); err != nil {
		return nil, fmt.Errorf("write requirements.lock: %w", err)
	}
	wheels, err := os.MkdirTemp(venvDir, ".wheels-")
	if err != nil {
		return nil, fmt.Errorf("create wheel cache: %w", err)
	}
	defer func() { _ = os.RemoveAll(wheels) }()
	notice(ctx, p.dispatcher, "Downloading verified Python dependencies for "+p.Name)
	if err := pip(proxied, "-m", "pip", "download", "--only-binary=:all:", "--require-hashes", "--index-url", p.config.PackageIndexURL, "-r", lockPath, "-d", wheels); err != nil {
		return nil, fmt.Errorf("download requirements: %w", err)
	}
	notice(ctx, p.dispatcher, "Installing verified Python dependencies for "+p.Name)
	if err := pip(env, "-m", "pip", "install", "--no-index", "--only-binary=:all:", "--require-hashes", "--find-links", wheels, "-r", lockPath); err != nil {
		return nil, fmt.Errorf("install requirements: %w", err)
	}
	return core.MapSlice(packages, func(p resolvedPackage) string { return canonicalPackageName(p.Name) }), nil
}

func runSandboxedPip(ctx context.Context, cfg config.ToolboxConfig, pythonBin string, args []string, roDir, venvDir string, env []string) error {
	cmd, cleanup, err := pythonSandboxCommand(ctx, cfg, pythonBin, args, roDir, venvDir)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	cmd.Env = env
	out := core.NewTruncatingWriters(cfg.MaxOutputBytes)
	cmd.Stdout, cmd.Stderr = out.Stdout(), out.Stderr()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", joinNonEmpty([]string{out.StdoutString(), out.StderrString()}, "\n"), err)
	}
	return nil
}

func validatePipReport(ctx context.Context, path string, cfg config.ToolboxConfig, client *http.Client) ([]resolvedPackage, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var report pipReport
	if err := json.Unmarshal(b, &report); err != nil {
		return nil, fmt.Errorf("parse pip report: %w", err)
	}
	if len(report.Install) == 0 {
		return nil, fmt.Errorf("pip report contained no packages")
	}
	var names []string
	for _, item := range report.Install {
		names = append(names, item.Metadata.Name)
	}
	if err := enforcePackagePolicy(names, cfg.PackageAllow, cfg.PackageDeny); err != nil {
		return nil, err
	}
	var out []resolvedPackage
	for _, item := range report.Install {
		p, err := validateResolvedPackage(ctx, client, cfg, item.Metadata.Name, item.Metadata.Version, item.DownloadInfo.URL, item.DownloadInfo.ArchiveInfo.Hashes["sha256"])
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b resolvedPackage) int { return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out, nil
}

func validateResolvedPackage(ctx context.Context, client *http.Client, cfg config.ToolboxConfig, name, version, artifactURL, hash string) (resolvedPackage, error) {
	p := resolvedPackage{Name: name, Version: version, URL: artifactURL, SHA256: hash}
	if !pinnedRequirementRe.MatchString(name+"=="+version) || artifactURL == "" || hash == "" {
		return p, fmt.Errorf("pip report package lacks exact name, version, URL, or SHA-256")
	}
	u, err := url.Parse(artifactURL)
	if err != nil || !hostAllowed(u.Hostname(), cfg.PackageArtifactHosts) {
		return p, fmt.Errorf("artifact host is not allowed for %s", name)
	}
	p.AgeDays, err = releaseAge(ctx, client, cfg, name, version, artifactURL)
	if err != nil {
		return p, err
	}
	if p.AgeDays < cfg.ReleaseCooldownDays {
		return p, fmt.Errorf("%s==%s is %d days old; minimum is %d", name, version, p.AgeDays, cfg.ReleaseCooldownDays)
	}
	return p, nil
}

func releaseAge(ctx context.Context, client *http.Client, cfg config.ToolboxConfig, name, version, artifactURL string) (int, error) {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(cfg.PackageMetadataURL, url.PathEscape(name)), nil)
	if e != nil {
		return 0, e
	}
	resp, e := client.Do(req)
	if e != nil {
		return 0, e
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("metadata for %s returned %d", name, resp.StatusCode)
	}
	var m struct {
		Releases map[string][]releaseFile `json:"releases"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&m); e != nil {
		return 0, e
	}
	files := m.Releases[version]
	if len(files) == 0 {
		return 0, fmt.Errorf("metadata lacks release %s==%s", name, version)
	}
	upload := selectUploadTime(files, artifactFilename(artifactURL))
	if upload.IsZero() {
		return 0, fmt.Errorf("metadata has no parseable upload time for %s==%s", name, version)
	}
	if upload.After(time.Now()) {
		return 0, fmt.Errorf("release %s==%s is future-dated", name, version)
	}
	return int(time.Since(upload).Hours() / 24), nil
}

// selectUploadTime returns the upload time of the wanted artifact filename, or
// the oldest release upload time when no filename matches. Zero if none parse.
type releaseFile struct {
	Upload   string `json:"upload_time_iso_8601"`
	Filename string `json:"filename"`
}

func selectUploadTime(files []releaseFile, wanted string) time.Time {
	var oldest, matched time.Time
	for _, f := range files {
		t, e := time.Parse(time.RFC3339Nano, f.Upload)
		if e != nil {
			continue
		}
		if oldest.IsZero() || t.Before(oldest) {
			oldest = t
		}
		if wanted != "" && f.Filename == wanted {
			matched = t
		}
	}
	if !matched.IsZero() {
		return matched
	}
	return oldest
}

func artifactFilename(artifactURL string) string {
	u, err := url.Parse(artifactURL)
	if err != nil {
		return ""
	}
	return path.Base(u.Path)
}
func renderRequirementsLock(ps []resolvedPackage) string {
	var b strings.Builder
	for _, p := range ps {
		fmt.Fprintf(&b, "%s==%s --hash=sha256:%s\n", p.Name, p.Version, p.SHA256)
	}
	return b.String()
}
func packageApprovalDetails(ps []resolvedPackage) string {
	return strings.TrimSpace(strings.Join(core.MapSlice(ps, func(p resolvedPackage) string {
		return fmt.Sprintf("%s==%s (%d days old) sha256:%s", p.Name, p.Version, p.AgeDays, p.SHA256)
	}), "\n"))
}

func notice(ctx context.Context, d events.Dispatcher, text string) {
	events.Notice(ctx, d, "", "", text)
}
func proxyEnv(env []string, proxy string) []string {
	for key, value := range map[string]string{"HTTP_PROXY": proxy, "HTTPS_PROXY": proxy, "http_proxy": proxy, "https_proxy": proxy, "NO_PROXY": "", "no_proxy": ""} {
		env = core.WithEnvValue(env, key, value)
	}
	return env
}

func hostAllowed(host string, allowed []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return slices.ContainsFunc(allowed, func(candidate string) bool { return host == strings.ToLower(strings.TrimSpace(candidate)) })
}

type packageProxy struct {
	listener net.Listener
	server   *http.Server
	allowed  map[string]bool
	wg       sync.WaitGroup
}

func startPackageProxy(index, artifacts []string) (*packageProxy, error) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return nil, e
	}
	p := &packageProxy{listener: l, allowed: map[string]bool{}}
	for _, h := range append(append([]string{}, index...), artifacts...) {
		p.allowed[strings.ToLower(strings.TrimSpace(h))] = true
	}
	// ReadTimeout/WriteTimeout are intentionally left unset: package downloads
	// proxied here can be large/slow, and CONNECT tunnels hijack the conn anyway
	// (Go's server timeouts stop applying post-hijack). ReadHeaderTimeout alone
	// closes the slowloris gap on the header-read phase.
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	p.wg.Add(1)
	go func() { defer p.wg.Done(); _ = p.server.Serve(l) }()
	return p, nil
}
func (p *packageProxy) URL() string { return "http://" + p.listener.Addr().String() }
func (p *packageProxy) Close()      { _ = p.server.Close(); p.wg.Wait() }
func (p *packageProxy) Client() *http.Client {
	return &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: p.listener.Addr().String()})}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if !p.allowed[strings.ToLower(req.URL.Hostname())] {
			return fmt.Errorf("redirect host %q is not allowed", req.URL.Hostname())
		}
		return nil
	}}
}
func (p *packageProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, port := proxyDestination(r)
	if !p.allowed[strings.ToLower(host)] {
		http.Error(w, "package host denied", http.StatusForbidden)
		return
	}
	if port != "" && port != "80" && port != "443" {
		http.Error(w, "package port denied", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Authorization")
	resp, e := http.DefaultTransport.RoundTrip(out)
	if e != nil {
		http.Error(w, e.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	maps.Copy(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func proxyDestination(r *http.Request) (string, string) {
	if r.Method != http.MethodConnect {
		return r.URL.Hostname(), r.URL.Port()
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		return strings.Trim(r.Host, "[]"), "443"
	}
	return host, port
}
func (p *packageProxy) connect(w http.ResponseWriter, r *http.Request) {
	dst, e := net.DialTimeout("tcp", r.Host, 10*time.Second)
	if e != nil {
		http.Error(w, e.Error(), http.StatusBadGateway)
		return
	}
	h, ok := w.(http.Hijacker)
	if !ok {
		_ = dst.Close()
		http.Error(w, "CONNECT unsupported", http.StatusInternalServerError)
		return
	}
	client, rw, e := h.Hijack()
	if e != nil {
		_ = dst.Close()
		return
	}
	_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	_ = rw.Flush()
	_, _ = io.CopyN(dst, rw, int64(rw.Reader.Buffered()))
	go func() { _, _ = io.Copy(dst, client); _ = dst.Close() }()
	go func() { _, _ = io.Copy(client, dst); _ = client.Close() }()
}
