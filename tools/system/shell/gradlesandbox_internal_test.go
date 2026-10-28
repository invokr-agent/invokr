package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/tools/core/sandbox"
)

// gradleFixtureEnv names a Kotlin/Gradle project to build for real inside the
// sandbox. The check is opt-in because it needs a JDK, a `gradle` on PATH and a
// warm gradle home, none of which CI has — but it is the only check that can
// see the failure #548's first attempt shipped: that attempt passed
// `gradle --version`, and a compiling build passes under it too, because
// neither reads anything at the top level of the gradle home.
const gradleFixtureEnv = "INVOKR_GRADLE_FIXTURE"

// runInSandbox runs one bash line through the same plan the shell tool builds
// for a JVM command, and returns its combined output.
func runInSandbox(t *testing.T, dir string, line string) (string, error) {
	t.Helper()
	bash, err := bashPath()
	if err != nil {
		t.Fatal(err)
	}
	plan := preparedShellCommand{workspaceRoot: dir, cwd: dir, line: line, commands: []string{"gradle"}}
	sb := sandboxPlan(bash, plan)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd, cleanup, err := sandbox.Command(ctx, sb, dir, []string{"-c", line})
	if err != nil {
		t.Fatalf("sandbox.Command: %v", err)
	}
	defer cleanup()
	cmd.Dir, cmd.Env = dir, sandboxEnv(plan, sb)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// gradleFixture skips unless this host can run the real thing.
func gradleFixture(t *testing.T) string {
	t.Helper()
	fixture := strings.TrimSpace(os.Getenv(gradleFixtureEnv))
	if fixture == "" {
		t.Skipf("set %s to a gradle project to run this", gradleFixtureEnv)
	}
	if !sandbox.Available() {
		t.Skip("namespace sandbox unavailable on this host")
	}
	if _, err := exec.LookPath("gradle"); err != nil {
		t.Skipf("gradle not on PATH: %v", err)
	}
	return fixture
}

// seedGradleHomeFile writes a file at the top level of the gradle home for the
// duration of the test, skipping rather than clobbering an existing one.
func seedGradleHomeFile(t *testing.T, name string, content string) string {
	t.Helper()
	path := filepath.Join(gradleUserHome(), name)
	if _, err := os.Stat(path); err == nil {
		t.Skipf("%s already exists; not overwriting the host's own file", path)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}

// The gradle home has to be *visible* inside the sandbox, not just its cache
// subdirectories. The reverted attempt (622e4a8) mounted an enumerated list of
// subdirectories and left the home itself unmounted, so a sandboxed process saw
// `ls: cannot access '~/.gradle': No such file or directory` and every file at
// the home's top level — gradle.properties above all — was gone. That is the
// line the M5 bench log actually shows, and neither `gradle --version` nor a
// compiling build reaches it.
func TestGradleHomeIsVisibleInsideTheSandbox(t *testing.T) {
	dir := gradleFixture(t)
	probe := seedGradleHomeFile(t, ".invokr-548-probe", "visible\n")
	out, err := runInSandbox(t, dir, "ls "+gradleUserHome()+" && cat "+probe)
	if err != nil {
		t.Fatalf("gradle home not readable inside the sandbox: %v\n%s", err, out)
	}
	if !strings.Contains(out, "visible") {
		t.Fatalf("gradle home file not readable inside the sandbox:\n%s", out)
	}
}

// Gradle must actually honour a setting written at the top level of its home:
// reading the directory is not the same as gradle resolving gradle.properties
// from it. org.gradle.logging.level=quiet is the cheapest setting whose effect
// is unambiguous in the build output.
func TestGradleReadsHomeLevelProperties(t *testing.T) {
	dir := gradleFixture(t)
	seedGradleHomeFile(t, "gradle.properties", "org.gradle.logging.level=quiet\n")
	out, err := runInSandbox(t, dir, "gradle --offline :cli:installDist")
	if err != nil {
		t.Fatalf("sandboxed gradle build failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "> Task :") {
		t.Fatalf("gradle did not apply the home-level logging level, so it never read gradle.properties:\n%s", out)
	}
}

// What #548 asked for: the home itself is not writable, so a sandboxed line
// cannot leave a gradle.properties or an init.d/ script behind for every later
// host build — while the caches gradle needs stay writable.
func TestGradleHomeIsReadOnlyButCachesAreWritable(t *testing.T) {
	dir := gradleFixture(t)
	home := gradleUserHome()
	if out, err := runInSandbox(t, dir, "touch "+filepath.Join(home, "init.gradle")); err == nil {
		t.Fatalf("writing to the gradle home succeeded, want it read-only:\n%s", out)
	}
	probe := filepath.Join(home, "caches", ".invokr-548-probe")
	out, err := runInSandbox(t, dir, "touch "+probe+" && rm "+probe)
	if err != nil {
		t.Fatalf("gradle caches not writable inside the sandbox: %v\n%s", err, out)
	}
}

// A real build still has to pass. This is the check that #548's first attempt
// never ran, but on its own it is not enough: it passes under the reverted
// mount too, because compiling reads nothing at the home's top level.
func TestGradleBuildRunsInsideTheSandbox(t *testing.T) {
	dir := gradleFixture(t)
	out, err := runInSandbox(t, dir, "gradle --offline :cli:installDist")
	if err != nil {
		t.Fatalf("sandboxed gradle build failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "BUILD SUCCESSFUL") {
		t.Fatalf("sandboxed gradle build did not report success:\n%s", out)
	}
}
