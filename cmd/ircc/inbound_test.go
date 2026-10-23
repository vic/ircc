package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"ircc/internal/model"
	"ircc/internal/peer"
	"ircc/internal/registry"
)

// TestInboundHelperSender is the sender child (the existing TestMain falls
// through to m.Run, so a helper test replaces a second TestMain). It waits for
// one line on stdin, then sends INBOUND_FRAME to INBOUND_SEND.
func TestInboundHelperSender(t *testing.T) {
	sock := os.Getenv("INBOUND_SEND")
	if sock == "" {
		t.Skip("helper for sendInbound")
	}
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if err := peer.Send(sock, []byte(os.Getenv("INBOUND_FRAME")+"\n")); err != nil {
		t.Fatal(err)
	}
}

type emitted struct {
	ch   string
	l    model.Line
	note bool // came through the note callback (focused channel)
}

type inFix struct {
	sock string
	got  chan emitted
	live *liveSet
}

// startInbound serves a listener in a short /tmp dir; live is a mutable slice
// the test fills once the child pid is known.
func startInbound(t *testing.T) inFix {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "in")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock, err := peer.EndpointPath(dir, "u")
	if err != nil {
		t.Fatal(err)
	}
	l, err := peer.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	live := &liveSet{}
	f := inFix{sock: sock, got: make(chan emitted, 8), live: live}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		serveInbound(ctx, l, live.get,
			func(ch string, ln model.Line) { f.got <- emitted{ch: ch, l: ln} },
			func(ln model.Line) { f.got <- emitted{l: ln, note: true} })
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("serveInbound did not stop on ctx done")
		}
	})
	return f
}

// sendFromChild starts the helper, lets mkLive register the child's pid as a
// live entry, then releases the send. It returns once the child exits.
func (f inFix) sendFromChild(t *testing.T, frame string, registerChild func(pid int)) {
	t.Helper()
	c := exec.Command(os.Args[0], "-test.run=^TestInboundHelperSender$")
	c.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0", "INBOUND_SEND="+f.sock, "INBOUND_FRAME="+frame)
	in, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(5*time.Second, func() { _ = c.Process.Kill() })
	defer timer.Stop()
	registerChild(c.Process.Pid)
	if _, err := in.Write([]byte("go\n")); err != nil {
		t.Fatal(err)
	}
	in.Close()
	if err := c.Wait(); err != nil {
		t.Fatalf("child: %v", err)
	}
}

func (f inFix) next(t *testing.T) emitted {
	t.Helper()
	select {
	case e := <-f.got:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("nothing emitted")
		return emitted{}
	}
}

func (f inFix) none(t *testing.T) {
	t.Helper()
	select {
	case e := <-f.got:
		t.Fatalf("unexpected emit %+v", e)
	case <-time.After(100 * time.Millisecond):
	}
}

func frameFor(t *testing.T, body string) string {
	t.Helper()
	from, err := peer.ParseAddr("uds:/tmp/x/ircc-u.sock")
	if err != nil {
		t.Fatal(err)
	}
	fr, err := peer.NewUserFrame(from, "alpha", peer.Prompting, body)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(string(fr.Bytes()), "\n")
}

func TestInboundLandsInSessionChannel(t *testing.T) {
	f := startInbound(t)
	f.sendFromChild(t, frameFor(t, "hi there"), func(pid int) {
		f.live.set([]registry.Entry{{PID: pid, Name: "Alpha"}, {PID: 1, Name: "Other"}})
	})
	e := f.next(t)
	if e.ch != "#alpha" || e.l.Nick != "Alpha" || e.l.Text != "hi there" || e.l.Dim {
		t.Fatalf("got %+v", e)
	}
}

func TestInboundAgentNick(t *testing.T) {
	f := startInbound(t)
	f.sendFromChild(t, frameFor(t, `<agent-message from="scribe-1"> done`), func(pid int) {
		f.live.set([]registry.Entry{{PID: pid, Name: "Alpha"}})
	})
	if e := f.next(t); e.ch != "#alpha" || e.l.Nick != "scribe-1@Alpha" {
		t.Fatalf("got %+v", e)
	}
}

func TestInboundUnknownPidDropped(t *testing.T) {
	f := startInbound(t)
	f.sendFromChild(t, frameFor(t, "hi"), func(pid int) {
		f.live.set([]registry.Entry{{PID: pid + 1, Name: "Alpha"}}) // not the sender
	})
	e := f.next(t)
	if !e.note || e.ch != "" || !e.l.Dim || strings.Contains(e.l.Text, "hi") {
		t.Fatalf("want dim note (note callback), no content: %+v", e)
	}
	f.none(t)
}

