// Whoever can create, replace or write the share socket can put words into a
// chat and act in a workspace, so both ends verify the file and the peer uid.
package acpagent

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/invokr-agent/invokr/config"
)

const (
	socketDirName = "invokr"
	socketName    = "relay.sock"
	socketDirMode = 0o700
	socketMode    = 0o600
	// AF_UNIX names a path in 107 bytes; bind reports a bare EINVAL, so say
	// which end is wrong before the socket is created.
	maxSocketPath = 107
)

func SocketPath() string {
	runtime := config.Env("XDG_RUNTIME_DIR")
	if runtime == "" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".invokr", "run", socketName)
	}
	return filepath.Join(runtime, socketDirName, socketName)
}

type Socket struct {
	Path     string
	listener *net.UnixListener
}

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
	// Package net sets the backlog itself (SOMAXCONN), which is what keeps a
	// dial from being refused while another connection is served.
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return Socket{}, err
	}
	// Bind honours the umask; the socket must not be writable by group or others.
	if err := os.Chmod(path, socketMode); err != nil {
		_ = listener.Close()
		return Socket{}, err
	}
	return Socket{Path: path, listener: listener}, nil
}

var peerUID = peerCredentialUID

// Accept returns a connection only when the process behind it runs as this
// uid; an unreadable credential refuses, since that check is the whole
// trust boundary.
func (s Socket) Accept() (*net.UnixConn, error) {
	conn, err := s.listener.AcceptUnix()
	if err != nil {
		return nil, err
	}
	uid, failed := peerOf(conn)
	if failed != nil || uid != os.Getuid() {
		defer func() { _ = conn.Close() }()
		return nil, fmt.Errorf("%s: %s", s.Path, blame(failed, uid))
	}
	return conn, nil
}

// peerOf asks the connected socket whose uid is on the other end; a credential
// it cannot read is a refusal, never a pass.
func peerOf(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid int
	var failed error
	if control := raw.Control(func(fd uintptr) { uid, failed = peerUID(int(fd)) }); control != nil {
		return 0, control
	}
	if failed != nil {
		return 0, failed
	}
	return uid, nil
}

// Package net created the socket file and unlinks it on Close, which also
// unblocks a pending Accept; a bare descriptor would leave the watcher parked
// in accept() with the fd number free for reuse.
func (s Socket) Close() error { return s.listener.Close() }

// clearStale removes a socket file nobody answers; taking the path from one
// that answers would point another session's chat here.
func clearStale(path string) error {
	if answers(path) {
		return fmt.Errorf("%s is already serving a shared session; close it there first", path)
	}
	// Anything but a socket file here is someone else's: replacing it, or the
	// symlink's target, would follow their hand.
	found, err := os.Lstat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if found.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s is %s, not a socket file; refusing to replace it", path, found.Mode())
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// answers is a non-blocking dial: a listener accepts us (EINPROGRESS counts as
// accepted, then closed unread), and nothing listening gives ECONNREFUSED.
// Bounded by the socket's backlog, not a clock.
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
	// EAGAIN means the accept queue is full, so someone is answering it.
	return err == nil || errors.Is(err, unix.EINPROGRESS) || errors.Is(err, unix.EAGAIN)
}

func blame(err error, uid int) string {
	if err != nil {
		return "cannot tell whose uid the peer is: " + err.Error()
	}
	return fmt.Sprintf("uid %d is not our uid %d", uid, os.Getuid())
}
