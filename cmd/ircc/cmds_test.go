package main

import (
	"errors"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"ircc/internal/model"
	"ircc/internal/peer"
	"ircc/internal/rooms"
	"ircc/internal/ui"
)

// fakeRT is an in-memory cmdRT.
type fakeRT struct {
	mu      sync.Mutex
	rooms   map[string]rooms.Room
	opened  []string
	closed  []string
	openErr error
	addr    peer.Addr
	dir     string
}

func (f *fakeRT) open(n rooms.Name, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openErr != nil {
		return f.openErr
	}
	f.opened = append(f.opened, n.String())
	if _, ok := f.rooms[n.String()]; !ok {
		f.rooms[n.String()] = rooms.Room{Name: n}
	}
	return nil
}

func (f *fakeRT) closeRoom(n rooms.Name) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rooms[n.String()]; !ok {
		return errors.New("no such open room")
	}
	delete(f.rooms, n.String())
	f.closed = append(f.closed, n.String())
	return nil
}

func (f *fakeRT) room(n rooms.Name) (rooms.Room, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rooms[n.String()]
	return r, ok
}

func (f *fakeRT) endpoint(n rooms.Name) (*peer.Endpoint, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rooms[n.String()]; !ok {
		return nil, false
	}
	return &peer.Endpoint{Name: n.String(), Addr: f.addr}, true
}

func (f *fakeRT) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.rooms {
		out = append(out, k)
	}
	return sortedStrings(out)
}

func (f *fakeRT) setRoom(r rooms.Room) error {
	if err := rooms.Save(f.dir, r); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rooms[r.Name.String()] = r
	return nil
}

func sortedStrings(s []string) []string {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
	return s
}

type cmdFix struct {
	c      *cmds
	rt     *fakeRT
	msgs   []tea.Msg
	frames []string // socks of invite frames
	chSent []string
}

func newCmdFix(t *testing.T) *cmdFix {
	t.Helper()
	addr, err := peer.ParseAddr("uds:/run/x/ircc-%26r.sock")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	f := &cmdFix{rt: &fakeRT{rooms: map[string]rooms.Room{}, addr: addr, dir: dir}}
	f.c = &cmds{
		rt:   f.rt,
		dir:  dir,
		emit: func(m tea.Msg) { f.msgs = append(f.msgs, m) },
		send: func(sock string, _ peer.Frame) error { f.frames = append(f.frames, sock); return nil },
		ends: func(rooms.Room) []rooms.Endpoint {
			return []rooms.Endpoint{{Member: rooms.Member{Session: "s7", Nick: "dave"}, Sock: "/s/7.sock", Class: peer.Prompting}}
		},
		sendCh:   func(ch, text string) error { f.chSent = append(f.chSent, ch+"|"+text); return nil },
		inviteFn: fakeInvite,
	}
	return f
}

func (f *cmdFix) do(t *testing.T, ch, line string) error {
	t.Helper()
	c, err := model.ParseCmd(line)
	if err != nil {
		t.Fatal(err)
	}
	return f.c.do(ch, c)
}

func (f *cmdFix) lastRooms() ui.RoomsMsg {
	for i := len(f.msgs) - 1; i >= 0; i-- {
		if m, ok := f.msgs[i].(ui.RoomsMsg); ok {
			return m
		}
	}
	return nil
}

func (f *cmdFix) lastMembers() (ui.MembersMsg, bool) {
	for i := len(f.msgs) - 1; i >= 0; i-- {
		if m, ok := f.msgs[i].(ui.MembersMsg); ok {
			return m, true
		}
	}
	return ui.MembersMsg{}, false
}

func (f *cmdFix) lastNote() string {
	for i := len(f.msgs) - 1; i >= 0; i-- {
		if m, ok := f.msgs[i].(ui.NoteMsg); ok {
			return m.L.Text
		}
	}
	return ""
}

func TestCmdJoinPartCloseCycle(t *testing.T) {
	f := newCmdFix(t)
	if err := f.do(t, "#a", "/join &r"); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRooms(); len(got) != 1 || got[0] != "&r" {
		t.Fatalf("rooms after join: %v", got)
	}
	if err := f.do(t, "&r", "/part"); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRooms(); len(got) != 0 || len(f.rt.rooms) != 1 {
		t.Fatalf("part must hide only: shown %v, rt rooms %d", got, len(f.rt.rooms))
	}
	if err := f.do(t, "#a", "/join &r"); err != nil { // parted: show, do not reopen
		t.Fatal(err)
	}
	if len(f.rt.opened) != 1 || len(f.lastRooms()) != 1 {
		t.Fatalf("rejoin of a parted room: opened %v shown %v", f.rt.opened, f.lastRooms())
	}
	if err := f.do(t, "&r", "/close"); err != nil {
		t.Fatal(err)
	}
	if len(f.rt.closed) != 1 || len(f.lastRooms()) != 0 {
		t.Fatalf("close: closed %v shown %v", f.rt.closed, f.lastRooms())
	}
}

