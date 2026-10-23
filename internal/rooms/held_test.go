package rooms

import (
	"strings"
	"testing"

	"ircc/internal/peer"
	"ircc/internal/transcript"
)

func heldEvent(t *testing.T, level, from string) transcript.Event {
	return heldEventPID(t, level, from, "[verified pid 1] ")
}

func heldEventPID(t *testing.T, level, from, verified string) transcript.Event {
	t.Helper()
	line := `{"type":"system","subtype":"informational","level":"` + level + `","content":"Held peer message — from ` +
		from + ` ` + verified + `(peer claims name: x); preview: «hi» — not delivered to Claude (1 held)."}`
	e, err := transcript.ParseEvent([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestHeldAt(t *testing.T) {
	const roomSock = "uds:/run/x/ircc-%26review.sock"
	room, err := peer.ParseAddr(roomSock)
	if err != nil {
		t.Fatal(err)
	}
	m := Member{Session: "s1", Nick: "scribe"}
	tests := []struct {
		name string
		ev   transcript.Event
		want string
		ok   bool
	}{
		{"held from room", heldEvent(t, "warning", roomSock), "scribe", true},
		{"held from other sender", heldEvent(t, "warning", "uds:/run/x/ircc-vic.sock"), "", false},
		{"info level ignored", heldEvent(t, "info", roomSock), "", false},
		{"forged held, other pid", heldEventPID(t, "warning", roomSock, "[verified pid 2] "), "", false},
		{"non-held event", transcript.Event{Type: "user"}, "", false},
		{"prefix sibling sock is not the room", heldEvent(t, "warning", strings.TrimSuffix(roomSock, ".sock")+"2.sock"), "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, prev, ok := HeldAt(tc.ev, room, m, 1)
			if ok && prev != "hi" {
				t.Fatalf("preview %q", prev)
			}
			if got != tc.want || ok != tc.ok {
				t.Fatalf("got %q %v want %q %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}
