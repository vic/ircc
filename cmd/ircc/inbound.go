package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"ircc/internal/model"
	"ircc/internal/peer"
	"ircc/internal/registry"
)

// inboundTimeout bounds one connection: a silent peer cannot hold a goroutine.
const inboundTimeout = 2 * time.Second

// noteEvery bounds how often bad-connection notes reach the UI.
const noteEvery = time.Second

// acceptBackoff is the first wait after a failed Accept; it doubles up to acceptBackoffMax.
const (
	acceptBackoff    = 50 * time.Millisecond
	acceptBackoffMax = 2 * time.Second
)

// serveInbound is the endpoint inbound path (DESIGN D:268-272). It accepts on l
// until ctx is done, then closes l and waits for open connections. Per connection:
// every frame line (m22), PeerPID -> live registry entry -> that session's `#`
// channel, and line(channel, line). Content is only displayed, never executed.
//
// line(ch, l) delivers a session line; note(l) is a dim note for the currently focused channel
// (unknown sender pid, malformed frame, accept failure); notes carry no frame
// content and are limited to one per noteEvery. A line
// from a session agent has Nick "<agent>@<session>" when the body wraps an
// <agent-message from="...">, else the session's name.
func serveInbound(ctx context.Context, l *peer.Listener, live func() []registry.Entry, line func(ch string, l model.Line), note func(l model.Line)) {
	stop := context.AfterFunc(ctx, func() { _ = l.Close() })
	defer stop()
	note = rateLimited(note, noteEvery, time.Now)
	serveLoop(ctx, l.Accept, func(c *peer.Conn) { handleInbound(c, live, line, note) }, note, acceptBackoff)
}

// serveLoop accepts until ctx is done or the listener is closed. Any other Accept
// error is noted and retried with doubling backoff (reset by a good Accept).
// It waits for open connections before returning.
func serveLoop(ctx context.Context, accept func() (*peer.Conn, error), handle func(*peer.Conn), note func(model.Line), backoff time.Duration) {
	var wg sync.WaitGroup
	defer wg.Wait()
	wait := backoff
	for {
		c, err := accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return // closed by ctx or by the owner
			}
			noteText(note, "inbound: accept failed, retrying")
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = min(wait*2, acceptBackoffMax)
			continue
		}
		wait = backoff
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer c.Close()
			handle(c)
		}()
	}
}

// rateLimited passes at most one note per every; the rest are dropped.
func rateLimited(note func(model.Line), every time.Duration, now func() time.Time) func(model.Line) {
	var mu sync.Mutex
	var last time.Time
	return func(l model.Line) {
		mu.Lock()
		t := now()
		ok := last.IsZero() || t.Sub(last) >= every
		if ok {
			last = t
		}
		mu.Unlock()
		if ok {
			note(l)
		}
	}
}

func noteText(note func(model.Line), text string) {
	note(model.Line{At: time.Now(), Text: text, Dim: true})
}

func handleInbound(c *peer.Conn, live func() []registry.Entry, line func(string, model.Line), note func(model.Line)) {
	handleConn(c, inboundTimeout, live, line, note)
}

func handleConn(c *peer.Conn, timeout time.Duration, live func() []registry.Entry, line func(string, model.Line), note func(model.Line)) {
	if err := c.SetDeadline(time.Now().Add(timeout)); err != nil {
		noteText(note, "inbound: deadline failed, connection dropped")
		return
	}
	pid, err := c.PeerPID()
	if err != nil {
		noteText(note, "inbound: sender credentials unreadable, dropped")
		return
	}
	sc := c.Lines()
	frames := 0
	for sc.Scan() {
		frames++
		handleFrame(pid, sc.Bytes(), live, line, note)
	}
	// A peer may keep the connection open after valid frames (m22): the deadline is not an error.
	if err := sc.Err(); err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
		noteText(note, fmt.Sprintf("inbound: frame too long / read error from pid %d, dropped", pid))
	}
	if frames == 0 {
		noteText(note, fmt.Sprintf("inbound: no frame from pid %d, dropped", pid))
	}
}

func handleFrame(pid int, b []byte, live func() []registry.Entry, line func(string, model.Line), note func(model.Line)) {
	in, err := peer.ParseFrame(b)
	if err != nil {
		noteText(note, fmt.Sprintf("inbound: unparsed frame from pid %d (%v): %s", pid, err, clip(string(b), 300)))
		return
	}
	var sessions []model.Session
	var from *registry.Entry
	entries := live()
	for i, e := range entries {
		sessions = append(sessions, model.Session{PID: e.PID, Name: e.Name, Cwd: e.Cwd})
		if e.PID == pid {
			from = &entries[i]
		}
	}
	if from == nil {
		noteText(note, fmt.Sprintf("inbound: frame from unknown pid %d, dropped", pid))
		return
	}
	ch := model.ChanNames(sessions)[pid]
	nick := from.Name
	if nick == "" {
		nick = strings.TrimPrefix(ch, "#")
	}
	if in.AgentNick != "" {
		nick = in.AgentNick + "@" + nick
	}
	line(ch, model.Line{At: time.Now(), Nick: nick, Text: in.Body})
}

// clip shortens s to n bytes for a note.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
