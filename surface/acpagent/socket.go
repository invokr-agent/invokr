// Whoever can create, replace or write the share socket can put words into a
// chat and act in a workspace, so both ends verify the file and the peer uid.
package acpagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/support/fsutil"
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

func RelayConfigPath() string {
	base := config.Env("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "agent-relay", "config.json")
}

// SetRelaySocket writes every other answer back unchanged, so the relay's
// tokens survive: each value keeps its own JSON bytes (no number becomes a
// float, no re-escaping), and the file is replaced atomically at 0600 rather
// than written through a symlink. Member keys come back sorted.
func SetRelaySocket(path string) error {
	file := RelayConfigPath()
	found, err := os.Lstat(file)
	if err != nil || !found.Mode().IsRegular() {
		// One gate for both: nothing at this path is a config to write through.
		if err != nil {
			return fmt.Errorf("%s: %w; configure agent-relay first", file, err)
		}
		return fmt.Errorf("%s is %s, not a regular file; refusing to write it", file, found.Mode())
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("%s: %w; configure agent-relay first", file, err)
	}
	stored := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	if path == "" {
		delete(stored, "agent_socket")
	} else {
		value, err := encodeJSON(path)
		if err != nil {
			return err
		}
		stored["agent_socket"] = value
	}
	encoded, err := encodeJSON(stored)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(file, encoded, 0o600)
}

func Listen(path string) (*net.UnixListener, error) {
	if len(path) > maxSocketPath {
		return nil, fmt.Errorf("%s is %d bytes; a unix socket path fits %d", path, len(path), maxSocketPath)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, socketDirMode); err != nil {
		return nil, err
	}
	// MkdirAll applies the umask, and a directory others can write is a socket
	// anyone can replace.
	if err := os.Chmod(dir, socketDirMode); err != nil {
		return nil, err
	}
	if err := clearStale(path); err != nil {
		return nil, err
	}
	// Package net sets the backlog itself (SOMAXCONN), which is what keeps a
	// dial from being refused while another connection is served.
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	// Bind honours the umask; the socket must not be writable by group or others.
	if err := os.Chmod(path, socketMode); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

var peerUID = peerCredentialUID

// acceptPeer returns a connection only when the process behind it runs as this
// uid; an unreadable credential refuses, since that check is the whole
// trust boundary.
func acceptPeer(listener *net.UnixListener, path string) (*net.UnixConn, error) {
	conn, err := listener.AcceptUnix()
	if err != nil {
		return nil, err
	}
	var uid int
	var failed error
	if raw, rawErr := conn.SyscallConn(); rawErr != nil {
		failed = rawErr
	} else if ctlErr := raw.Control(func(fd uintptr) { uid, failed = peerUID(int(fd)) }); ctlErr != nil {
		failed = ctlErr
	}
	if failed != nil || uid != os.Getuid() {
		defer func() { _ = conn.Close() }()
		if failed != nil {
			return nil, fmt.Errorf("%s: cannot tell whose uid the peer is: %w", path, failed)
		}
		return nil, fmt.Errorf("%s: peer uid %d is not our uid %d", path, uid, os.Getuid())
	}
	return conn, nil
}

// clearStale removes a socket file nobody answers; taking the path from one
// that answers would point another session's chat here.
func clearStale(path string) error {
	if answers(path) {
		return fmt.Errorf("%s is already serving a shared session; close it there first", path)
	}
	// Anything but a socket file here is someone else's: replacing it, or the
	// symlink's target, would follow their hand.
	found, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if found.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s is %s, not a socket file; refusing to replace it", path, found.Mode())
	}
	return os.Remove(path)
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

// encodeJSON writes the document with HTML escaping left off: agent-relay's
// own answers may hold < > & in tokens and headers, and a socket path may too.
// The whole document goes through this encoder because json re-escapes a value
// it is handed as RawMessage.
func encodeJSON(value any) (json.RawMessage, error) {
	buf := bytes.Buffer{}
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return buf.Bytes(), err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