func TestCmdNegatives(t *testing.T) {
	for _, tc := range []struct{ name, ch, line, want string }{
		{"join bad name", "#a", `/join &a"b`, "forbidden"},
		{"join without &", "#a", "/join r", "&"},
		{"close a # channel", "#a", "/close", "&"},
		{"close unknown room", "&nope", "/close", "no room"},
		{"part unknown room", "&nope", "/part", "no room"},
		{"topic in # channel", "#a", "/topic hi", "&"},
		{"invite into unknown room", "#a", "/invite dave &nope", "no room"},
		{"kick in # channel", "#a", "/kick dave", "&"},
		{"names in # channel", "#a", "/names", "&"},
		{"mode unsupported", "#a", "/mode bypass", "GAP-10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCmdFix(t)
			f.rt.setRoom(rooms.Room{Name: mustName(t, "&r")})
			err := f.do(t, tc.ch, tc.line)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestCmdJoinOpenFailureSurfaces(t *testing.T) {
	f := newCmdFix(t)
	f.rt.openErr = errors.New("boom")
	if err := f.do(t, "#a", "/join &r"); err == nil || f.lastRooms() != nil {
		t.Fatalf("err %v rooms %v", err, f.lastRooms())
	}
}

func TestCmdTopicSaves(t *testing.T) {
	f := newCmdFix(t)
	if err := f.do(t, "#a", "/join &r"); err != nil {
		t.Fatal(err)
	}
	if err := f.do(t, "&r", "/topic ship the thing"); err != nil {
		t.Fatal(err)
	}
	r, _ := f.rt.room(mustName(t, "&r"))
	if r.Topic != "ship the thing" {
		t.Fatalf("topic %q", r.Topic)
	}
	saved, errs := rooms.Load(f.c.dir)
	if len(errs) != 0 || len(saved) != 1 || saved[0].Topic != "ship the thing" {
		t.Fatalf("not saved: %+v %v", saved, errs)
	}
}

func TestCmdInviteKickNames(t *testing.T) {
	f := newCmdFix(t)
	if err := f.do(t, "#a", "/join &r"); err != nil {
		t.Fatal(err)
	}
	if err := f.do(t, "#a", "/invite dave &r"); err != nil {
		t.Fatal(err)
	}
	if len(f.frames) != 1 || f.frames[0] != "/s/7.sock" {
		t.Fatalf("want exactly one invite frame to dave, got %v", f.frames)
	}
	r, _ := f.rt.room(mustName(t, "&r"))
	if len(r.Members) != 1 || r.Members[0].Nick != "dave" {
		t.Fatalf("members %+v", r.Members)
	}
	if m, ok := f.lastMembers(); !ok || m.Ch != "&r" || len(m.Nicks) != 1 || m.Nicks[0] != "dave" {
		t.Fatalf("ui members after invite: %+v", m)
	}
	if err := f.do(t, "&r", "/names"); err != nil || !strings.Contains(f.lastNote(), "dave") {
		t.Fatalf("names: %v %q", err, f.lastNote())
	}
	if err := f.do(t, "&r", "/who"); err != nil || !strings.Contains(f.lastNote(), "dave") {
		t.Fatalf("who: %v %q", err, f.lastNote())
	}
	if err := f.do(t, "&r", "/kick nobody"); err == nil {
		t.Fatal("kick of a non-member: want error")
	}
	if err := f.do(t, "&r", "/kick dave"); err != nil {
		t.Fatal(err)
	}
	r, _ = f.rt.room(mustName(t, "&r"))
	saved, _ := rooms.Load(f.c.dir)
	if len(r.Members) != 0 || len(saved) != 1 || len(saved[0].Members) != 0 {
		t.Fatalf("kick: rt %+v saved %+v", r.Members, saved)
	}
	if m, ok := f.lastMembers(); !ok || len(m.Nicks) != 0 {
		t.Fatalf("ui members after kick: %+v", m)
	}
	// NEG: unknown invitee sends nothing.
	f.frames = nil
	if err := f.do(t, "#a", "/invite ghost &r"); err == nil || len(f.frames) != 0 {
		t.Fatalf("unknown nick: err %v frames %v", err, f.frames)
	}
}

func TestCmdMsgGoesToSessionChannel(t *testing.T) {
	f := newCmdFix(t)
	if err := f.do(t, "#a", "/msg alpha hello there"); err != nil {
		t.Fatal(err)
	}
	if len(f.chSent) != 1 || f.chSent[0] != "#alpha|hello there" {
		t.Fatalf("sent %v", f.chSent)
	}
	f.c.sendCh = func(string, string) error { return errors.New("no live session") }
	if err := f.do(t, "#a", "/msg alpha hi"); err == nil {
		t.Fatal("send failure must surface")
	}
}

func TestCmdTeaCmdTurnsErrorsIntoNotes(t *testing.T) {
	f := newCmdFix(t)
	c, err := model.ParseCmd("/close")
	if err != nil {
		t.Fatal(err)
	}
	msg := f.c.cmd("#a", c)()
	n, ok := msg.(ui.NoteMsg)
	if !ok || !n.L.Dim || n.L.Text == "" {
		t.Fatalf("want dim NoteMsg, got %#v", msg)
	}
	ok2, _ := model.ParseCmd("/join &r")
	if m := f.c.cmd("#a", ok2)(); m != nil {
		t.Fatalf("success must return no message, got %#v", m)
	}
}

// fakeInvite mimics rooms.Invite's contract: unknown nick -> error and no frame;
// otherwise one frame to the endpoint and the member added.
func fakeInvite(send rooms.SendFunc, _ string, roomAddr peer.Addr, room rooms.Room, ends []rooms.Endpoint, nick string) (rooms.Room, error) {
	for _, e := range ends {
		if e.Member.Nick != nick {
			continue
		}
		f, err := peer.NewUserFrame(roomAddr, room.Name.String(), e.Class, "invite")
		if err != nil {
			return room, err
		}
		if err := send(e.Sock, f); err != nil {
			return room, err
		}
		next := room
		next.Members = append(append([]rooms.Member(nil), room.Members...), e.Member)
		return next, nil
	}
	return room, errors.New("unknown nick " + nick)
}
