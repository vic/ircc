package model

import (
	"errors"
	"testing"
)

func newModel(t *testing.T) Model {
	t.Helper()
	m, err := New(5)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func names(m Model) []string {
	var out []string
	for _, c := range m.Channels() {
		out = append(out, c.Name)
	}
	return out
}

func TestNewBadCap(t *testing.T) {
	if _, err := New(0); !errors.Is(err, ErrBadCap) {
		t.Fatalf("got %v", err)
	}
}

func TestWithSessions(t *testing.T) {
	a := Session{PID: 1, Name: "Alpha"}
	b := Session{PID: 2, Name: "Beta"}
	m := newModel(t).WithSessions([]Session{a, b})
	if got := names(m); len(got) != 2 || got[0] != "#alpha" || got[1] != "#beta" {
		t.Fatalf("add: %v", got)
	}
	for _, c := range m.Channels() {
		if c.Kind != SessionChan || c.Gone || c.Lines.Len() != 0 {
			t.Fatalf("fresh channel %+v", c)
		}
	}

	// beta gone: kept, greyed, scrollback kept (D:220)
	m.chans[1].Lines.Push(Line{Text: "x"})
	m2 := m.WithSessions([]Session{a})
	cs := m2.Channels()
	if len(cs) != 2 || cs[0].Gone || !cs[1].Gone || cs[1].Lines.Len() != 1 {
		t.Fatalf("gone: %+v", cs)
	}

	// beta returns: un-greyed, same scrollback, no duplicate
	m3 := m2.WithSessions([]Session{a, b})
	cs = m3.Channels()
	if len(cs) != 2 || cs[1].Gone || cs[1].Lines.Len() != 1 {
		t.Fatalf("return: %+v", cs)
	}

	// rename on collision follows ChanNames
	m4 := m3.WithSessions([]Session{a, {PID: 3, Name: "alpha"}})
	if got := names(m4); got[0] != "#alpha~1" || got[2] != "#alpha~3" {
		t.Fatalf("collide: %v", got)
	}
}

func TestOrderKeptNewLast(t *testing.T) { // vic 2026-10-22: most recent first, not # before &
	m := newModel(t)
	m.chans = append(m.chans, Channel{Name: "&room", Kind: RoomChan})
	m = m.WithSessions([]Session{{PID: 9, Name: "s"}})
	if got := names(m); got[0] != "&room" || got[1] != "#s" {
		t.Fatalf("order %v", got)
	}
}

func TestFocusFollowsChannel(t *testing.T) {
	m := newModel(t)
	m.chans = append(m.chans, Channel{Name: "&room", Kind: RoomChan})
	m = m.WithSessions([]Session{{PID: 9, Name: "s"}}) // [&room #s]: the new session goes last
	m.cur = 0
	m = m.WithSessions([]Session{{PID: 9, Name: "s"}, {PID: 8, Name: "t"}})
	if m.chans[m.cur].Name != "&room" {
		t.Fatalf("cur=%d on %q", m.cur, m.chans[m.cur].Name)
	}
}

func TestChannelsIsCopy(t *testing.T) {
	m := newModel(t).WithSessions([]Session{{PID: 1, Name: "a"}})
	cs := m.Channels()
	cs[0].Name = "mutated"
	if m.Channels()[0].Name != "#a" {
		t.Fatal("Channels leaked internal slice")
	}
}
