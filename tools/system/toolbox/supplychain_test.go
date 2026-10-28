package toolbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestPackageProxyRejectsUnlistedHost(t *testing.T) {
	p, err := startPackageProxy([]string{"pypi.org"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	req, _ := http.NewRequest(http.MethodGet, "http://denied.example/simple", nil)
	resp, err := p.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestSupplyChainSystemValidatesThroughRestrictedProxy(t *testing.T) {
	uploaded := time.Now().Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano)
	metadata := fmt.Sprintf(`{"releases":{"1.2.3":[{"upload_time_iso_8601":%q}]}}`, uploaded)
	calls := 0
	previousTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Hostname() != "pypi.org" || req.URL.Path != "/pypi/demo/json" {
			t.Fatalf("unexpected upstream request: %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(metadata)), Request: req}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	proxy, err := startPackageProxy([]string{"pypi.org"}, []string{"files.pythonhosted.org"})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	reportPath := filepath.Join(t.TempDir(), "report.json")
	report := `{"install":[{"metadata":{"name":"demo","version":"1.2.3"},"download_info":{"url":"https://files.pythonhosted.org/demo.whl","archive_info":{"hashes":{"sha256":"abc123"}}}}]}`
	if err := os.WriteFile(reportPath, []byte(report), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.ToolboxConfig{
		PackageMetadataURL:   "http://pypi.org/pypi/%s/json",
		PackageArtifactHosts: []string{"files.pythonhosted.org"},
		ReleaseCooldownDays:  14,
	}
	packages, err := validatePipReport(context.Background(), reportPath, cfg, proxy.Client())
	if err != nil {
		t.Fatal(err)
	}
	if got := renderRequirementsLock(packages); got != "demo==1.2.3 --hash=sha256:abc123\n" {
		t.Fatalf("lock = %q", got)
	}

	resp, err := proxy.Client().Get("http://evil.example/simple")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden || calls != 1 {
		t.Fatalf("denied request status=%d upstream calls=%d", resp.StatusCode, calls)
	}
}

func TestSecureInstallRequiresApproverBeforeSetup(t *testing.T) {
	p := &executableTool{}
	_, err := p.secureInstallPythonRequirements(context.Background(), "missing-python", "missing-requirements", t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "requires an approver") {
		t.Fatalf("error = %v, want approver rejection", err)
	}
}

func TestValidatePipReportChecksLockAndCooldown(t *testing.T) {
	uploaded := time.Now().Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano)
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, fmt.Sprintf(`{"releases":{"1.2.3":[{"upload_time_iso_8601":%q}]}}`, uploaded))
	}))
	defer metadata.Close()
	path := filepath.Join(t.TempDir(), "report.json")
	report := `{"install":[{"metadata":{"name":"demo","version":"1.2.3"},"download_info":{"url":"https://files.example/demo.whl","archive_info":{"hashes":{"sha256":"abc123"}}}}]}`
	if err := os.WriteFile(path, []byte(report), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.ToolboxConfig{PackageMetadataURL: metadata.URL + "/%s", PackageArtifactHosts: []string{"files.example"}, ReleaseCooldownDays: 14}
	got, err := validatePipReport(context.Background(), path, cfg, metadata.Client())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].AgeDays < 29 || !strings.Contains(renderRequirementsLock(got), "--hash=sha256:abc123") {
		t.Fatalf("unexpected lock data: %#v", got)
	}
	cfg.ReleaseCooldownDays = 40
	if _, err := validatePipReport(context.Background(), path, cfg, metadata.Client()); err == nil {
		t.Fatal("expected cooldown rejection")
	}
}

func TestValidatePipReportRejectsUnsafeArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	for _, report := range []string{
		`{"install":[{"metadata":{"name":"demo","version":"1"},"download_info":{"url":"https://evil.example/x.whl","archive_info":{"hashes":{"sha256":"abc"}}}}]}`,
		`{"install":[{"metadata":{"name":"demo","version":"1"},"download_info":{"url":"https://files.example/x.whl","archive_info":{"hashes":{}}}}]}`,
	} {
		if err := os.WriteFile(path, []byte(report), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := validatePipReport(context.Background(), path, config.ToolboxConfig{PackageArtifactHosts: []string{"files.example"}}, http.DefaultClient); err == nil {
			t.Fatal("expected report rejection")
		}
	}
}

