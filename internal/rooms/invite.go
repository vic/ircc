package rooms

import (
	"fmt"
	"slices"
	"strings"

	"ircc/internal/peer"
)

// inviteIntro is the opening of the /invite frame. DESIGN D:240-241 names its content
// (room name, topic, member list, how to talk) but gives no literal text: lead ruling GAP-7.
const inviteIntro = "You are invited to %s. Reply to `from` to talk in the room"

// Invite sends nick (a live endpoint in ends, not yet a member) one frame from the
// room address and, only if that succeeds, adds it to the room and saves it to dir
// (D:240-241). The frame claims the target's class, like a line you typed (R8).
// It returns the updated room; if sending fails the room is unchanged and nothing is saved.
// If only Save fails the invitee already has its frame, so the updated room is returned
// with an "invited but room not saved" error.
func Invite(send SendFunc, dir string, roomAddr peer.Addr, room Room, ends []Endpoint, nick string) (Room, error) {
	if slices.ContainsFunc(room.Members, func(m Member) bool { return m.Nick == nick }) {
		return room, fmt.Errorf("rooms: invite: %s is already in %s", nick, room.Name)
	}
	i := slices.IndexFunc(ends, func(e Endpoint) bool { return e.Member.Nick == nick })
	if i < 0 {
		return room, fmt.Errorf("rooms: invite: unknown nick %q", nick)
	}
	target := ends[i]
	next := room
	next.Members = append(slices.Clone(room.Members), target.Member)
	if err := checkNicks(next.Members); err != nil {
		return room, fmt.Errorf("rooms: invite: %w", err)
	}
	names := make([]string, len(room.Members))
	for j, m := range room.Members {
		names[j] = m.Nick
	}
	body := fmt.Sprintf(inviteIntro, room.Name) + " (send to " + room.Name.String() + "; use @nick to mention)." +
		"\nTopic: " + indent(room.Topic) + "\nMembers: " + strings.Join(names, ", ")
	if local, _, isTeammate := strings.Cut(nick, "@"); isTeammate { // D:229: a teammate is reached via its parent
		body = "@" + local + " " + body
	}
	f, err := peer.NewUserFrame(roomAddr, room.Name.String(), target.Class, body)
	if err != nil {
		return room, fmt.Errorf("rooms: invite %s: %w", nick, err)
	}
	if err := send(target.Sock, f); err != nil {
		return room, fmt.Errorf("rooms: invite %s: %w", nick, err)
	}
	if err := Save(dir, next); err != nil {
		return next, fmt.Errorf("rooms: invited but room not saved: %w", err) // the frame is out: keep the member in memory
	}
	return next, nil
}
