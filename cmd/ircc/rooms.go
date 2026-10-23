package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"

	"ircc/internal/peer"
	"ircc/internal/rooms"
	"ircc/internal/ui"
)

// errShutdown means the room runtime no longer spawns keepers.
var errShutdown = errors.New("rooms: runtime shut down")

type spawnRes struct {
	p   *os.Process
	err error
}

// spawnReq asks the spawner goroutine for one keeper; the answer comes on reply.
type spawnReq struct{ reply chan spawnRes }

// roomRT owns the room endpoints. Keepers are children of ONE goroutine locked to
// its OS thread for as long as ircc lives: Pdeathsig fires when the parent thread
// exits, so the spawning thread must outlive every room (DESIGN D:193).
// mu guards rooms and eps only; spawning, sockets and files happen outside it.
type roomRT struct {
	dir, socks, sess, procRoot, self string
	spawnKeeper                      func(self string) (*os.Process, error)
	// onOpen runs after a room endpoint opens (the runner serves it); nil in tests.
	onOpen func(name rooms.Name, ep *peer.Endpoint)

	mu    sync.Mutex
	rooms map[string]rooms.Room
	eps   map[string]*peer.Endpoint

	spawn   chan spawnReq
	quit    chan struct{}
	stopped chan struct{} // closed when the spawner goroutine has exited
	once    sync.Once
}

// newRoomRT starts the spawner goroutine. dir holds the room files; socks and
// sess are the Claude cc-socks and sessions dirs; self is this executable.
func newRoomRT(dir, socks, sess, procRoot, self string) *roomRT {
	r := &roomRT{dir: dir, socks: socks, sess: sess, procRoot: procRoot, self: self,
		spawnKeeper: rooms.SpawnKeeper,
		rooms:       map[string]rooms.Room{}, eps: map[string]*peer.Endpoint{},
		spawn: make(chan spawnReq), quit: make(chan struct{}), stopped: make(chan struct{})}
	go r.spawner()
	return r
}

// spawner never calls runtime.UnlockOSThread: when it returns, its thread exits
// and Pdeathsig kills any keeper still running.
func (r *roomRT) spawner() {
	runtime.LockOSThread()
	defer close(r.stopped)
	for {
		select {
		case req := <-r.spawn:
			p, err := r.spawnKeeper(r.self)
			req.reply <- spawnRes{p, err}
		case <-r.quit:
			return
		}
	}
}

func (r *roomRT) newKeeper() (*os.Process, error) {
	req := spawnReq{reply: make(chan spawnRes, 1)}
	select {
	case r.spawn <- req:
	case <-r.quit:
		return nil, errShutdown
	}
	res := <-req.reply
	return res.p, res.err
}

func (r *roomRT) file(name rooms.Name) string {
	return filepath.Join(r.dir, peer.EncName(name.String())+".json")
}

// load opens every saved room. A bad room file or a room that fails to open is
// reported, the rest still open.
func (r *roomRT) load() []error {
	saved, errs := rooms.Load(r.dir)
	for _, rm := range saved {
		r.mu.Lock()
		r.rooms[rm.Name.String()] = rm
		r.mu.Unlock()
		if err := r.open(rm.Name, ""); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// open starts a keeper for name, opens its endpoint and saves the room file.
// Opening an open room is a no-op. The zero Name is refused before any spawn.
func (r *roomRT) open(name rooms.Name, pidDomain string) error {
	key := name.String()
	if key == "" {
		return errors.New("rooms: open: zero room name")
	}
	r.mu.Lock()
	_, open := r.eps[key]
	r.mu.Unlock()
	if open {
		return nil
	}
	r.mu.Lock()
	socks := r.socks
	r.mu.Unlock()
	if socks == "" {
		return fmt.Errorf("rooms: open %s: socks dir unknown until a session is live", key)
	}
	k, err := r.newKeeper()
	if err != nil {
		return fmt.Errorf("rooms: open %s: %w", key, err)
	}
	ep, err := peer.OpenRoom(socks, r.sess, r.procRoot, key, k, pidDomain) // reaps k on failure
	if err != nil {
		return fmt.Errorf("rooms: open %s: %w", key, err)
	}
	r.mu.Lock()
	rm, known := r.rooms[key]
	if !known {
		rm = rooms.Room{Name: name}
	}
	r.rooms[key], r.eps[key] = rm, ep
	r.mu.Unlock()
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return fmt.Errorf("rooms: open %s: %w", key, err)
	}
	if err := rooms.Save(r.dir, rm); err != nil {
		return fmt.Errorf("rooms: open %s: %w", key, err)
	}
	if r.onOpen != nil {
		r.onOpen(name, ep)
	}
	return nil
}

// setSocks sets the cc-socks dir once it is known (first live session, m26).
func (r *roomRT) setSocks(d string) {
	r.mu.Lock()
	r.socks = d
	r.mu.Unlock()
}

// closeRoom closes name's endpoint (entry, keeper kill+reap, socket) and deletes its file.
func (r *roomRT) closeRoom(name rooms.Name) error {
	key := name.String()
	r.mu.Lock()
	ep, ok := r.eps[key]
	delete(r.eps, key)
	delete(r.rooms, key)
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("rooms: close %s: no such open room", key)
	}
	errs := []error{ep.Close()}
	if err := os.Remove(r.file(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("rooms: remove %s: %w", key, err))
	}
	if err := rooms.RemoveLog(r.dir, name); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// names lists the open rooms, sorted.
func (r *roomRT) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.eps))
	for k := range r.eps {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// room returns the stored room (topic, members) of an open room.
func (r *roomRT) room(name rooms.Name) (rooms.Room, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rm, ok := r.rooms[name.String()]
	return rm, ok
}

// endpoint returns the open endpoint of name.
func (r *roomRT) endpoint(name rooms.Name) (*peer.Endpoint, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ep, ok := r.eps[name.String()]
	return ep, ok
}

// shutdown closes every room endpoint (files stay: rooms reopen on the next run),
// then stops the spawner. Idempotent.
func (r *roomRT) shutdown() error {
	var err error
	r.once.Do(func() {
		r.mu.Lock()
		eps := r.eps
		r.eps = map[string]*peer.Endpoint{}
		r.mu.Unlock()
		var errs []error
		for _, ep := range eps {
			errs = append(errs, ep.Close())
		}
		close(r.quit)
		<-r.stopped
		err = errors.Join(errs...)
	})
	return err
}

// setRoom stores r (topic, members) for an open room: saved to disk first, memory
// changes only if that worked.
func (r *roomRT) setRoom(rm rooms.Room) error {
	key := rm.Name.String()
	r.mu.Lock()
	_, open := r.eps[key]
	r.mu.Unlock()
	if !open {
		return fmt.Errorf("rooms: set %s: no such open room", key)
	}
	if err := rooms.Save(r.dir, rm); err != nil {
		return err
	}
	r.mu.Lock()
	r.rooms[key] = rm
	r.mu.Unlock()
	return nil
}

// roomsDir is where room state lives: $XDG_STATE_HOME/ircc/rooms (DESIGN :237).
func roomsDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "ircc", "rooms"), nil
}

// loadRooms opens the saved rooms once the socks dir is known and shows them.
func (r *runner) loadRooms() {
	for _, err := range r.rt.load() {
		r.emit(ui.NoteMsg{L: dim("rooms: " + err.Error())})
	}
	for _, m := range roomMsgs(r.rt, r.rt.names()) {
		r.emit(m)
	}
}
