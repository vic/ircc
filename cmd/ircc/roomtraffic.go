package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"ircc/internal/model"
	"ircc/internal/peer"
	"ircc/internal/rooms"
	"ircc/internal/transcript"
	"ircc/internal/ui"
)

var errNoRooms = errors.New("room runtime not started")

// roomLine is a line in room ch, shown by the UI.
func (r *runner) roomLine(ch, nick, text string, dimmed bool) {
	now := time.Now()
	r.emit(ui.LineMsg{Ch: ch, L: model.Line{At: now, Nick: nick, Text: text, Dim: dimmed}})
	name, err := rooms.ParseName(ch)
	if err != nil || r.rt == nil || nick == "" {
		return // not a room line: # channels keep no disk scrollback
	}
	if err := rooms.AppendLine(r.rt.dir, name, rooms.LogLine{Time: now, Nick: nick, Text: text}); err != nil {
		r.emit(ui.NoteMsg{L: model.Line{At: now, Dim: true, Text: "scrollback not saved: " + err.Error()}})
	}
}

func (r *runner) sendTo(sock string, f peer.Frame) error { return r.sendFrame(sock, f.Bytes()) }

// endsFor maps room members to live sockets and permission classes. A member that
// is offline or whose mode peer.ClassOf rejects is left out (fail closed) and
// listed in skipped. A teammate (n@p) shares its parent's session, so its socket.
func (r *runner) endsFor(room rooms.Room) (ends []rooms.Endpoint, skipped []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range room.Members {
		pid := 0
		for p, e := range r.live {
			if e.SessionID == m.Session && (pid == 0 || p < pid) {
				pid = p
			}
		}
		if pid == 0 {
			skipped = append(skipped, m.Nick+" (offline)")
			continue
		}
		mode := ""
		if t := r.tails[pid]; t != nil {
			mode = t.mode
		}
		class, err := peer.ClassOf(mode)
		if err != nil {
			skipped = append(skipped, m.Nick+" (mode unknown)")
			continue
		}
		ends = append(ends, rooms.Endpoint{Member: m, Sock: r.live[pid].SocketPath, Class: class})
	}
	return ends, skipped
}

// invitable lists every live session as a main-agent member, and its subagents as
// <agent>@<session> members (D:229 nick =
// channel name without "#"); /invite picks its target from it. Unknown mode
// fails closed: that session is left out.
func (r *runner) invitable(rooms.Room) []rooms.Endpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []rooms.Endpoint
	for pid, e := range r.live {
		mode := ""
		if t := r.tails[pid]; t != nil {
			mode = t.mode
		}
		class, err := peer.ClassOf(mode)
		if err != nil || e.SessionID == "" {
			continue
		}
		nick := strings.TrimPrefix(r.names[pid], "#")
		out = append(out, rooms.Endpoint{Member: rooms.Member{Session: e.SessionID, Nick: nick}, Sock: e.SocketPath, Class: class})
		if t := r.tails[pid]; t != nil {
			for _, ag := range t.list { // teammates: <agent>@<session> (D:229), reached via the session's socket
				m := rooms.Member{Session: e.SessionID, Nick: ag.Nick + "@" + nick}
				out = append(out, rooms.Endpoint{Member: m, Sock: e.SocketPath, Class: class})
			}
		}
	}
	return out
}

func (r *runner) openRoom(name rooms.Name) (rooms.Room, *peer.Endpoint, error) {
	if r.rt == nil {
		return rooms.Room{}, nil, errNoRooms
	}
	room, ok := r.rt.room(name)
	ep, ok2 := r.rt.endpoint(name)
	if !ok || !ok2 {
		return rooms.Room{}, nil, fmt.Errorf("no open room %s", name)
	}
	return room, ep, nil
}

func (r *runner) skipNote(room rooms.Name, skipped []string) {
	if len(skipped) > 0 {
		r.emit(ui.NoteMsg{L: model.Line{At: time.Now(), Dim: true, Text: fmt.Sprintf("%s: not sent to %s", room, strings.Join(skipped, ", "))}})
	}
}

