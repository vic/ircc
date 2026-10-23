package main

import (
	"os"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"ircc/internal/peer"
	"ircc/internal/registry"
	"ircc/internal/rooms"
	"ircc/internal/transcript"
	"ircc/internal/ui"
)

type sentFrame struct {
	sock string
	in   peer.Inbound
}

type trFix struct {
	r    *runner
	f    rtFix
	room rooms.Room
	name rooms.Name

	mu   sync.Mutex
	msgs []tea.Msg
	sent []sentFrame
}

// newTR opens room &r with members alice (pid 101, default), bob (pid 102, default),
// and teammate scout@alice (alice's session). pid 103 is live but not a member.
func newTR(t *testing.T) *trFix {
	t.Helper()
	x := &trFix{f: newRT(t)}
	x.r = newRunner("/nonexistent", "/proc", "vic", func(m tea.Msg) {
		x.mu.Lock()
		x.msgs = append(x.msgs, m)
		x.mu.Unlock()
	})
	x.r.rt = x.f.rt
	x.r.sendFrame = func(sock string, b []byte) error {
		in, err := peer.ParseFrame([]byte(strings.TrimSuffix(string(b), "\n")))
		if err != nil {
			t.Errorf("bad frame to %s: %v", sock, err)
		}
		x.mu.Lock()
		x.sent = append(x.sent, sentFrame{sock, in})
		x.mu.Unlock()
		return nil
	}
	for pid, n := range map[int]string{101: "alice", 102: "bob", 103: "carol"} {
		x.r.live[pid] = registry.Entry{PID: pid, SessionID: "s-" + n, SocketPath: "/s/" + n + ".sock", Name: n}
		x.r.names[pid] = "#" + n
		x.r.tails[pid] = &tail{mode: "default"}
	}
	x.name = mustName(t, "&r")
	if err := x.f.rt.open(x.name, ""); err != nil {
		t.Fatal(err)
	}
	x.room = rooms.Room{Name: x.name, Members: []rooms.Member{
		{Session: "s-alice", Nick: "alice"}, {Session: "s-bob", Nick: "bob"}, {Session: "s-alice", Nick: "scout@alice"}}}
	if err := x.f.rt.setRoom(x.room); err != nil {
		t.Fatal(err)
	}
	return x
}

func (x *trFix) lines(ch string) []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	var out []string
	for _, m := range x.msgs {
		if l, ok := m.(ui.LineMsg); ok && l.Ch == ch {
			out = append(out, l.L.Nick+"|"+l.L.Text)
		}
	}
	return out
}

func (x *trFix) notes() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	var out []string
	for _, m := range x.msgs {
		if n, ok := m.(ui.NoteMsg); ok {
			out = append(out, n.L.Text)
		}
	}
	return out
}

func (x *trFix) socks() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	var out []string
	for _, s := range x.sent {
		out = append(out, s.sock)
	}
	return out
}

func (x *trFix) frameFrom(t *testing.T, body string) []byte {
	t.Helper()
	ep, _ := x.f.rt.endpoint(x.name)
	fr, err := peer.NewUserFrame(ep.Addr, "claimed", peer.Prompting, body)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(strings.TrimSuffix(string(fr.Bytes()), "\n"))
}

func TestRoomSayFansOutToMembersAndEchoes(t *testing.T) {
	x := newTR(t)
	if err := x.r.say("&r", "hello all"); err != nil {
		t.Fatal(err)
	}
	// alice and her teammate share one socket: exactly one frame per socket (D:246)
	if got := x.socks(); len(got) != 2 {
		t.Fatalf("sockets %v", got)
	}
	for _, s := range x.sent {
		if !strings.Contains(s.in.Body, "<vic> hello all") || s.in.Mode != peer.Prompting {
			t.Errorf("frame %+v", s)
		}
	}
	if got := x.lines("&r"); len(got) != 1 || got[0] != "vic|hello all" {
		t.Fatalf("room lines %v", got)
	}
}

func TestRoomSayErrors(t *testing.T) {
	x := newTR(t)
	if err := x.r.say("&nope", "x"); err == nil {
		t.Fatal("say to an unopened room succeeded")
	}
	if err := x.r.say("#alice", "x"); err == nil {
		t.Fatal("say to a # channel succeeded")
	}
	// NEG: a member of unknown mode is skipped (fail closed), the rest still get it, and the user is told
	x.r.tails[102].mode = ""
	if err := x.r.say("&r", "hi"); err != nil {
		t.Fatal(err)
	}
	if got := x.socks(); len(got) != 1 || got[0] != "/s/alice.sock" {
		t.Fatalf("sockets %v", got)
	}
	if n := x.notes(); len(n) != 1 || !strings.Contains(n[0], "bob") {
		t.Fatalf("notes %v", n)
	}
}

