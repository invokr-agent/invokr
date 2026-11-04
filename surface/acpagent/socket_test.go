package acpagent

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// dial connects to path the way agent-relay does: a bounded unix dial.
func dial(t *testing.T, path string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	return conn
}

func TestListenAcceptsOwnPeer(t *testing.T) {
	s, err := Listen(socketPathIn(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	peer := dial(t, s.Path)
	defer func() { _ = peer.Close() }()
	if _, err := peer.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	conn, err := s.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer func() { _ = conn.Close() }()
	seen := make([]byte, 5)
	n, err := conn.Read(seen)
	if err != nil || n != 5 || string(seen) != "ping\n" {
		t.Fatalf("read = %d %q, %v", n, string(seen[:n]), err)
	}
}

// A socket file nobody answers is left behind by a crash, and bind refuses
// the path: listening must clear it rather than fail.
func TestListenClearsStaleSocket(t *testing.T) {
	path := socketPathIn(t)
	if err := leaveStaleSocket(path); err != nil {
		t.Fatal(err)
	}
	s, err := Listen(path)
	if err != nil {
		t.Fatalf("listen over a stale socket: %v", err)
	}
	defer func() { _ = s.Close() }()
	_ = dial(t, path).Close()
}

// leaveStaleSocket binds a socket and closes the descriptor without removing
// the file, which is what a killed listener leaves behind.
func leaveStaleSocket(path string) error {
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return err
	}
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: path}); err != nil {
		return err
	}
	return unix.Close(fd)
}

// A live socket belongs to another shared session: taking the path would point
// that session's chat here.
func TestListenRefusesLiveSocket(t *testing.T) {
	held, err := Listen(socketPathIn(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	_, err = Listen(held.Path)
	if err == nil {
		t.Fatal("a second listener took a live socket")
	}
	if !strings.Contains(err.Error(), "already serving a shared session") {
		t.Fatalf("err = %v, want the held-session message", err)
	}
}

// agent-relay dials only a socket file it owns, with no group or other write
// bit, in a directory others cannot write. The listener creates exactly that,
// or the relay answers "socket not used" and spawns its own agent.
func TestListenSocketIsTrustedByTheRelay(t *testing.T) {
	s, err := Listen(socketPathIn(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	st, err := os.Lstat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.Lstat(filepath.Dir(s.Path))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSocket == 0 {
		t.Fatalf("%s is %s, want a socket file", s.Path, st.Mode())
	}
	if st.Mode().Perm()&0o022 != 0 {
		t.Fatalf("socket mode %o is writable by group or others", st.Mode().Perm())
	}
	if dir.Mode().Perm()&0o022 != 0 {
		t.Fatalf("directory mode %o is writable by others", dir.Mode().Perm())
	}
}

// A peer that does not run as us never reaches ACP. No test can run under
// another uid, so the credential read is replaced, as in agent-relay.
func TestAcceptRefusesForeignPeer(t *testing.T) {
	own := peerUID
	peerUID = func(fd int) (int, error) {
		uid, _ := own(fd)
		return uid + 1, nil
	}
	t.Cleanup(func() { peerUID = own })
	s, err := Listen(socketPathIn(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	_ = dial(t, s.Path).Close()
	_, err = s.Accept()
	if err == nil {
		t.Fatal("accepted a peer with a foreign uid")
	}
	if !strings.Contains(err.Error(), "is not our uid") {
		t.Fatalf("err = %v, want the uid mismatch named", err)
	}
}

// socketPathIn returns a path under a private directory. AF_UNIX caps the
// name at 107 bytes, so a long temp root is not always usable.
func socketPathIn(t *testing.T) string {
	dir := filepath.Join(t.TempDir(), "r")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "s.sock")
}