func TestInboundMalformedDroppedWithNote(t *testing.T) {
	f := startInbound(t)
	f.sendFromChild(t, `{"not":"a frame"}`, func(pid int) {
		f.live.set([]registry.Entry{{PID: pid, Name: "Alpha"}})
	})
	e := f.next(t)
	if !e.note || e.ch != "" || !e.l.Dim || strings.Contains(e.l.Text, "not") {
		t.Fatalf("got %+v", e)
	}
	f.none(t)
}

// liveSet is the registry snapshot the test swaps in once the child pid is known.
type liveSet struct {
	mu sync.Mutex
	es []registry.Entry
}

func (s *liveSet) get() []registry.Entry   { s.mu.Lock(); defer s.mu.Unlock(); return s.es }
func (s *liveSet) set(es []registry.Entry) { s.mu.Lock(); s.es = es; s.mu.Unlock() }

func TestInboundMultipleFramesPerConn(t *testing.T) {
	f := startInbound(t)
	f.sendFromChild(t, frameFor(t, "one")+"\n"+frameFor(t, "two"), func(pid int) {
		f.live.set([]registry.Entry{{PID: pid, Name: "Alpha"}})
	})
	if e := f.next(t); e.l.Text != "one" {
		t.Fatalf("first: %+v", e)
	}
	if e := f.next(t); e.l.Text != "two" {
		t.Fatalf("second: %+v", e)
	}
}

func TestRateLimitedNotes(t *testing.T) {
	now := time.Unix(1000, 0)
	n := 0
	note := rateLimited(func(model.Line) { n++ }, time.Second, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		note(model.Line{})
	}
	if n != 1 {
		t.Fatalf("burst gave %d notes, want 1", n)
	}
	now = now.Add(time.Second)
	note(model.Line{})
	if n != 2 {
		t.Fatalf("after 1s: %d notes, want 2", n)
	}
}

func TestServeLoopAcceptRetry(t *testing.T) {
	calls, notes := 0, 0
	accept := func() (*peer.Conn, error) {
		calls++
		if calls <= 2 {
			return nil, errors.New("boom")
		}
		return nil, fmt.Errorf("accept: %w", net.ErrClosed)
	}
	serveLoop(context.Background(), accept, func(*peer.Conn) { t.Error("no conn expected") },
		func(model.Line) { notes++ }, time.Millisecond)
	if calls != 3 || notes != 2 {
		t.Fatalf("calls=%d notes=%d, want 3 and 2 (closed listener ends quietly)", calls, notes)
	}
	// ctx cancelled while backing off: returns.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		serveLoop(ctx, func() (*peer.Conn, error) { return nil, errors.New("boom") },
			func(*peer.Conn) {}, func(model.Line) {}, time.Hour)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serveLoop ignored ctx during backoff")
	}
}

func TestInboundOversizeAfterGoodFrame(t *testing.T) {
	f := startInbound(t)
	f.live.set([]registry.Entry{{PID: os.Getpid(), Name: "Alpha"}})
	c, err := net.Dial("unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	big := strings.Repeat("x", 1<<20+1024) // over the 1 MiB frame cap, no newline needed
	if _, err := c.Write([]byte(frameFor(t, "ok") + "\n" + big + "\n")); err != nil {
		t.Fatal(err)
	}
	if e := f.next(t); e.l.Text != "ok" {
		t.Fatalf("good frame: %+v", e)
	}
	e := f.next(t)
	if !e.note || !strings.Contains(e.l.Text, "too long") || strings.Contains(e.l.Text, "xxx") {
		t.Fatalf("want content-free note, got %+v", e)
	}
	f.none(t)
}

func TestInboundIdleAfterGoodFrameNoNote(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "in")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock, err := peer.EndpointPath(dir, "u")
	if err != nil {
		t.Fatal(err)
	}
	l, err := peer.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan emitted, 8)
	live := func() []registry.Entry { return []registry.Entry{{PID: os.Getpid(), Name: "Alpha"}} }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		serveLoop(ctx, l.Accept, func(c *peer.Conn) {
			handleConn(c, 100*time.Millisecond, live,
				func(ch string, ln model.Line) { got <- emitted{ch: ch, l: ln} },
				func(ln model.Line) { got <- emitted{l: ln, note: true} })
		}, func(model.Line) {}, time.Millisecond)
		close(done)
	}()
	t.Cleanup(func() { cancel(); l.Close(); <-done })
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close() // stays open, idle, past the deadline
	if _, err := c.Write([]byte(frameFor(t, "ok") + "\n")); err != nil {
		t.Fatal(err)
	}
	if e := <-got; e.note || e.l.Text != "ok" {
		t.Fatalf("good frame: %+v", e)
	}
	select {
	case e := <-got:
		t.Fatalf("idle deadline produced %+v, want nothing", e)
	case <-time.After(400 * time.Millisecond):
	}
}
