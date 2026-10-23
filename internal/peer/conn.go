package peer

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

const (
	sendTimeout = 5 * time.Second
	maxFrame    = 1 << 20 // 1 MiB
)

// Send writes b to the unix socket sock and closes (D:283). b is written as given.
func Send(sock string, b []byte) error {
	c, err := net.DialTimeout("unix", sock, sendTimeout)
	if err != nil {
		return fmt.Errorf("peer: dial %s: %w", sock, err)
	}
	if err := c.SetDeadline(time.Now().Add(sendTimeout)); err != nil {
		c.Close()
		return fmt.Errorf("peer: deadline: %w", err)
	}
	if _, err := c.Write(b); err != nil {
		c.Close()
		return fmt.Errorf("peer: write %s: %w", sock, err)
	}
	return c.Close()
}

// Listener is an endpoint socket, mode 0600. chmod after bind leaves a short
// window (GAP-4). Listen unlinks its own stale socket (D:369-370).
type Listener struct {
	l    *net.UnixListener
	once sync.Once
	err  error
}

// ErrInUse: a live socket already owns the path.
var ErrInUse = errors.New("in use")

// clearStale removes a leftover socket at path if nothing listens on it. A
// live socket or a non-socket file is an error and is never unlinked.
func clearStale(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("peer: stat %s: %w", path, err)
	}
	if fi.Mode().Type() != fs.ModeSocket {
		return fmt.Errorf("peer: %s exists and is not a socket", path)
	}
	c, err := net.DialTimeout("unix", path, sendTimeout)
	if err == nil {
		c.Close()
		return fmt.Errorf("peer: %s: %w", path, ErrInUse)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("peer: probe %s: %w", path, err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("peer: unlink stale %s: %w", path, err)
	}
	return nil
}

func Listen(path string) (*Listener, error) {
	if err := clearStale(path); err != nil {
		return nil, err
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("peer: listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, fmt.Errorf("peer: chmod %s: %w", path, err)
	}
	return &Listener{l: l}, nil
}

func (l *Listener) Accept() (*Conn, error) {
	c, err := l.l.AcceptUnix()
	if err != nil {
		return nil, fmt.Errorf("peer: accept: %w", err)
	}
	return &Conn{c}, nil
}

// Close closes the listener; the Go runtime unlinks the socket file. Only the
// first call acts; later calls return its result (nil on success).
func (l *Listener) Close() error {
	l.once.Do(func() { l.err = l.l.Close() })
	return l.err
}

type Conn struct{ c *net.UnixConn }

// PeerPID reads SO_PEERCRED of the connection (m14).
func (c *Conn) PeerPID() (int, error) {
	rc, err := c.c.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("peer: syscallconn: %w", err)
	}
	var ucred *syscall.Ucred
	var gerr error
	if err := rc.Control(func(fd uintptr) {
		ucred, gerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, fmt.Errorf("peer: control: %w", err)
	}
	if gerr != nil {
		return 0, fmt.Errorf("peer: SO_PEERCRED: %w", gerr)
	}
	return int(ucred.Pid), nil
}

// Lines yields one frame per line (m22).
func (c *Conn) Lines() *bufio.Scanner {
	sc := bufio.NewScanner(c.c)
	sc.Buffer(nil, maxFrame) // longer line: Scan false, Err() = ErrTooLong
	return sc
}

func (c *Conn) SetDeadline(t time.Time) error { return c.c.SetDeadline(t) }

func (c *Conn) Close() error { return c.c.Close() }
