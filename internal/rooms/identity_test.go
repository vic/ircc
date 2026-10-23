package rooms

import (
	"testing"

	"ircc/internal/registry"
)

func TestIdentify(t *testing.T) {
	room := Room{Members: []Member{
		{Session: "s1", Nick: "alice"},
		{Session: "s2", Nick: "scribe-1@bob"}, // teammate listed first
		{Session: "s2", Nick: "bob"},
		{Session: "", Nick: "nosession"},
	}}
	live := []registry.Entry{{PID: 40}, {PID: 10, SessionID: "s1"}, {PID: 20, SessionID: "s2"}, {PID: 30, SessionID: "s9"}}
	tests := []struct {
		name string
		pid  int
		want string
		ok   bool
	}{
		{"member", 10, "alice", true},
		{"parent pid, not teammate", 20, "bob", true},
		{"live but not a member", 30, "", false},
		{"dead pid", 99, "", false},
		{"pid 0", 0, "", false},
		{"live entry with empty session", 40, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := Identify(tc.pid, live, room)
			if ok != tc.ok || m.Nick != tc.want {
				t.Fatalf("got %+v %v", m, ok)
			}
		})
	}
}

func TestResolveAgent(t *testing.T) {
	bob := Member{Session: "s2", Nick: "bob"}
	scribe := Member{Session: "s2", Nick: "scribe-1@bob"}
	other := Member{Session: "s3", Nick: "scribe-1@bob"} // same nick, other session
	room := Room{Members: []Member{other, scribe, bob}}
	for _, tc := range []struct {
		name, agent string
		want        Member
	}{
		{"known teammate", "scribe-1", scribe},
		{"unknown agent -> parent", "nobody", bob},
		{"empty -> parent", "", bob},
	} {
		if got := ResolveAgent(room, bob, tc.agent); got != tc.want {
			t.Errorf("%s: got %+v want %+v", tc.name, got, tc.want)
		}
	}
}
