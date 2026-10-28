//go:build linux

package sandbox

// Linux namespace sandbox for toolbox tool execution.
//
// Recipe (validated by hands-on stress testing, see
// docs/private/plans/toolbox-agent-authored-tools.md "Workspace access" and
// "M2" sections): a rootless user namespace mapped uid->0, a private
// mount namespace, and a from-scratch root built by RO bind-mounting only
// the host paths the tool needs at their *original absolute paths* (so the
// command/args/env built by the rest of toolbox.go need no path rewriting).
// pivot_root requires the new root to already be a mount point, and the
// only pivot_root sequence that does not leak the host root via a leftover
// oldroot mount is the self-stacking idiom: `pivot_root . .` followed by a
// lazy `umount -l .` of the now-topmost (old) layer. Naive variants (an
// explicit oldroot directory, eager rmdir, etc.) were proven in testing to
// leave the host root reachable.
import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/invokr-agent/invokr/tools/core"
	"golang.org/x/sys/unix"
)

// Plan describes the bind mounts and Network/resource posture for one
// toolbox tool invocation, derived from its declared capabilities.
type Plan struct {
	// Sealed mounts are applied last, so writable ancestors cannot shadow them.
	Sealed             []string
	RO                 []string // host paths bind-mounted read-only at the same path
	RW                 []string // host paths bind-mounted read-write at the same path
	PinnedRW, PinnedRO []string // No-symlink fd binds: RW directories, RO regular files.
	Network            bool
	MemoryLimit        int64
	CommandPath        string // absolute path to the resolved interpreter/binary
	// TmpDir, when set, is bind-mounted at /tmp in place of the private
	// tmpfs, so scratch files outlive a single call and are shared with
	// whatever else mounts the same directory.
	TmpDir string
	// Detached, for a process that must outlive the call that started it (the
	// tmux server holding a session's namespaces), drops the parent-death and
	// process-group kills: the sandbox is started with Start() and torn down by
	// its own payload exiting.
	Detached bool
	// AllowHosts scopes an otherwise-open network namespace to named hosts:
	// the sandbox gets a generated /etc/hosts holding only their addresses and
	// no /etc/resolv.conf, so nothing else resolves.
	AllowHosts []string
}

func BuildPlan(cmdPath string, toolDir string, venvDir string, workspaceDir string, caps core.ToolCapabilities, memoryLimitBytes int64, allowHosts []string) Plan {
	plan := Plan{
		// cmdPath's directory is bind-mounted explicitly because the resolved
		// runtime binary (e.g. "go", "node") often lives outside the standard
		// /usr,/bin,/lib paths (e.g. /usr/local/go/bin, /opt/node22/bin).
		RO:      []string{"/usr", "/bin", "/lib", "/lib64", toolDir, filepath.Dir(cmdPath)},
		Network: caps.Network, MemoryLimit: memoryLimitBytes,
		CommandPath: cmdPath, AllowHosts: allowHosts,
	}
	// A symlinked runtime (e.g. ~/.local/bin/node -> ~/.hermes/node/bin/node)
	// dangles inside the sandbox unless its target is mounted too. Still exec
	// via the original path: a venv python relies on its symlink location for
	// sys.prefix.
	if resolved, err := filepath.EvalSymlinks(cmdPath); err == nil && resolved != cmdPath {
		plan.RO = append(plan.RO, filepath.Dir(resolved))
	}
	if caps.Network {
		plan.RO = append(plan.RO, "/etc/nsswitch.conf", "/etc/services")
		plan.RO = append(plan.RO, caCertDirs()...)
		// Scoped network gets a generated /etc/hosts and no resolver instead.
		if len(allowHosts) == 0 {
			plan.RO = append(plan.RO, "/etc/resolv.conf", "/etc/hosts")
		}
	}
	plan.RO = append(plan.RO, venvDir)
	if caps.WritesWorkspace {
		plan.RW = append(plan.RW, workspaceDir)
	} else if caps.ReadsWorkspace {
		plan.RO = append(plan.RO, workspaceDir)
	}
	return DedupPlan(plan)
}

// caCertDirs names the host directories a TLS client reads its trust store
// from. Unmounted, DNS and TCP work inside the sandbox but every https call
// dies in the handshake with "unable to get local issuer certificate" (curl
// exit 77). The three cover every mainstream layout — /etc/ssl (Arch, Debian,
// SUSE), /etc/pki (Fedora, RHEL), and /etc/ca-certificates, where Arch keeps
// the extracted bundle that /etc/ssl/cert.pem and certs/ca-certificates.crt
// are symlinks into. Non-existent entries are dropped by DedupPlan. Each
// dir's private key subtree (e.g. */private) rides along in the read-only
// bind but stays unreadable: it's root-owned 0700, and a rootless userns
// maps the sandbox's root to the invoking host uid, not real root.
func caCertDirs() []string { return []string{"/etc/ssl", "/etc/ca-certificates", "/etc/pki"} }