func TestRoomFrameFromMemberFansToOthers(t *testing.T) {
	x := newTR(t)
	x.r.onRoomFrame(x.name, 102, x.frameFrom(t, "from bob"))
	// bob is the sender: alice's socket (teammate shares it) gets one frame, bob's none
	if got := x.socks(); len(got) != 1 || got[0] != "/s/alice.sock" {
		t.Fatalf("sockets %v", got)
	}
	if !strings.Contains(x.sent[0].in.Body, "<bob> from bob") {
		t.Fatalf("body %q", x.sent[0].in.Body)
	}
	if got := x.lines("&r"); len(got) != 1 || got[0] != "bob|from bob" {
		t.Fatalf("room lines %v", got)
	}
	if got := x.lines("#bob"); len(got) != 1 || !strings.Contains(got[0], "[&r]") || !strings.Contains(got[0], "from bob") {
		t.Fatalf("dim echo in #bob: %v", got)
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, m := range x.msgs {
		if l, ok := m.(ui.LineMsg); ok && l.Ch == "#bob" && !l.L.Dim {
			t.Fatal("echo in #bob is not dim")
		}
	}
}

func TestRoomFrameTeammateNick(t *testing.T) {
	x := newTR(t)
	x.r.onRoomFrame(x.name, 101, x.frameFrom(t, `<agent-message from="scout"> found it`))
	if got := x.lines("&r"); len(got) != 1 || !strings.HasPrefix(got[0], "scout@alice|") {
		t.Fatalf("room lines %v", got)
	}
	// the writer shares alice's socket: only bob gets a copy
	if got := x.socks(); len(got) != 1 || got[0] != "/s/bob.sock" {
		t.Fatalf("sockets %v", got)
	}
}

func TestRoomFrameNonMemberShownNotFanned(t *testing.T) {
	x := newTR(t)
	x.r.onRoomFrame(x.name, 103, x.frameFrom(t, "let me in"))
	if len(x.socks()) != 0 {
		t.Fatalf("non-member fanned: %v", x.socks())
	}
	if got := x.lines("&r"); len(got) != 1 || !strings.Contains(got[0], "[non-member]") {
		t.Fatalf("room lines %v", got)
	}
	// NEG: an unknown pid and a malformed frame: nothing sent, no panic
	x.r.onRoomFrame(x.name, 999, x.frameFrom(t, "who"))
	x.r.onRoomFrame(x.name, 102, []byte(`{"not":"a frame"}`))
	x.r.onRoomFrame(mustName(t, "&gone"), 102, x.frameFrom(t, "x"))
	if len(x.socks()) != 0 {
		t.Fatalf("sent %v", x.socks())
	}
}

func TestRoomFrameUnknownSenderModeFailsClosed(t *testing.T) {
	x := newTR(t)
	x.r.tails[102].mode = ""
	x.r.onRoomFrame(x.name, 102, x.frameFrom(t, "hm"))
	if len(x.socks()) != 0 {
		t.Fatalf("fanned with unknown sender mode: %v", x.socks())
	}
	if n := x.notes(); len(n) != 1 || !strings.Contains(n[0], "bob") {
		t.Fatalf("notes %v", n)
	}
}

func TestRoomHeldEventMarksLine(t *testing.T) {
	x := newTR(t)
	ep, _ := x.f.rt.endpoint(x.name)
	held := transcript.Event{Held: true, HeldFrom: ep.Addr.String(), HeldPID: os.Getpid(), HeldPreview: "<vic> hello"}
	x.r.onEvent(102, held)
	if got := x.lines("&r"); len(got) != 1 || !strings.Contains(got[0], "held at bob") || !strings.Contains(got[0], "<vic> hello") {
		t.Fatalf("room lines %v", got)
	}
	// NEG: another address, another pid, and a non-held event mark nothing
	other := held
	other.HeldFrom = "uds:/elsewhere.sock"
	x.r.onEvent(102, other)
	other = held
	other.HeldPID++
	x.r.onEvent(102, other)
	x.r.onEvent(102, transcript.Event{})
	x.r.onEvent(999, held)
	if got := x.lines("&r"); len(got) != 1 {
		t.Fatalf("extra marks: %v", got)
	}
}
