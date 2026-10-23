package rooms

import (
	"ircc/internal/peer"
	"ircc/internal/transcript"
)

// HeldAt reports whether ev, read from member m's transcript, is a held peer
// message sent from room's endpoint (D:256-257, measured 13). It returns the
// member's nick and the held preview; the caller marks the fanned line "held at
// <nick>" whose text starts with that preview (D:105 «...»; no msg id exists, so
// correlation is by preview prefix). Other senders and non-held events are ignored.
// ev.HeldPID must equal selfPID (ircc's own pid, m14): the receiver verifies the
// connecting pid, so another process claiming the room's address cannot forge it.
func HeldAt(ev transcript.Event, room peer.Addr, m Member, selfPID int) (nick, preview string, ok bool) {
	if !ev.Held || ev.HeldPID != selfPID || ev.HeldFrom != room.String() {
		return "", "", false
	}
	return m.Nick, ev.HeldPreview, true
}