func DedupPlan(plan Plan) Plan {
	plan.RO, plan.RW = dedupExistingPaths(plan.RO), dedupExistingPaths(plan.RW)
	// Pins must match RW entries and never fail a run for a path RW dropped.
	plan.PinnedRW = dedupExistingPaths(plan.PinnedRW)
	return plan
}

func dedupExistingPaths(paths []string) []string {
	// Remove empty entries before Clean can turn them into the working directory.
	nonempty := slices.DeleteFunc(slices.Clone(paths), func(p string) bool { return strings.TrimSpace(p) == "" })
	cleaned := core.MapSlice(nonempty, filepath.Clean)
	return slices.DeleteFunc(core.DedupPreserveOrder(cleaned), func(p string) bool { _, err := os.Stat(p); return err != nil })
}

func openPinnedPath(path string, directory bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) == "/" {
		return nil, fmt.Errorf("pin path %q: expected non-root absolute path", path)
	}
	parent, err := os.Open("/")
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
	for index, part := range parts {
		flags := unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if directory || index < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		fd, openErr := unix.Openat(int(parent.Fd()), part, flags, 0)
		_ = parent.Close()
		if openErr != nil {
			return nil, fmt.Errorf("pin path %q: %w", path, openErr)
		}
		parent = os.NewFile(uintptr(fd), part)
	}
	if info, err := parent.Stat(); !directory && (err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0) {
		_ = parent.Close()
		return nil, fmt.Errorf("pin file %q: expected executable regular file: %w", path, os.ErrInvalid)
	}
	return parent, nil
}

