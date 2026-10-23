package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"ircc/internal/model"
	"ircc/internal/peer"
	"ircc/internal/rooms"
	"ircc/internal/ui"
)

// cmdRT is the part of roomRT the commands use. setRoom replaces the stored
// room (topic, members); it is added to roomRT by T40's follow-up.
type cmdRT interface {
	open(name rooms.Name, pidDomain string) error
	closeRoom(name rooms.Name) error
	room(name rooms.Name) (rooms.Room, bool)
	endpoint(name rooms.Name) (*peer.Endpoint, bool)
	names() []string
	setRoom(r rooms.Room) error
}

var _ cmdRT = (*roomRT)(nil)

// errUnsupported is the answer to /mode: DESIGN :318 lists it
// without a protocol.
var errUnsupported = errors.New("unsupported: DESIGN :318 defines no protocol (GAP-10, vic)")

// cmds runs parsed commands. dir is the rooms dir; send delivers invite frames;
// ends lists the live endpoints a room's members could be; sendCh sends text to
// a session channel; emit hands messages to the UI.
type cmds struct {
	rt        cmdRT
	dir       string
	pidDomain string
	emit      func(tea.Msg)
	send      rooms.SendFunc
	// invite is rooms.Invite (not on this base yet; T43 wires it).
	inviteFn func(send rooms.SendFunc, dir string, roomAddr peer.Addr, room rooms.Room, ends []rooms.Endpoint, nick string) (rooms.Room, error)
	ends     func(rooms.Room) []rooms.Endpoint
	sendCh   func(ch, text string) error

	mu     sync.Mutex
	hidden map[string]bool // parted rooms: kept by the runtime, not shown
}

// cmd is the ui.App handler: the work runs in the returned tea.Cmd and an error
// comes back as a dim NoteMsg.
func (c *cmds) cmd(ch string, cm model.Cmd) tea.Cmd {
	return func() tea.Msg {
		if err := c.do(ch, cm); err != nil {
			return ui.NoteMsg{L: dim(err.Error())}
		}
		return nil
	}
}

// do runs cm; ch is the focused channel, the default room of /part /close
// /topic /kick /names /who.
func (c *cmds) do(ch string, cm model.Cmd) error {
	args := cm.Args()
	switch cm.Name() {
	case model.CmdJoin:
		return c.join(args[0])
	case model.CmdPart:
		return c.part(roomArg(args, ch))
	case model.CmdClose:
		return c.closeRoom(roomArg(args, ch))
	case model.CmdTopic:
		return c.update(ch, func(r *rooms.Room) error { r.Topic = args[0]; return nil })
	case model.CmdInvite:
		return c.invite(args[0], args[1])
	case model.CmdKick:
		return c.update(ch, func(r *rooms.Room) error { return kick(r, args[0]) })
	case model.CmdMsg:
		t := args[0]
		if !strings.HasPrefix(t, "#") {
			t = "#" + t
		}
		return c.sendCh(t, args[1])
	case model.CmdNames, model.CmdWho:
		return c.members(roomArg(args, ch))
	case model.CmdMode:
		return errUnsupported
	}
	return fmt.Errorf("command %q is not handled here", cm.Name())
}

func roomArg(args []string, ch string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ch
}

// existing resolves s to an open room.
func (c *cmds) existing(s string) (rooms.Name, rooms.Room, error) {
	n, err := rooms.ParseName(s)
	if err != nil {
		return rooms.Name{}, rooms.Room{}, err
	}
	r, ok := c.rt.room(n)
	if !ok {
		return rooms.Name{}, rooms.Room{}, fmt.Errorf("no room %s", s)
	}
	return n, r, nil
}

// shown tells the UI the visible rooms: the open ones minus the parted.
func (c *cmds) shown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	vis := []string{}
	for _, n := range c.rt.names() {
		if !c.hidden[n] {
			vis = append(vis, n)
		}
	}
	for _, m := range roomMsgs(c.rt, vis) {
		c.emit(m)
	}
}

func (c *cmds) join(s string) error {
	n, err := rooms.ParseName(s)
	if err != nil {
		return err
	}
	c.mu.Lock()
	parted := c.hidden[n.String()]
	delete(c.hidden, n.String())
	c.mu.Unlock()
	if !parted {
		if err := c.rt.open(n, c.pidDomain); err != nil {
			return err
		}
	}
	c.shown()
	c.emit(ui.FocusMsg(n.String()))
	return nil
}

func (c *cmds) part(s string) error {
	n, _, err := c.existing(s)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.hidden == nil {
		c.hidden = map[string]bool{}
	}
	c.hidden[n.String()] = true
	c.mu.Unlock()
	c.shown()
	return nil
}

func (c *cmds) closeRoom(s string) error {
	n, _, err := c.existing(s)
	if err != nil {
		return err
	}
	if err := c.rt.closeRoom(n); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.hidden, n.String())
	c.mu.Unlock()
	c.shown()
	return nil
}

// update applies f to the room ch names, then stores and saves it.
func (c *cmds) update(ch string, f func(*rooms.Room) error) error {
	_, r, err := c.existing(ch)
	if err != nil {
		return err
	}
	if err := f(&r); err != nil {
		return err
	}
	defer c.shown()
	return c.rt.setRoom(r) // saves first; memory changes only on success
}

func kick(r *rooms.Room, nick string) error {
	i := slices.IndexFunc(r.Members, func(m rooms.Member) bool { return m.Nick == nick })
	if i < 0 {
		return fmt.Errorf("%s is not in %s", nick, r.Name)
	}
	r.Members = slices.Delete(slices.Clone(r.Members), i, i+1)
	return nil
}

func (c *cmds) invite(nick, room string) error {
	n, r, err := c.existing(room)
	if err != nil {
		return err
	}
	ep, ok := c.rt.endpoint(n)
	if !ok {
		return fmt.Errorf("no room %s", room)
	}
	next, err := c.inviteFn(c.send, c.dir, ep.Addr, r, c.ends(r), nick)
	if len(next.Members) != len(r.Members) { // the frame went out: keep the member even if Save failed
		defer c.shown()
		return errors.Join(err, c.rt.setRoom(next))
	}
	return err
}

func (c *cmds) members(s string) error {
	_, r, err := c.existing(s)
	if err != nil {
		return err
	}
	nicks := make([]string, len(r.Members))
	for i, m := range r.Members {
		nicks[i] = m.Nick
	}
	c.emit(ui.NoteMsg{L: dim(fmt.Sprintf("%s members: %s", r.Name, strings.Join(nicks, ", ")))})
	return nil
}

// roomMsgs shows exactly the rooms vis, each with its member nicks.
func roomMsgs(rt cmdRT, vis []string) []tea.Msg {
	out := []tea.Msg{ui.RoomsMsg(vis)}
	for _, s := range vis {
		n, err := rooms.ParseName(s)
		if err != nil {
			continue
		}
		r, _ := rt.room(n)
		nicks := make([]string, len(r.Members))
		for i, m := range r.Members {
			nicks[i] = m.Nick
		}
		out = append(out, ui.MembersMsg{Ch: s, Nicks: nicks})
	}
	return out
}
