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
	path := socketPathIn(t)
	s, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	peer := dial(t, path)
	defer func() { _ = peer.Close() }()
	if _, err := peer.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	conn, err := acceptPeer(s, path)
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
	socket := socketPathIn(t)
	held, err := Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	_, err = Listen(socket)
	if err == nil {
		t.Fatal("a second listener took a live socket")
	}
	if !strings.Contains(err.Error(), "already serving a shared session") {
		t.Fatalf("err = %v, want the held-session message", err)
	}
}

func TestListenSocketIsTrustedByTheRelay(t *testing.T) {
	path := socketPathIn(t)
	s, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSocket == 0 {
		t.Fatalf("%s is %s, want a socket file", path, st.Mode())
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
	path := socketPathIn(t)
	s, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	_ = dial(t, path).Close()
	_, err = acceptPeer(s, path)
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
	path := socketPathIn(t)
	s, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	_ = dial(t, path).Close()
	conn, err := acceptPeer(s, path)
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

func TestSetRelaySocketKeepsTheRelaysOwnAnswers(t *testing.T) {
	conf := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", conf)
	dir := filepath.Dir(RelayConfigPath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := RelayConfigPath()
	// A number, a nested object and HTML-significant characters: agent-relay's
	// own answers must survive the rewrite byte for byte.
	before := `{"agent":["invokr","acp"],"version":1,"headers":{"X-A":"a<b"},"ttl":30}`
	if err := os.WriteFile(file, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SetRelaySocket(SocketPath()); err != nil {
		t.Fatalf("set = %v", err)
	}
	stored, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), "\"version\":1") || !strings.Contains(string(stored), "\"ttl\":30") {
		t.Fatalf("config after the write = %s, want the relay's numbers unconverted", stored)
	}
	if !strings.Contains(string(stored), "a<b") {
		t.Fatalf("config after the write = %s, want the headers value untouched", stored)
	}
	if !strings.Contains(string(stored), "agent_socket") {
		t.Fatalf("config after the write = %s, want agent_socket set", stored)
	}
	if strings.Contains(string(stored), `\u003c`) {
		t.Fatal("a value was HTML-escaped on the way out")
	}
	held, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if held.Mode()&0o777 != 0o600 {
		t.Fatalf("config mode = %o, want 600", held.Mode())
	}
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 {
		t.Fatalf("%s holds %d files, want only the config", dir, len(left))
	}
	if err := SetRelaySocket(""); err != nil {
		t.Fatalf("clear = %v", err)
	}
	stored, _ = os.ReadFile(file)
	if strings.Contains(string(stored), "agent_socket") {
		t.Fatalf("config after clear = %s, want agent_socket gone", stored)
	}
}

func TestSetRelaySocketRefusesASymlinkedConfig(t *testing.T) {
	conf := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", conf)
	elsewhere := filepath.Join(conf, "elsewhere.json")
	if err := os.WriteFile(elsewhere, []byte(`{"token":"mine"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(RelayConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, RelayConfigPath()); err != nil {
		t.Fatal(err)
	}
	if err := SetRelaySocket(SocketPath()); err == nil {
		t.Fatal("SetRelaySocket wrote through a symlink at the config path")
	}
	held, err := os.ReadFile(elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	if string(held) != `{"token":"mine"}` {
		t.Fatalf("the symlink's target changed: %s", held)
	}
}
