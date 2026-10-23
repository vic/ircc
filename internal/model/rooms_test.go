package model

import (
	"reflect"
	"testing"
)

func TestWithRooms(t *testing.T) {
	m := newModel(t).WithSessions([]Session{{PID: 1, Name: "a"}, {PID: 2, Name: "b"}})
	m = m.WithRooms([]string{"&x", "&y"})
	if got, want := names(m), []string{"#a", "#b", "&x", "&y"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	m, err := m.AddLine("&x", Line{Text: "keep me"})
	if err != nil {
		t.Fatal(err)
	}
	if m, err = m.WithRoomMembers("&x", []Member{{"n", 5}}); err != nil {
		t.Fatal(err)
	}
	m2 := m.WithRooms([]string{"&z", "&x"})                                                      // &y gone, &z new (last), &x kept in place
	if got, want := names(m2), []string{"&x", "#a", "#b", "&z"}; !reflect.DeepEqual(got, want) { // &x moved to the front by its line
		t.Fatalf("got %v want %v", got, want)
	}
	for _, c := range m2.Channels() {
		if c.Name == "&x" && (c.Lines.Len() != 1 || len(c.Members) != 1) {
			t.Fatalf("&x lost scrollback or members: %+v", c)
		}
		if c.Name == "&z" && c.Kind != RoomChan {
			t.Fatalf("&z kind %v", c.Kind)
		}
	}
	if names(m)[len(names(m))-1] != "&y" { // old Model unchanged
		t.Fatalf("old model changed: %v", names(m))
	}
	// NEG: empty set removes all rooms, sessions stay
	if got := names(m2.WithRooms(nil)); !reflect.DeepEqual(got, []string{"#a", "#b"}) {
		t.Fatalf("got %v", got)
	}
	// NEG: duplicate names collapse to one channel
	if got := names(newModel(t).WithRooms([]string{"&d", "&d"})); !reflect.DeepEqual(got, []string{"&d"}) {
		t.Fatalf("got %v", got)
	}
}

func TestWithRoomsKeepsFocusByName(t *testing.T) {
	m := newModel(t).WithSessions([]Session{{PID: 1, Name: "a"}}).WithRooms([]string{"&x", "&y"})
	m, err := m.Focus(2) // &y
	if err != nil {
		t.Fatal(err)
	}
	if got := m.WithRooms([]string{"&y"}).Current().Name; got != "&y" {
		t.Fatalf("focus moved to %q", got)
	}
	if got := m.WithRooms([]string{"&x"}).Current().Name; got == "" { // focused room removed: still a valid channel
		t.Fatal("no current channel after removing the focused room")
	}
}
