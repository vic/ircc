package model

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 22, 15, 4, 30, 0, time.UTC)

func roomModel(t *testing.T, lines ...Line) Model {
	t.Helper()
	m := newModel(t)
	r := RingOf[Line](m.cap)
	for _, l := range lines {
		r.Push(l)
	}
	m.chans = []Channel{{Name: "#a", Kind: SessionChan, PID: 1, Unread: 2}, {Name: "&review", Kind: RoomChan, PID: 0, Unread: 3, Lines: r, Members: []Member{{"a", 2}, {"b", 3}}}}
	return m
}

func TestStatus(t *testing.T) {
	const byp, def = "bypassPermissions", "default"
	si := func(pm string) SessionInfo { return SessionInfo{Status: "idle", PermMode: pm} }
	rate := []Line{{At: t0.Add(-10 * time.Second)}, {At: t0.Add(-59 * time.Second)},
		{At: t0.Add(-61 * time.Second)}, {At: t0.Add(5 * time.Second)}}
	// room members are pids 2 and 3; focusing the room clears its unread 3 (total 2)
	tests := []struct {
		name string
		m    Model
		cur  int
		mode string
		info map[int]SessionInfo
		want Modeline
	}{
		{"session channel", roomModel(t), 0, "NORMAL", map[int]SessionInfo{1: {Status: "busy", PermMode: def}},
			Modeline{Mode: "NORMAL", Left: "#a busy default", Right: "5 unread 15:04"}},
		{"session bypass is red", roomModel(t), 0, "INSERT", map[int]SessionInfo{1: si(byp)},
			Modeline{Mode: "INSERT", Left: "#a idle bypassPermissions", Right: "5 unread 15:04", Red: true}},
		{"room: member bypass, lines/min", roomModel(t, rate...), 1, "NORMAL", map[int]SessionInfo{2: si(byp), 3: si(def)},
			Modeline{Mode: "NORMAL", Left: "&review 2 members 1 bypass 0 ? 2/min", Right: "2 unread 15:04", Red: true}},
		{"room without bypass is not red", roomModel(t), 1, "NORMAL", map[int]SessionInfo{2: si(def), 3: si(def)},
			Modeline{Mode: "NORMAL", Left: "&review 2 members 0 bypass 0 ? 0/min", Right: "2 unread 15:04"}},
		{"bypass pid not a member is ignored", roomModel(t), 1, "NORMAL", map[int]SessionInfo{2: si(def), 3: si(def), 9: si(byp)},
			Modeline{Mode: "NORMAL", Left: "&review 2 members 0 bypass 0 ? 0/min", Right: "2 unread 15:04"}},
		// NEG: fail safe, unknown is red
		{"member without info is unknown and red", roomModel(t), 1, "NORMAL", map[int]SessionInfo{3: si(def)},
			Modeline{Mode: "NORMAL", Left: "&review 2 members 0 bypass 1 ? 0/min", Right: "2 unread 15:04", Red: true}},
		{"member with unknown mode is unknown and red", roomModel(t), 1, "NORMAL", map[int]SessionInfo{2: si("weird"), 3: si(def)},
			Modeline{Mode: "NORMAL", Left: "&review 2 members 0 bypass 1 ? 0/min", Right: "2 unread 15:04", Red: true}},
		{"unknown session info", roomModel(t), 0, "NORMAL", nil,
			Modeline{Mode: "NORMAL", Left: "#a ? ?", Right: "5 unread 15:04", Red: true}},
		{"session with unknown mode is red", roomModel(t), 0, "NORMAL", map[int]SessionInfo{1: {Status: "idle", PermMode: "weird"}},
			Modeline{Mode: "NORMAL", Left: "#a idle ?", Right: "5 unread 15:04", Red: true}},
		{"no channels", mustNew(t), 0, "NORMAL", nil,
			Modeline{Mode: "NORMAL", Right: "0 unread 15:04"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.m
			if tc.cur != 0 {
				var err error
				if m, err = m.Focus(tc.cur); err != nil {
					t.Fatal(err)
				}
			}
			if got := Status(m, tc.mode, tc.info, t0); got != tc.want {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

func mustNew(t *testing.T) Model {
	t.Helper()
	return newModel(t)
}

func TestWithRoomMembers(t *testing.T) {
	m := roomModel(t)
	m2, err := m.WithRoomMembers("&review", []Member{{"x", 1}, {"y", 2}, {"z", 3}})
	if err != nil {
		t.Fatal(err)
	}
	if got := m2.Channels()[1].Members; !reflect.DeepEqual(got, []Member{{"x", 1}, {"y", 2}, {"z", 3}}) {
		t.Fatalf("members %v", got)
	}
	if len(m.Channels()[1].Members) != 2 { // old Model unchanged
		t.Fatal("old model changed")
	}
	m2, err = m2.Focus(1)
	if err != nil {
		t.Fatal(err)
	}
	if got := Status(m2, "NORMAL", nil, t0).Left; got != "&review 3 members 0 bypass 3 ? 0/min" {
		t.Fatalf("left %q", got)
	}
	// copy on read: mutating a returned slice does not reach the model
	m2.Channels()[1].Members[0].Nick = "zzz"
	if m2.Channels()[1].Members[0].Nick != "x" {
		t.Fatal("Channels leaks Members")
	}
	// copy on write: caller's slice is not aliased
	in := []Member{{"x", 1}}
	m3, _ := m.WithRoomMembers("&review", in)
	in[0].Nick = "y"
	if m3.Channels()[1].Members[0].Nick != "x" {
		t.Fatal("input aliased")
	}
}

func TestWithRoomMembersErrors(t *testing.T) {
	m := roomModel(t)
	if _, err := m.WithRoomMembers("#a", []Member{{"x", 1}}); !errors.Is(err, ErrNotRoom) {
		t.Fatalf("session chan: %v", err)
	}
	if _, err := m.WithRoomMembers("&nope", nil); !errors.Is(err, ErrUnknownChannel) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestStatusDistinctPIDs(t *testing.T) {
	m := roomModel(t)
	m, err := m.WithRoomMembers("&review", []Member{{"parent", 2}, {"teammate", 2}, {"ghost", 7}, {"ghost2", 7}})
	if err != nil {
		t.Fatal(err)
	}
	if m, err = m.Focus(1); err != nil {
		t.Fatal(err)
	}
	info := map[int]SessionInfo{2: {Status: "idle", PermMode: "bypassPermissions"}}
	want := "&review 4 members 1 bypass 1 ? 0/min"
	if got := Status(m, "NORMAL", info, t0).Left; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestStatusSanitizesInfo(t *testing.T) {
	const esc = "\x1b[2J"
	for _, si := range []SessionInfo{{Status: esc, PermMode: "default"}, {Status: "idle", PermMode: esc}} {
		got := Status(roomModel(t), "NORMAL", map[int]SessionInfo{1: si}, t0)
		if strings.ContainsRune(got.Left, 0x1b) {
			t.Errorf("escape reached the modeline: %q", got.Left)
		}
	}
	got := Status(roomModel(t), "NORMAL", map[int]SessionInfo{1: {Status: "idle", PermMode: esc}}, t0)
	if got.Mode != "NORMAL" || got.Left != "#a idle ?" || !got.Red {
		t.Fatalf("got %+v", got)
	}
}
