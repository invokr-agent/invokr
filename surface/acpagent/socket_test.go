package acpagent

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

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

func socketPathIn(t *testing.T) string {
	dir := filepath.Join(t.TempDir(), "r")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "s.sock")
}

// Package acp reads a connection through a buffered reader, which panics on a
// negative read count and gives up after spinning on a zero one: a hung-up
// peer must end the read at EOF, and a closed connection must report why.
func TestAcceptedConnectionEndsReadsCleanly(t *testing.T) {
	s, err := Listen(socketPathIn(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	_ = dial(t, s.Path).Close()
	conn, err := s.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	reader := bufio.NewReader(conn)
	if _, _, err := reader.ReadLine(); err != nil && !strings.Contains(err.Error(), "EOF") {
		t.Fatalf("a hung-up peer = %v, want the read to end at EOF", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, _, err := bufio.NewReader(conn).ReadLine(); err == nil {
		t.Fatal("a read on a closed connection reported no error")
	}
}

func TestListenRefusesAPathThatIsNotASocket(t *testing.T) {
	path := socketPathIn(t)
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Listen(path)
	if err == nil || !strings.Contains(err.Error(), "not a socket file") {
		t.Fatalf("listen over a plain file = %v, want the file named and kept", err)
	}
	if found, err := os.ReadFile(path); err != nil || string(found) != "not a socket" {
		t.Fatalf("the file was %q, %v: want it left alone", found, err)
	}
}

func TestAnswersCountsAFullAcceptQueue(t *testing.T) {
	path := socketPathIn(t)
	s, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var peers []net.Conn
	defer func() {
		for _, peer := range peers {
			_ = peer.Close()
		}
	}()
	for range 200 {
		peer, err := net.DialTimeout("unix", path, 2*time.Second)
		if err != nil {
			break
		}
		peers = append(peers, peer)
	}
	if len(peers) < 2 {
		t.Skipf("only %d dials queued: this host's backlog cannot be filled", len(peers))
	}
	if !answers(path) {
		t.Fatal("a listener with a full accept queue looked dead")
	}
}
