package shell

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/invokr-agent/invokr/config"
)

// escapedLinkTargets returns the absolute targets of the symlinks directly
// inside each of dirs.
//
// A read-only mount is a bind of the host directory, so a symlink inside it
// that points somewhere else on the host dangles unless that target is mounted
// too. The JDK is laid out exactly that way on every mainstream distribution:
// /usr/lib/jvm/<jdk>/conf is a symlink to /etc/java-openjdk (Arch), /etc/java
// or /etc/alternatives/*. Unmounted, java.security does not exist and no JVM
// process can start at all (#546). Only absolute targets are resolved —
// a relative one points inside the tree that is already mounted.
func escapedLinkTargets(dirs []string) []string {
	var targets []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			target, err := os.Readlink(filepath.Join(dir, entry.Name()))
			if err != nil || !filepath.IsAbs(target) {
				continue
			}
			targets = append(targets, target)
		}
	}
	return targets
}

// jvmCommands names the commands that start a JVM. The wrappers a repo ships
// (./gradlew, ./mvnw) are matched by suffix.
var jvmCommands = []string{"java", "javac", "jar", "gradle", "kotlin", "kotlinc", "mvn", "sbt", "scala"}

func isJVMCommand(command string) bool {
	base := filepath.Base(strings.TrimSpace(command))
	return slices.Contains(jvmCommands, base) || strings.HasSuffix(base, "gradlew") || strings.HasSuffix(base, "mvnw")
}

// jvmSupportDirs names what a JVM needs mounted read-only beyond the directory
// its own binary lives in: the JDK a wrapper script finds through JAVA_HOME or
// PATH (a `gradle` line never names `java`, so commandDirs never resolves it),
// and /etc/ssl, where the distribution keeps the cacerts trust store the JDK
// links its lib/security/cacerts to.
func jvmSupportDirs(isJVM bool) []string {
	if !isJVM {
		return nil
	}
	dirs := commandDirs("java")
	if home := config.Env("JAVA_HOME"); home != "" {
		dirs = append(dirs, home)
	}
	return append(dirs, "/etc/ssl")
}

// gradleCacheSubdirs names the directories under the gradle home a build
// writes to. The list does not have to be complete: the home itself is mounted
// read-only (gradleReadOnlyDirs), so an unlisted path is still readable rather
// than missing — which is what made the first attempt at #548 fail.
var gradleCacheSubdirs = []string{"caches", "native", "wrapper", "workers", "notifications", "jdks", ".tmp"}

// gradleReadOnlyDirs mounts the gradle home itself read-only. Mounting only
// the cache subdirectories left the home invisible, so `ls ~/.gradle` answered
// "No such file or directory" and gradle silently resolved no gradle.properties
// from it; a real M5 build then oscillated for 93 minutes against the 12-32 it
// normally takes (#548, reverted in af421ce). Read-only is what the issue
// actually wanted: gradle sees its configuration and cannot leave a
// gradle.properties or an init.d/ script behind for every later host build.
func gradleReadOnlyDirs(isJVM bool) []string {
	if !isJVM {
		return nil
	}
	return []string{gradleUserHome()}
}

// jvmCacheDirs names the toolchain caches a JVM build writes to, the same
// carve-out `go` gets for its build cache: gradle unpacks its distribution and
// its native libraries under GRADLE_USER_HOME and maven its downloads into
// ~/.m2/repository, both outside the workspace. Read-only they are worse than
// absent — gradle fails on "Failed to load native library
// 'libnative-platform.so'" — and with the network off nothing can be
// refetched. These layer read-write over the read-only home, so only the cache
// directories are writable. Only the maven repository, not ~/.m2 itself: a
// writable settings.xml would let sandboxed code repoint the host's
// repositories and proxies for every later build.
func jvmCacheDirs(isJVM bool) []string {
	if !isJVM {
		return nil
	}
	dirs := []string{userHomeDir(".m2", "repository")}
	for _, name := range gradleCacheSubdirs {
		dir := filepath.Join(gradleUserHome(), name)
		// DedupPlan drops a path that does not exist, and a directory created
		// under the read-only home inside the sandbox would not persist.
		_ = os.MkdirAll(dir, 0o755)
		dirs = append(dirs, dir)
	}
	return dirs
}

// jvmEnv points the JVM's `user.home` at the real home directory and, when
// registry is non-empty, the gradle daemon registry at a per-workspace
// directory.
//
// The sandbox maps the caller to uid 0, and the JVM reads user.home from the
// passwd entry of the euid, not from $HOME — so every JVM toolchain resolved
// its home-relative cache under /root, which is mounted nowhere: gradle failed
// to extract its native library there however writable the real gradle home
// was, and maven, sbt and ivy would each miss their own (#546). One
// JAVA_TOOL_OPTIONS entry fixes all of them at once; the JVM announces it on
// stderr with a "Picked up JAVA_TOOL_OPTIONS" line.
//
// Without a registry override every session's gradle client registers its
// daemon in the host-wide ~/.gradle/daemon registry, so a --no-daemon build
// in one concurrent session stops another session's daemon (#579).
// org.gradle.daemon.registry.base moves that registry into the per-workspace
// shared tmp dir so concurrent sessions cannot stop each other's daemons.
func jvmEnv(isJVM bool, registry string, env []string) []string {
	home := userHomeDir()
	if !isJVM || home == "" {
		return env
	}
	option := "-Duser.home=" + home
	if registry != "" {
		option += " -Dorg.gradle.daemon.registry.base=" + registry
	}
	for i, kv := range env {
		if after, ok := strings.CutPrefix(kv, "JAVA_TOOL_OPTIONS="); ok {
			env[i] = "JAVA_TOOL_OPTIONS=" + strings.TrimSpace(after+" "+option)
			return env
		}
	}
	return append(env, "JAVA_TOOL_OPTIONS="+option)
}

func gradleUserHome() string {
	if home := config.Env("GRADLE_USER_HOME"); home != "" {
		return home
	}
	return userHomeDir(".gradle")
}

// userHomeDir joins name onto the user's home directory, or returns "" when
// there is no home to join it to (DedupPlan drops the empty entry).
func userHomeDir(name ...string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(append([]string{home}, name...)...)
}