func TestResolvedDependencyPolicyRejectsBeforeMetadataOrInstall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	report := `{"install":[{"metadata":{"name":"direct","version":"1"}},{"metadata":{"name":"Transitive_Pkg","version":"1"}}]}`
	if err := os.WriteFile(path, []byte(report), 0600); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("policy rejection must precede metadata requests")
		return nil, nil
	})}
	for _, cfg := range []config.ToolboxConfig{
		{PackageDeny: []string{"transitive-pkg"}},
		{PackageAllow: []string{"direct"}},
	} {
		_, err := validatePipReport(context.Background(), path, cfg, client)
		if err == nil || !strings.Contains(err.Error(), `package "transitive-pkg"`) {
			t.Fatalf("expected transitive policy rejection, got %v", err)
		}
	}
}

func TestCachedRequirementsRecheckCompletePackagePolicy(t *testing.T) {
	dir := t.TempDir()
	reqPath := filepath.Join(dir, "requirements.txt")
	if err := os.WriteFile(reqPath, []byte("direct==1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.ToolboxConfig{PackageAllow: []string{"direct", "transitive-pkg"}}
	_, _, hash, err := readValidatedRequirements(reqPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	marker, _ := json.Marshal(requirementsCache{Hash: hash, Packages: []string{"direct", "Transitive_Pkg"}})
	markerPath := requirementsMarkerPath(dir, reqPath, cfg)
	if err := os.WriteFile(markerPath, marker, 0600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, "requirements.lock")
	if err := os.WriteFile(lockPath, []byte("direct==1 --hash=sha256:abc\nTransitive_Pkg==1 --hash=sha256:def\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &executableTool{config: cfg}
	check := func() error {
		return p.installPythonRequirementsIfNeeded(context.Background(), "must-not-execute", reqPath, dir, nil)
	}
	if err := check(); err != nil {
		t.Fatalf("authorized cached graph: %v", err)
	}
	p.config.PackageDeny = []string{"transitive.pkg"}
	// The model-visible lock is not authority for the installed environment.
	if err := os.WriteFile(lockPath, []byte("# comment --hash=sha256:x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := check(); err == nil || !strings.Contains(err.Error(), "denied by policy") {
		t.Fatalf("cached denied transitive: %v", err)
	}
	p.config.PackageDeny = nil
	p.config.PackageAllow = []string{"direct"}
	if err := check(); err == nil || !strings.Contains(err.Error(), "not in the allowlist") {
		t.Fatalf("cached unlisted transitive: %v", err)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := check(); err == nil {
		t.Fatal("missing lock bypassed recorded dependency policy")
	}
	p.config.PackageAllow = nil
	marker, _ = json.Marshal(requirementsCache{Hash: hash})
	if err := os.WriteFile(markerPath, marker, 0600); err != nil {
		t.Fatal(err)
	}
	if err := check(); err == nil || !strings.Contains(err.Error(), "lacks dependency graph") {
		t.Fatalf("missing cached graph: %v", err)
	}
}

func TestPackagePolicyRejectsSharedEnvironment(t *testing.T) {
	reqPath := filepath.Join(t.TempDir(), "requirements.txt")
	if err := os.WriteFile(reqPath, []byte("direct==1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []config.ToolboxConfig{
		{VenvDir: t.TempDir(), PackageAllow: []string{"direct"}},
		{VenvDir: t.TempDir(), PackageDeny: []string{"transitive"}},
	} {
		p := &executableTool{config: cfg}
		err := p.installPythonRequirementsIfNeeded(context.Background(), "must-not-execute", reqPath, cfg.VenvDir, nil)
		if err == nil || !strings.Contains(err.Error(), "requires per-tool environments") {
			t.Fatalf("shared stale graph accepted: %v", err)
		}
	}
}

func TestLegacyAndMalformedCacheRequireResolution(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".requirements.sha256")
	for _, data := range []string{"old-requirements-hash", `{"Hash":`, `null`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		cache, rebuild, err := readRequirementsCache(path)
		if err != nil || !rebuild || cache.Hash != "" {
			t.Fatalf("legacy/corrupt cache reused: %#v, rebuild=%v, err=%v", cache, rebuild, err)
		}
	}
}

func TestEmptyRequirementsClearsOldGraph(t *testing.T) {
	dir := t.TempDir()
	cfg := config.ToolboxConfig{VenvDir: dir}
	reqPath := filepath.Join(dir, "requirements.txt")
	if err := os.WriteFile(reqPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	markerPath := requirementsMarkerPath(dir, reqPath, cfg)
	if err := os.WriteFile(markerPath, []byte(`{"Hash":"obsolete","Packages":["removed"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	p := &executableTool{config: cfg}
	if err := p.installPythonRequirementsIfNeeded(context.Background(), "must-not-execute", reqPath, dir, nil); err != nil {
		t.Fatal(err)
	}
	cache, _, err := readRequirementsCache(markerPath)
	if err != nil || len(cache.Packages) != 0 {
		t.Fatalf("obsolete graph retained: %#v, %v", cache, err)
	}
}

func TestReleaseAgeRejectsUnparseableUploadTime(t *testing.T) {
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"releases":{"1.2.3":[{"upload_time_iso_8601":"invalid"}]}}`)
	}))
	defer metadata.Close()
	_, err := releaseAge(context.Background(), metadata.Client(), config.ToolboxConfig{PackageMetadataURL: metadata.URL + "/%s"}, "demo", "1.2.3", "")
	if err == nil || !strings.Contains(err.Error(), "no parseable upload time") {
		t.Fatalf("error = %v, want parseable upload time rejection", err)
	}
}

func TestReleaseAgeUsesDownloadedArtifactUploadTime(t *testing.T) {
	old := time.Now().AddDate(0, 0, -365).Format(time.RFC3339Nano)
	fresh := time.Now().AddDate(0, 0, -1).Format(time.RFC3339Nano)
	body := fmt.Sprintf(`{"releases":{"1.2.3":[`+
		`{"upload_time_iso_8601":%q,"filename":"demo-1.2.3-old.whl"},`+
		`{"upload_time_iso_8601":%q,"filename":"demo-1.2.3-fresh.whl"}]}}`, old, fresh)
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer metadata.Close()
	age, err := releaseAge(context.Background(), metadata.Client(), config.ToolboxConfig{PackageMetadataURL: metadata.URL + "/%s"}, "demo", "1.2.3", "https://files.example/packages/demo-1.2.3-fresh.whl")
	if err != nil {
		t.Fatalf("releaseAge: %v", err)
	}
	if age > 2 {
		t.Fatalf("age = %d days, want age of the freshly uploaded artifact (~1 day)", age)
	}
}

func TestProxyDestinationHandlesIPv6AndPorts(t *testing.T) {
	request := httptest.NewRequest(http.MethodConnect, "http://[::1]:443", nil)
	request.Host = "[::1]:443"
	host, port := proxyDestination(request)
	if host != "::1" || port != "443" {
		t.Fatalf("destination = %q:%q, want ::1:443", host, port)
	}
	request = httptest.NewRequest(http.MethodGet, "http://pypi.org:8080/simple", nil)
	_, port = proxyDestination(request)
	if port != "8080" {
		t.Fatalf("port = %q, want 8080", port)
	}
	proxy := &packageProxy{allowed: map[string]bool{"pypi.org": true}}
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

// The approval details trim surrounding whitespace, so a name or hash that
// arrives padded from the index does not pad the prompt the human reads.
func TestPackageApprovalDetailsTrimsSurroundingWhitespace(t *testing.T) {
	got := packageApprovalDetails([]resolvedPackage{{Name: " lead", Version: "1", AgeDays: 1, SHA256: "cc "}})
	if want := "lead==1 (1 days old) sha256:cc"; got != want {
		t.Fatalf("details = %q, want %q", got, want)
	}
	if got := packageApprovalDetails(nil); got != "" {
		t.Fatalf("empty details = %q, want empty", got)
	}
}
