package model

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// Member is one room participant: its nick and session pid.
type Member struct {
	Nick string
	PID  int
}

// ErrNotRoom means the channel is a session channel, which has no members.
var ErrNotRoom = errors.New("model: channel is not a room")

// LineKind is where a line came from; the UI can hide all but Chat.
type LineKind uint8

const (
	Chat       LineKind = iota // user lines, peer replies, room lines, notes
	Transcript                 // session text read from its transcript
	Tool                       // tool_use and tool errors from the transcript
)

// Line is one scrollback entry.
type Line struct {
	At         time.Time
	Nick, Text string
	Dim        bool
	Kind       LineKind
}

// ChanKind tells a session channel (#) from a room (&).
type ChanKind uint8

const (
	SessionChan ChanKind = iota
	RoomChan
)

// Channel is one buffer; Gone means the session pid is dead (greyed, scrollback kept).
type Channel struct {
	Name    string
	Kind    ChanKind
	PID     int
	Cwd     string // session channels: the session's working directory, for relative file clicks
	Gone    bool
	Unread  int
	Lines   Ring[Line]
	Members []Member // rooms only; set by WithRoomMembers
	Agents  []Agent  // # channels only: the session's subagents; set by WithAgents
}

// Model is the channel list. Updates return a new Model; AddLine gives the
// touched channel its own Ring (copy-on-write), untouched channels share theirs.
type Model struct {
	chans []Channel
	cur   int
	cap   Cap
}

// New returns an empty model whose channels keep n lines each.
func New(n int) (Model, error) {
	c, err := NewCap(n)
	if err != nil {
		return Model{}, err
	}
	return Model{cap: c}, nil
}

// Channels returns a copy of the channel list (members included): sessions (#) before rooms (&).
func (m Model) Channels() []Channel {
	out := append([]Channel(nil), m.chans...)
	for i := range out {
		out[i].Members = slices.Clone(out[i].Members)
		out[i].Agents = slices.Clone(out[i].Agents)
	}
	return out
}

// WithRoomMembers returns a Model whose room ch has the given members (copied).
func (m Model) WithRoomMembers(ch string, members []Member) (Model, error) {
	for i, c := range m.chans {
		if c.Name != ch {
			continue
		}
		if c.Kind != RoomChan {
			return m, fmt.Errorf("%w: %s", ErrNotRoom, ch)
		}
		c.Members = slices.Clone(members)
		for j := range c.Members {
			c.Members[j].Nick = clean(c.Members[j].Nick)
		}
		return m.withChan(i, c), nil
	}
	return m, fmt.Errorf("%w: %s", ErrUnknownChannel, ch)
}

// WithSessions syncs session channels to ss: new ones are added, absent ones
// are marked Gone with their lines kept, returning ones are revived.
func (m Model) WithSessions(ss []Session) Model {
	names := ChanNames(ss)
	old := map[int]Channel{}
	for _, c := range m.chans {
		if c.Kind == SessionChan {
			old[c.PID] = c
		}
	}
	live := map[int]bool{}
	var out []Channel
	for _, c := range m.chans { // keep existing order (most recent first); new sessions go last
		if c.Kind != SessionChan {
			out = append(out, c)
			continue
		}
		for _, s := range ss {
			if s.PID == c.PID {
				live[c.PID] = true
				c.Name, c.Cwd, c.Gone = clean(names[c.PID]), s.Cwd, false
			}
		}
		if !live[c.PID] {
			c.Gone = true
		}
		out = append(out, c)
	}
	for _, s := range ss {
		if _, seen := old[s.PID]; seen {
			continue
		}
		out = append(out, Channel{Name: clean(names[s.PID]), Kind: SessionChan, PID: s.PID, Cwd: s.Cwd, Lines: RingOf[Line](m.cap)})
	}
	n := m
	n.chans = out
	if m.cur < len(m.chans) { // keep focus on the same channel after reordering
		c := m.chans[m.cur]
		for i, x := range n.chans {
			if x.Kind == c.Kind && x.PID == c.PID && (c.Kind == SessionChan || x.Name == c.Name) {
				n.cur = i
			}
		}
	}
	return n
}

// Agent is a subagent or teammate of a session, a nick in its # channel (D:33).
type Agent struct {
	Nick string
	Busy bool
}

// WithAgents returns a Model whose session channel ch lists agents (copied).
func (m Model) WithAgents(ch string, agents []Agent) (Model, error) {
	for i, c := range m.chans {
		if c.Name != ch {
			continue
		}
		if c.Kind != SessionChan {
			return m, fmt.Errorf("model: %s is a room: rooms have members, not agents", ch)
		}
		c.Agents = slices.Clone(agents)
		for j := range c.Agents {
			c.Agents[j].Nick = clean(c.Agents[j].Nick)
		}
		return m.withChan(i, c), nil
	}
	return m, fmt.Errorf("%w: %s", ErrUnknownChannel, ch)
}