// say sends a line you typed in room channel ch to every member and shows it.
func (r *runner) say(ch, text string) error {
	name, err := rooms.ParseName(ch)
	if err != nil {
		return err
	}
	room, ep, err := r.openRoom(name)
	if err != nil {
		return err
	}
	w, err := rooms.UserWriter(r.user)
	if err != nil {
		return err
	}
	ends, skipped := r.endsFor(room)
	r.skipNote(name, skipped)
	err = rooms.Fanout(r.sendTo, ep.Addr, room, ends, w, text)
	r.roomLine(ch, r.user, text, false)
	return err
}

// onRoomFrame handles one frame that reached room name's endpoint from process
// pid. Who wrote it is decided by pid (peer credentials), never by the claimed
// name (D:246-250). Non-members are shown marked and not fanned out.
func (r *runner) onRoomFrame(name rooms.Name, pid int, b []byte) {
	room, ep, err := r.openRoom(name)
	if err != nil {
		return
	}
	in, err := peer.ParseFrame(b)
	if err != nil {
		r.emit(ui.NoteMsg{L: model.Line{At: time.Now(), Dim: true, Text: fmt.Sprintf("%s: unparsed frame from pid %d (%v): %s", name, pid, err, clip(string(b), 300))}})
		return
	}
	parent, ok := rooms.Identify(pid, r.liveCopy(), room)
	if !ok {
		r.roomLine(name.String(), "[non-member]", in.Body, true)
		return
	}
	sender := rooms.ResolveAgent(room, parent, in.AgentNick)
	r.roomLine(name.String(), sender.Nick, in.Body, false)

	r.mu.Lock()
	home, mode := r.names[pid], ""
	if t := r.tails[pid]; t != nil {
		mode = t.mode
	}
	r.mu.Unlock()
	if home != "" { // D:262: the line also shows, dim and tagged, in the member's own channel
		r.roomLine(home, "", fmt.Sprintf("[%s] <%s> %s", name, sender.Nick, in.Body), true)
	}
	class, err := peer.ClassOf(mode)
	if err == nil {
		var w rooms.Writer
		if w, err = rooms.AgentWriter(sender.Nick, class); err == nil {
			ends, skipped := r.endsFor(room)
			r.skipNote(name, skipped)
			err = rooms.Fanout(r.sendTo, ep.Addr, room, ends, w, in.Body)
		}
	}
	if err != nil { // unknown sender mode fails closed: nothing was sent
		r.emit(ui.NoteMsg{L: model.Line{At: time.Now(), Dim: true, Text: fmt.Sprintf("%s: not relayed from %s: %v", name, sender.Nick, err)}})
	}
}

// onEvent marks a room line held when pid's transcript says a frame from a room
// endpoint of ours was held at that member (D:256-257).
func (r *runner) onEvent(pid int, ev transcript.Event) {
	if r.rt == nil || !ev.Held {
		return
	}
	r.mu.Lock()
	e, live := r.live[pid]
	r.mu.Unlock()
	if !live {
		return
	}
	for _, key := range r.rt.names() {
		name, err := rooms.ParseName(key)
		if err != nil {
			continue
		}
		room, ep, err := r.openRoom(name)
		if err != nil {
			continue
		}
		for _, m := range room.Members {
			if m.Session != e.SessionID || strings.Contains(m.Nick, "@") {
				continue
			}
			if nick, preview, ok := rooms.HeldAt(ev, ep.Addr, m, os.Getpid()); ok {
				r.roomLine(key, "", fmt.Sprintf("held at %s: %s", nick, preview), true)
			}
		}
	}
}

// serveRoom reads frames sent to room name's endpoint until it closes; each frame
// goes to onRoomFrame with the sender pid from peer credentials (D:246-250).
func (r *runner) serveRoom(name rooms.Name, ep *peer.Endpoint) {
	note := rateLimited(func(l model.Line) { r.emit(ui.NoteMsg{L: l}) }, noteEvery, time.Now)
	go serveLoop(context.Background(), ep.Listener().Accept, func(c *peer.Conn) {
		if err := c.SetDeadline(time.Now().Add(inboundTimeout)); err != nil {
			return
		}
		pid, err := c.PeerPID()
		if err != nil {
			return
		}
		sc := c.Lines()
		for sc.Scan() {
			r.onRoomFrame(name, pid, slices.Clone(sc.Bytes()))
		}
	}, note, acceptBackoff)
}
