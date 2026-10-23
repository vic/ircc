package rooms

import (
	"strings"

	"ircc/internal/registry"
)

// Identify maps a peer pid (from socket credentials, D:246-250) to a room
// member: pid -> live registry entry -> member with that session. A teammate
// shares its parent's session, so the parent (nick without "@") wins; the
// claimed from/from-name is never consulted. live must come from registry.Scan/Watch
// (procStart-verified); entries with an empty SessionID never match.
func Identify(pid int, live []registry.Entry, room Room) (Member, bool) {
	for _, e := range live {
		if e.PID != pid || e.SessionID == "" {
			continue
		}
		for _, m := range room.Members {
			if m.Session == e.SessionID && !strings.Contains(m.Nick, "@") {
				return m, true
			}
		}
	}
	return Member{}, false
}

// ResolveAgent maps a teammate nick (from an <agent-message> wrapper, D:249) to
// its "<agentNick>@<parent nick>" member of the parent's session, else parent.
func ResolveAgent(room Room, parent Member, agentNick string) Member {
	if agentNick == "" {
		return parent
	}
	want := agentNick + "@" + parent.Nick
	for _, m := range room.Members {
		if m.Session == parent.Session && m.Nick == want {
			return m
		}
	}
	return parent
}
