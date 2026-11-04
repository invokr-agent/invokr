// The socket half of sharing a live session with agent-relay: where the TUI
// listens, and the checks that make answering it safe. agent-relay dials the
// same path and trusts it only when the file and its directory deny group and
// other writes, the file belongs to its uid, and the process on the other end
// of the connection runs as its uid. This listener keeps the same promises for
// the socket it creates and makes the peer check its own, because whoever can
// create, replace or write this socket can put words into a chat and act in a
// workspace.
package acpagent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"golang.org/x/sys/unix"

	"github.com/invokr-agent/invokr/config"
)

const (
	socketDirName = "invokr"
	socketName    = "relay.sock"
	socketDirMode = 0o700
	socketMode    = 0o600
	listenBacklog = 2
	// AF_UNIX names a path in 107 bytes; bind reports a bare EINVAL, so say
	// which end is wrong before the socket is created.
	maxSocketPath = 107
)

// SocketPath is where a shared session listens: $XDG_RUNTIME_DIR/invokr/relay
// .sock, falling back to ~/.invokr/run/relay.sock for a session with no runtime
// directory. agent-relay's agent_socket is set to the same path.
func SocketPath() string {
	runtime := config.Env("XDG_RUNTIME_DIR")
	if runtime == "" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".invokr", "run", socketName)
	}
	return filepath.Join(runtime, socketDirName, socketName)
}

// Socket is the listener one session holds. Only one session can hold a path
// at a time, which is what makes "another session already shares" reportable.
type Socket struct {
	Path string
	fd   int
}

// Listen binds path after clearing a socket nobody answers. A socket that
// answers belongs to a live session, and this one refuses to take the path.
func Listen(path string) (Socket, error) {
	if len(path) > maxSocketPath {
		return Socket{}, fmt.Errorf("%s is %d bytes; a unix socket path fits %d", path, len(path), maxSocketPath)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, socketDirMode); err != nil {
		return Socket{}, err
	}
	// MkdirAll applies the umask, and a directory others can write is a socket
	// anyone can replace.
	if err := os.Chmod(dir, socketDirMode); err != nil {
		return Socket{}, err
	}
	if err := clearStale(path); err != nil {
		return Socket{}, err
	}
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return Socket{}, err
	}
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: path}); err != nil {
		_ = unix.Close(fd)
		return Socket{}, err
	}
	if err := unix.Listen(fd, listenBacklog); err != nil {
		_ = unix.Close(fd)
		return Socket{}, err
	}
	// Bind honours the umask; the socket must not be writable by group or others.
	if err := os.Chmod(path, socketMode); err != nil {
		_ = unix.Close(fd)
		return Socket{}, err
	}
	return Socket{Path: path, fd: fd}, nil
}

// peerUID is a package var so a test can pretend a peer runs as someone else:
// no test can run an invokr under another uid.
var peerUID = peerCredentialUID

// Accept waits for the next connection and returns it only when the process
// behind it runs as this process's uid. A peer whose uid cannot be read is
// refused: the credential check is the whole trust boundary, so it fails closed.
func (s Socket) Accept() (*Conn, error) {
	fd, _, err := unix.Accept(s.fd)
	if err != nil {
		return nil, err
	}
	uid, err := peerUID(fd)
	if err != nil || uid != os.Getuid() {
		defer func() { _ = unix.Close(fd) }()
		return nil, fmt.Errorf("%s: %s", s.Path, blame(err, uid))
	}
	conn := &Conn{}
	conn.fd.Store(int32(fd))
	return conn, nil
}

// Conn is one accepted connection: the descriptor both directions speak ACP
// over. Go cannot adopt an accepted descriptor as a net.UnixConn (only the
// side that connected owns one), so the stream reads and writes the fd.
type Conn struct {
	fd atomic.Int32
}

func (c *Conn) Read(into []byte) (int, error) {
	fd := int(c.fd.Load())
	if fd < 0 {
		return -1, errors.New("socket connection closed")
	}
	n, err := unix.Read(fd, into)
	if err != nil {
		return -1, err
	}
	return n, nil
}

func (c *Conn) Write(from []byte) (int, error) {
	fd := int(c.fd.Load())
	if fd < 0 {
		return -1, errors.New("socket connection closed")
	}
	n, err := unix.Write(fd, from)
	if err != nil {
		return -1, err
	}
	return n, nil
}

// Close releases the descriptor once: acp joins a reader and a writer and
// closes both, and a second close of a closed fd would report a failed session
// where the stream simply ended.
func (c *Conn) Close() error {
	fd := int(c.fd.Swap(-1))
	if fd < 0 {
		return nil
	}
	return unix.Close(fd)
}

// Close stops listening and removes the socket file, so a later session can
// take the path.
func (s Socket) Close() error {
	closed := unix.Close(s.fd)
	removed := os.Remove(s.Path)
	if os.IsNotExist(removed) {
		removed = nil
	}
	return errors.Join(closed, removed)
}

// clearStale removes a socket file nobody answers. One that answers belongs to
// another session's shared TUI, and taking the path would point that session's
// chat at this one.
func clearStale(path string) error {
	if answers(path) {
		return fmt.Errorf("%s is already serving a shared session; close it there first", path)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// answers is a non-blocking dial: a listener accepts us (EINPROGRESS counts as
// accepted, the connection is then closed unread), and nothing listening gives
// ECONNREFUSED. Bounded by the socket's backlog rather than a clock, which is
// why agent-relay bounds its own dial at 3s.
func answers(path string) bool {
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return false
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.SetNonblock(fd, true); err != nil {
		return false
	}
	err = unix.Connect(fd, &unix.SockaddrUnix{Name: path})
	return err == nil || errors.Is(err, unix.EINPROGRESS)
}

// blame names which uid the peer check wanted and whose it found.
func blame(err error, uid int) string {
	if err != nil {
		return "cannot tell whose uid the peer is: " + err.Error()
	}
	return fmt.Sprintf("uid %d is not our uid %d", uid, os.Getuid())
}