func reopenPinned(plan Plan) ([]*os.File, error) {
	var files []*os.File
	for index, path := range slices.Concat(plan.PinnedRW, plan.PinnedRO) {
		file, err := openPinnedPath(path, index < len(plan.PinnedRW))
		if err != nil {
			closePinned(files)
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

func closePinned(files []*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

// Command wraps command/args so they run inside the namespace
// sandbox described by plan, returning a ready-to-configure *exec.Cmd. The
// caller is still responsible for Env/Stdin/Stdout/Stderr/Dir.
func command(ctx context.Context, plan Plan, dir string, args []string) (*exec.Cmd, func(), error) {
	pinned, err := reopenPinned(plan)
	if err != nil {
		return nil, nil, err
	}
	script, cleanup, err := writeSandboxScript(plan, dir)
	if err != nil {
		closePinned(pinned)
		return nil, nil, err
	}
	cleanupPinned := func() {
		cleanup()
		closePinned(pinned)
	}

	unshareArgs := []string{"--user", "--map-root-user", "--mount", "--pid", "--fork", "--uts", "--ipc"}
	// --kill-child makes unshare SIGKILL the shell it forked when unshare itself
	// dies, so Pdeathsig below reaches the whole tree and not just unshare.
	if unshareSupportsKillChild() && !plan.Detached {
		unshareArgs = append(unshareArgs, "--kill-child")
	}
	if !plan.Network {
		unshareArgs = append(unshareArgs, "--net")
	}
	initShell := "/bin/sh"
	if len(plan.PinnedRO) > 0 {
		initShell = "/bin/bash" // Shell tool caller already requires bash; dash cannot close fd >= 10.
	}
	unshareArgs = append(unshareArgs, "--", initShell, script)
	unshareArgs = append(unshareArgs, plan.CommandPath)
	unshareArgs = append(unshareArgs, args...)

	if plan.Detached {
		cmd := exec.Command("unshare", unshareArgs...)
		cmd.ExtraFiles = pinned
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		return cmd, cleanupPinned, nil
	}
	cmd := exec.CommandContext(ctx, "unshare", unshareArgs...)
	cmd.ExtraFiles = pinned
	// Pdeathsig kills the sandbox child when the agent dies for any reason,
	// including an external SIGKILL that never runs cmd.Cancel below.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error {
		if cmd.Process == nil || cmd.Process.Pid <= 0 {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return cmd, cleanupPinned, nil
}

// unshareSupportsKillChild reports whether the host util-linux is new enough
// for --kill-child; older builds reject the flag outright.
var unshareSupportsKillChild = sync.OnceValue(func() bool {
	out, err := exec.Command("unshare", "--help").CombinedOutput()
	return err == nil && strings.Contains(string(out), "--kill-child")
})

// loopbackEnabled reports whether a no-network sandbox still gets a working
// loopback interface. Default-on; set INVOKR_SANDBOX_LOOPBACK=0 to opt out.
func loopbackEnabled() bool { return os.Getenv("INVOKR_SANDBOX_LOOPBACK") != "0" }

func writeSandboxScript(plan Plan, toolDir string) (string, func(), error) {
	root, err := os.MkdirTemp("", "invokr-sandbox-")
	if err != nil {
		return "", nil, fmt.Errorf("create sandbox root: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(root) }

	var b strings.Builder
	b.WriteString("set -e\n")
	// The script's own PATH must not depend on whatever (possibly isolated or
	// near-empty) env policy the tool itself runs under - it needs to find
	// mount/pivot_root/umount regardless of what env the caller configured.
	// The caller's PATH is restored before exec: it is the env the caller
	// built for the command, and a shell inside the sandbox resolves the
	// same binaries through it.
	b.WriteString("INVOKR_CALLER_PATH=\"$PATH\"\nPATH=/usr/sbin:/usr/bin:/sbin:/bin\nexport PATH\nmount --make-rprivate / 2>/dev/null || true\n")
	if !plan.Network && loopbackEnabled() {
		// Best-effort: `ip` may be absent, and the namespace is useless for
		// egress either way, so a failure here must not fail the tool call -
		// but it does leave a greppable line rather than silently doing
		// nothing (peer follow-up).
		b.WriteString("ip link set lo up 2>/dev/null || echo 'invokr: loopback not brought up' >&2\n")
	}
	fmt.Fprintf(&b, "ROOT=%s\nmount --bind \"$ROOT\" \"$ROOT\"\n", shellEscape(root))
	// proc/dev/tmp must be mounted before the RO/RW binds below:
	// any of those host paths may live under /tmp (e.g. a test's t.TempDir()
	// tool dir), and mounting tmpfs on $ROOT/tmp here first means later
	// binds layer on top of it instead of being hidden underneath it.
	b.WriteString("mkdir -p \"$ROOT/proc\" \"$ROOT/dev\" \"$ROOT/tmp\" \"$ROOT/run\"\nmount -t proc proc \"$ROOT/proc\"\nmount -t tmpfs -o nosuid,nodev tmpfs \"$ROOT/tmp\"\n")
	if plan.TmpDir != "" {
		// A shared /tmp layers over the tmpfs, so a path under it is still
		// hidden from the host root while the directory itself persists.
		fmt.Fprintf(&b, "mount --bind %s \"$ROOT/tmp\"\n", shellEscape(plan.TmpDir))
	}
	for _, dev := range []string{"null", "zero", "urandom"} {
		fmt.Fprintf(&b, "touch \"$ROOT/dev/%s\" && mount --bind /dev/%s \"$ROOT/dev/%s\" 2>/dev/null || true\n", dev, dev, dev)
	}
	// A terminal program (the tmux server and its panes) needs a pty pair, and
	// openpty finds none without a devpts instance: it fails the fork with
	// ENOENT. newinstance keeps the pts namespace private to the sandbox.
	b.WriteString("mkdir -p \"$ROOT/dev/pts\" && mount -t devpts -o nosuid,noexec,newinstance,ptmxmode=0666 devpts \"$ROOT/dev/pts\" 2>/dev/null && ln -sf pts/ptmx \"$ROOT/dev/ptmx\" || echo 'invokr: no pty in sandbox' >&2\n")
	pins := slices.Concat(plan.PinnedRW, plan.PinnedRO)
	for index, p := range slices.Concat(plan.RO, plan.RW) {
		source := p
		if index := slices.Index(pins, p); index >= 0 {
			source = fmt.Sprintf("/proc/self/fd/%d", 3+index)
		}
		writeSandboxBind(&b, root, p, source, index < len(plan.RO))
	}
	writeSealedBinds(&b, root, plan)
	if err := writeAllowedHosts(&b, root, plan.AllowHosts); err != nil {
		cleanup()
		return "", nil, err
	}
	writePinnedClose(&b, len(pins))
	// Everything the sandbox needs exists by now, so seal the root itself: a
	// write outside a mounted RW path (child mounts keep their own rw flag)
	// must fail loudly rather than land in a tmpdir that is deleted at exit.
	b.WriteString("mount -o remount,bind,ro,nosuid,nodev \"$ROOT\"\n")
	if plan.MemoryLimit > 0 {
		fmt.Fprintf(&b, "ulimit -v %d\n", plan.MemoryLimit/1024)
	}
	// The only pivot_root sequence proven not to leak the host root: stack
	// the old root onto the new root at the same path, then lazily unmount
	// the (now topmost) old layer.
	b.WriteString("cd \"$ROOT\"\npivot_root . .\numount -l . 2>/dev/null || true\n")
	fmt.Fprintf(&b, "cd %s\nPATH=\"$INVOKR_CALLER_PATH\"\nunset INVOKR_CALLER_PATH\nexec \"$@\"\n", shellEscape(toolDir))

	scriptPath := filepath.Join(root, ".sandbox-init.sh")
	if err := os.WriteFile(scriptPath, []byte(b.String()), 0o700); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("write sandbox script: %w", err)
	}
	return scriptPath, cleanup, nil
}

func writePinnedClose(b *strings.Builder, count int) {
	for index := range count {
		fmt.Fprintf(b, "exec %d<&-\n", 3+index)
	}
}

// writeAllowedHosts gives the sandbox a generated /etc/hosts holding only the
// declared hosts, resolved on the host before pivot_root. Together with the
// absent /etc/resolv.conf (see BuildPlan) that is the scoping: an undeclared
// name has nowhere to resolve, so the connection fails inside the sandbox.
func writeAllowedHosts(b *strings.Builder, root string, allowHosts []string) error {
	if len(allowHosts) == 0 {
		return nil
	}
	hosts := "127.0.0.1 localhost\n::1 localhost\n"
	for _, host := range allowHosts {
		host = strings.TrimSpace(host)
		addrs, _ := net.LookupHost(host)
		for _, addr := range addrs {
			hosts += fmt.Sprintf("%s %s\n", addr, host)
		}
	}
	path := filepath.Join(root, ".sandbox-hosts")
	if err := os.WriteFile(path, []byte(hosts), 0o600); err != nil {
		return fmt.Errorf("write sandbox hosts file: %w", err)
	}
	fmt.Fprintf(b, "mkdir -p \"$ROOT/etc\" && touch \"$ROOT/etc/hosts\" && mount --bind %s \"$ROOT/etc/hosts\"\n", shellEscape(path))
	return nil
}

func writeSandboxBind(b *strings.Builder, root string, hostPath string, source string, readOnly bool) {
	target := filepath.Join(root, hostPath)
	if info, err := os.Stat(hostPath); err == nil && !info.IsDir() {
		fmt.Fprintf(b, "mkdir -p %s && touch %s\n", shellEscape(filepath.Dir(target)), shellEscape(target))
	} else {
		fmt.Fprintf(b, "mkdir -p %s\n", shellEscape(target))
	}
	fmt.Fprintf(b, "mount --bind %s %s\n", shellEscape(source), shellEscape(target))
	ro := ""
	if readOnly {
		ro = "ro,"
	}
	fmt.Fprintf(b, "mount -o remount,bind,%snosuid,nodev %s\n", ro, shellEscape(target))
}

// shellEscape single-quotes s for safe interpolation into the generated
// sandbox script: Go's %q uses Go string escaping, which does not match
// POSIX shell quoting rules and can be broken out of by paths containing
// $, backticks, or double quotes.
func shellEscape(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

var sandboxSupported = probeSandboxSupport()

// probeSandboxSupport checks not just that `unshare` exists but that
// unprivileged user namespaces with a uid map actually work: hardened hosts
// ship the binary while disabling the namespace (sysctl
// user.max_user_namespaces=0, or AppArmor restrictions on Ubuntu 24.04), and
// --map-root-user is the exact step that fails there ("write /proc/self/uid_map:
// Operation not permitted"). Probing it once at init lets the caller fall back
// to the explicit-opt-in unsandboxed path instead of failing every invocation.
func probeSandboxSupport() bool {
	if _, err := exec.LookPath("unshare"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "unshare", "--user", "--map-root-user", "true").Run() == nil
}

func Available() bool { return sandboxSupported }

func writeSealedBinds(b *strings.Builder, root string, plan Plan) {
	for _, p := range plan.Sealed {
		// Never create mountpoints through an RW bind. A path disappearing
		// after authorization must fail the mount, not recreate host state.
		target := shellEscape(filepath.Join(root, p))
		fmt.Fprintf(b, "mount --bind %s %s\nmount -o remount,bind,ro,nosuid,nodev %s\n", shellEscape(p), target, target)
	}
}
