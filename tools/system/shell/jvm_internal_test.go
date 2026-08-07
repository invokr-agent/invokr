package shell

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The JDK keeps its whole conf directory behind a symlink out of /usr
// (/usr/lib/jvm/<jdk>/conf -> /etc/java-openjdk). Mounting the JDK read-only
// without that target leaves java.security missing and no JVM can start (#546).
func TestEscapedLinkTargetsResolvesTheJDKConfSymlink(t *testing.T) {
	base := t.TempDir()
	jdk := filepath.Join(base, "usr", "lib", "jvm", "java-26-openjdk")
	conf := filepath.Join(base, "etc", "java-openjdk")
	for _, dir := range []string{filepath.Join(jdk, "bin"), conf} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(conf, filepath.Join(jdk, "conf")); err != nil {
		t.Fatal(err)
	}
	// A relative link stays inside the tree that is already mounted.
	if err := os.Symlink("bin", filepath.Join(jdk, "sbin")); err != nil {
		t.Fatal(err)
	}
	got := escapedLinkTargets([]string{jdk})
	if !slices.Contains(got, conf) {
		t.Fatalf("escapedLinkTargets(%q) = %v, want it to contain %q", jdk, got, conf)
	}
	if slices.Contains(got, "bin") {
		t.Fatalf("escapedLinkTargets(%q) = %v, must not include the relative link", jdk, got)
	}
	if got := escapedLinkTargets([]string{filepath.Join(base, "nope")}); got != nil {
		t.Fatalf("escapedLinkTargets on a missing dir = %v, want nil", got)
	}
}

// The gradle home is mounted read-only so gradle.properties and everything
// else at its top level resolve, with the cache directories read-write on top;
// mounting only the subdirectories left the home invisible (#548). Maven gets
// its repository only — a writable settings.xml would let sandboxed code
// repoint the host's repositories.
func TestJVMCacheDirsCoversGradleAndMaven(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GRADLE_USER_HOME", "")
	gradleHome := filepath.Join(home, ".gradle")
	if got := gradleReadOnlyDirs(true); !slices.Contains(got, gradleHome) {
		t.Fatalf("gradleReadOnlyDirs = %v, want it to contain %q", got, gradleHome)
	}
	if got := gradleReadOnlyDirs(false); got != nil {
		t.Fatalf("gradleReadOnlyDirs for a non-JVM line = %v, want nil", got)
	}
	got := jvmCacheDirs(true)
	if slices.Contains(got, gradleHome) {
		t.Fatalf("jvmCacheDirs = %v, must not mount the gradle home read-write", got)
	}
	want := []string{filepath.Join(home, ".m2", "repository")}
	for _, name := range gradleCacheSubdirs {
		want = append(want, filepath.Join(gradleHome, name))
	}
	for _, dir := range want {
		if !slices.Contains(got, dir) {
			t.Fatalf("jvmCacheDirs = %v, want it to contain %q", got, dir)
		}
	}
	if slices.Contains(got, filepath.Join(home, ".m2")) {
		t.Fatalf("jvmCacheDirs = %v, must not mount ~/.m2 itself read-write", got)
	}
	// The daemon subdir is not a read-write carve-out: it stays behind the
	// registry override in jvmEnv, per-workspace (#579).
	if slices.Contains(got, filepath.Join(gradleHome, "daemon")) {
		t.Fatalf("jvmCacheDirs = %v, must not mount the gradle daemon registry read-write", got)
	}
	if got := jvmCacheDirs(false); got != nil {
		t.Fatalf("jvmCacheDirs for a non-JVM line = %v, want nil", got)
	}
}

// $GRADLE_USER_HOME wins over ~/.gradle on both sides of the mount.
func TestGradleUserHomeEnvOverride(t *testing.T) {
	custom := t.TempDir()
	t.Setenv("GRADLE_USER_HOME", custom)
	if got := gradleReadOnlyDirs(true); !slices.Contains(got, custom) {
		t.Fatalf("gradleReadOnlyDirs = %v, want it to contain %q", got, custom)
	}
	if got := jvmCacheDirs(true); !slices.Contains(got, filepath.Join(custom, "caches")) {
		t.Fatalf("jvmCacheDirs = %v, want it to contain %q", got, filepath.Join(custom, "caches"))
	}
}

// A `gradle` line never names `java`, so the JDK is only mounted if the JVM
// commands pull it in themselves — along with the trust store its cacerts
// links to.
func TestJVMSupportDirsPullsInTheJDK(t *testing.T) {
	t.Setenv("JAVA_HOME", "/host/jdk")
	got := jvmSupportDirs(true)
	for _, want := range []string{"/host/jdk", "/etc/ssl"} {
		if !slices.Contains(got, want) {
			t.Fatalf("jvmSupportDirs = %v, want it to contain %q", got, want)
		}
	}
	if got := jvmSupportDirs(false); got != nil {
		t.Fatalf("jvmSupportDirs for a non-JVM line = %v, want nil", got)
	}
}

// isJVMCommand keys on the command basename, wrappers included.
func TestIsJVMCommand(t *testing.T) {
	for _, command := range []string{"java", "gradle", "./gradlew", "/usr/bin/mvn", "mvnw"} {
		if !isJVMCommand(command) {
			t.Fatalf("isJVMCommand(%q) = false, want true", command)
		}
	}
	for _, command := range []string{"go", "make", "sh"} {
		if isJVMCommand(command) {
			t.Fatalf("isJVMCommand(%q) = true, want false", command)
		}
	}
}

// The sandbox maps the caller to uid 0 and the JVM reads user.home from the
// passwd entry, not $HOME, so every JVM toolchain has to be told where home is.
func TestJVMEnvPinsUserHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	env := jvmEnv(true, "", []string{"HOME=" + home})
	if want := "JAVA_TOOL_OPTIONS=-Duser.home=" + home; !slices.Contains(env, want) {
		t.Fatalf("jvmEnv = %v, want it to contain %q", env, want)
	}
	env = jvmEnv(true, "", []string{"JAVA_TOOL_OPTIONS=-Xmx512m"})
	if want := "JAVA_TOOL_OPTIONS=-Xmx512m -Duser.home=" + home; len(env) != 1 || env[0] != want {
		t.Fatalf("jvmEnv = %v, want [%q]", env, want)
	}
	if env := jvmEnv(false, "", []string{"HOME=" + home}); len(env) != 1 {
		t.Fatalf("jvmEnv for a non-JVM line = %v, want it unchanged", env)
	}
}

// A non-empty registry appends org.gradle.daemon.registry.base to the same
// JAVA_TOOL_OPTIONS entry, so gradle's daemon registers per-workspace instead
// of in the host-wide ~/.gradle/daemon registry a concurrent session could
// stop (#579).
func TestJVMEnvSetsGradleDaemonRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	env := jvmEnv(true, "/tmp/gradle-daemon", []string{"HOME=" + home})
	want := "JAVA_TOOL_OPTIONS=-Duser.home=" + home + " -Dorg.gradle.daemon.registry.base=/tmp/gradle-daemon"
	if !slices.Contains(env, want) {
		t.Fatalf("jvmEnv = %v, want it to contain %q", env, want)
	}
	env = jvmEnv(true, "/tmp/gradle-daemon", []string{"JAVA_TOOL_OPTIONS=-Xmx512m"})
	want = "JAVA_TOOL_OPTIONS=-Xmx512m -Duser.home=" + home + " -Dorg.gradle.daemon.registry.base=/tmp/gradle-daemon"
	if len(env) != 1 || env[0] != want {
		t.Fatalf("jvmEnv = %v, want [%q]", env, want)
	}
}
