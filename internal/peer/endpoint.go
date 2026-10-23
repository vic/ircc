package peer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"

	"ircc/internal/registry"
)

// roomPrefix marks a room name (RFC 2811 local channel, D:169-175).
const roomPrefix = "&"

// Endpoint is a Claude socket ircc owns: a listener plus a registry entry (D:169-195).
// The caller owns the keeper's spawning (rooms.SpawnKeeper from a locked OS thread,
// D:193); peer must not import rooms.
type Endpoint struct {
	Name    string
	Addr    Addr
	l       *Listener
	keeper  *os.Process // nil for the user endpoint
	sessDir string
	sock    string
	entry   registry.Entry // what we wrote; Close removes it only if unchanged
	once    sync.Once
}

// OpenUser opens the user endpoint under the given name; the entry takes ircc's pid.
// The name is a parameter on purpose: the clash with a session of the same name is
// an open Vic gate (GAP-6).
func OpenUser(socksDir, sessDir, procRoot, user, pidDomain string) (*Endpoint, error) {
	if user == "" || strings.HasPrefix(user, roomPrefix) {
		return nil, fmt.Errorf("peer: user endpoint name %q: want non-empty without %q", user, roomPrefix)
	}
	if registry.HasOwn(sessDir, os.Getpid()) {
		return nil, fmt.Errorf("peer: user endpoint %q: ircc already has an entry for pid %d", user, os.Getpid())
	}
	return open(socksDir, sessDir, procRoot, user, os.Getpid(), nil, pidDomain)
}

// OpenRoom opens the endpoint of room name (&<room>); the entry takes keeper's pid.
func OpenRoom(socksDir, sessDir, procRoot, name string, keeper *os.Process, pidDomain string) (*Endpoint, error) {
	ep, err := openRoom(socksDir, sessDir, procRoot, name, keeper, pidDomain)
	if err != nil && keeper != nil {
		err = errors.Join(err, reapKeeper(keeper)) // open owns the keeper on failure
	}
	return ep, err
}

func openRoom(socksDir, sessDir, procRoot, name string, keeper *os.Process, pidDomain string) (*Endpoint, error) {
	if !strings.HasPrefix(name, roomPrefix) || len(name) == len(roomPrefix) {
		return nil, fmt.Errorf("peer: room name %q: want %q<room>", name, roomPrefix)
	}
	if keeper == nil || keeper.Pid <= 0 {
		return nil, errors.New("peer: room endpoint needs a keeper process")
	}
	return open(socksDir, sessDir, procRoot, name, keeper.Pid, keeper, pidDomain)
}

func open(socksDir, sessDir, procRoot, name string, pid int, keeper *os.Process, pidDomain string) (*Endpoint, error) {
	sock, err := EndpointPath(socksDir, name)
	if err != nil {
		return nil, err
	}
	addr, err := PathAddr(sock)
	if err != nil {
		return nil, err
	}
	start, err := registry.ProcStart(procRoot, pid)
	if err != nil {
		return nil, fmt.Errorf("peer: endpoint %q: %w", name, err)
	}
	l, err := Listen(sock)
	if err != nil {
		return nil, err
	}
	e := registry.Entry{PID: pid, Name: name, Kind: registry.Bg, Status: registry.Idle, SocketPath: sock,
		PeerProtocol: 1, PeerFeatures: []string{}, ProcStart: start, PIDDomain: pidDomain}
	if err := registry.WriteOwn(sessDir, e); err != nil {
		return nil, errors.Join(fmt.Errorf("peer: endpoint %q: %w", name, err), l.Close(), removeSock(sock))
	}
	return &Endpoint{Name: name, Addr: addr, l: l, keeper: keeper, sessDir: sessDir, sock: sock, entry: e}, nil
}

// Listener returns the endpoint's socket listener.
func (e *Endpoint) Listener() *Listener { return e.l }

// Close removes the entry (only if it is still ours), kills and reaps the keeper
// (D:195), then closes the listener and unlinks the socket. Idempotent: only the
// first call acts; later calls return nil, so a reused pid's foreign entry is safe.
func (e *Endpoint) Close() error {
	var err error
	e.once.Do(func() {
		errs := []error{registry.RemoveOwnIf(e.sessDir, e.entry)}
		if e.keeper != nil {
			errs = append(errs, reapKeeper(e.keeper))
		}
		errs = append(errs, e.l.Close(), removeSock(e.sock))
		err = errors.Join(errs...)
	})
	return err
}

// reapKeeper kills and waits for k; an already finished keeper is fine.
func reapKeeper(k *os.Process) error {
	if k.Pid <= 0 {
		return nil
	}
	var errs []error
	if err := k.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		errs = append(errs, fmt.Errorf("peer: kill keeper: %w", err))
	}
	if _, err := k.Wait(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		errs = append(errs, fmt.Errorf("peer: wait keeper: %w", err))
	}
	return errors.Join(errs...)
}

func removeSock(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("peer: remove %s: %w", p, err)
	}
	return nil
}
