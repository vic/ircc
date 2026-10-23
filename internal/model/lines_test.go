package model

import (
	"errors"
	"strings"
	"testing"
)

func twoChans(t *testing.T) Model {
	t.Helper()
	return newModel(t).WithSessions([]Session{{PID: 1, Name: "a"}, {PID: 2, Name: "b"}})
}

func TestAddLine(t *testing.T) {
	m := twoChans(t) // cur = 0 (#a)
	m2, err := m.AddLine("#a", Line{Text: "cur"})
	if err != nil {
		t.Fatal(err)
	}
	m3, err := m2.AddLine("#b", Line{Text: "other"})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := chanAt(t, m3, "#a")
	b, bi := chanAt(t, m3, "#b")
	if bi != 0 || m3.Current().Name != "#a" { // the newest line moves #b first; focus stays
		t.Fatalf("order %v cur %s", names(m3), m3.Current().Name)
	}
	if a.Unread != 0 || a.Lines.Len() != 1 {
		t.Fatalf("current: %+v", a)
	}
	if b.Unread != 1 || b.Lines.Len() != 1 {
		t.Fatalf("other: %+v", b)
	}
}

func TestAddLineUnknownLeavesModel(t *testing.T) {
	m := twoChans(t)
	m2, err := m.AddLine("#zz", Line{Text: "x"})
	if !errors.Is(err, ErrUnknownChannel) {
		t.Fatalf("got %v", err)
	}
	for i, c := range m2.Channels() {
		if c.Lines.Len() != 0 || c.Unread != 0 {
			t.Fatalf("chan %d changed", i)
		}
	}
}

func TestAddLineCopyOnWrite(t *testing.T) {
	m := twoChans(t)
	m2, err := m.AddLine("#b", Line{Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := chanAt(t, m2, "#b"); c.Lines.Len() != 1 {
		t.Fatal("new model missing line")
	}
	if c, _ := chanAt(t, m, "#b"); c.Lines.Len() != 0 || c.Unread != 0 {
		t.Fatalf("old model mutated: %+v", c)
	}
}

func TestFocusAndCurrent(t *testing.T) {
	m, err := twoChans(t).AddLine("#b", Line{Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	_, bi := chanAt(t, m, "#b")
	m2, err := m.Focus(bi)
	if err != nil {
		t.Fatal(err)
	}
	if c := m2.Current(); c.Name != "#b" || c.Unread != 0 {
		t.Fatalf("current %+v", c)
	}
	if b, _ := chanAt(t, m, "#b"); b.Unread != 1 || m.Current().Name != "#a" {
		t.Fatal("old model changed")
	}
	// a line to the focused channel does not count as unread
	m3, err := m2.AddLine("#b", Line{Text: "y"})
	if err != nil {
		t.Fatal(err)
	}
	if m3.Current().Unread != 0 {
		t.Fatal("unread on focused channel")
	}
}

func TestFocusBadIndex(t *testing.T) {
	m := twoChans(t)
	for _, i := range []int{-1, 2} {
		if _, err := m.Focus(i); !errors.Is(err, ErrBadIndex) {
			t.Fatalf("Focus(%d): %v", i, err)
		}
	}
}

func TestCurrentEmpty(t *testing.T) {
	if c := newModel(t).Current(); c.Name != "" {
		t.Fatalf("got %+v", c)
	}
}

func TestAddLineSanitizes(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"OSC52 clipboard + clear", "hi\x1b]52;c;ZXZpbA==\x07\x1b[2J", "hi]52;c;ZXZpbA==[2J"},
		{"BEL DEL", "a\x07b\x7fc", "abc"},
		{"C1 CSI", "a\u009b2Jb", "a2Jb"},
		{"tab newline to space", "a\tb\nc\r\nd", "a b c  d"},
		{"invalid utf8", "a\xffb", "a�b"},
		{"bidi override", "a‮b​c", "abc"},
		{"printable unchanged", "héllo wörld, ok <x> 日本", "héllo wörld, ok <x> 日本"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := twoChans(t).AddLine("#a", Line{Nick: "n\x1b[31m" + tc.in, Text: tc.in})
			if err != nil {
				t.Fatal(err)
			}
			got := m.Channels()[0].Lines.All()[0]
			if got.Text != tc.want || got.Nick != "n[31m"+tc.want {
				t.Fatalf("got nick %q text %q, want %q", got.Nick, got.Text, tc.want)
			}
		})
	}
}

func TestSessionNamesAndMembersSanitized(t *testing.T) {
	m := newModel(t).WithSessions([]Session{{PID: 1, Name: "a\x1b]52;c;x\x07b"}})
	if n := m.Channels()[0].Name; strings.ContainsAny(n, "\x1b\x07") {
		t.Fatalf("channel name %q", n)
	}
	m.chans = append(m.chans, Channel{Name: "&r", Kind: RoomChan})
	m, err := m.WithRoomMembers("&r", []Member{{Nick: "bo\x1b[2Jb", PID: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if n := m.Channels()[1].Members[0].Nick; n != "bo[2Jb" {
		t.Fatalf("member nick %q", n)
	}
}

// chanAt returns channel name and its index: positions follow recency.
func chanAt(t *testing.T, m Model, name string) (Channel, int) {
	t.Helper()
	for i, c := range m.Channels() {
		if c.Name == name {
			return c, i
		}
	}
	t.Fatalf("no channel %s in %v", name, names(m))
	return Channel{}, -1
}
