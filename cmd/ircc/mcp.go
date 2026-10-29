package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"ircc/internal/mcp"
	"ircc/internal/model"
	"ircc/internal/peer"
	"ircc/internal/registry"
	"ircc/internal/rooms"
	"ircc/internal/ui"
)

const (
	vicRoom      = "&vic"          // vic's trusted channel; every MCP caller joins it (I6)
	maxWalk      = 64              // /proc parent steps from the bridge to its session (I3)
	queryTimeout = 2 * time.Second // the TUI answers a ui.QueryMsg within this
	historyMax   = ringCap
)

// mcpSock is $XDG_RUNTIME_DIR/ircc/mcp.sock (I1).
func mcpSock() (string, error) {
	d := os.Getenv("XDG_RUNTIME_DIR")
	if d == "" {
		return "", errors.New("mcp: XDG_RUNTIME_DIR is not set")
	}
	return filepath.Join(d, "ircc", "mcp.sock"), nil
}

// runMCP is `ircc mcp`, the stateless stdio bridge (I2).
func runMCP() int {
	sock, err := mcpSock()
	if err != nil {
		sock = "" // no runtime dir: no TUI listens; the bridge answers with the I2 error
	}
	if err := mcp.Bridge(os.Stdin, os.Stdout, sock); err != nil {
		fmt.Fprintln(os.Stderr, "ircc mcp:", err)
		return 1
	}
	return 0
}

// listenMCP binds the MCP socket, mode 0600 in a 0700 dir (I1).
func listenMCP(path string) (*peer.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mcp: %w", err)
	}
	return peer.Listen(path)
}

// serveMCP runs the MCP listener of runTUI until ctx is done; a failure to
// listen is a note, the TUI goes on without MCP.
func serveMCP(ctx context.Context, r *runner, cs *cmds, p *tea.Program) {
	sock, err := mcpSock()
	var l *peer.Listener
	if err == nil {
		l, err = listenMCP(sock)
	}
	if err != nil {
		r.emit(ui.NoteMsg{L: dim(err.Error())})
		return
	}
	newMCPSrv(r, cs, func() (ui.Snapshot, error) { return query(p) }).serve(ctx, l)
}

// query asks the App for what the TUI user sees.
func query(p *tea.Program) (ui.Snapshot, error) {
	reply := make(chan ui.Snapshot, 1)
	p.Send(ui.QueryMsg{Reply: reply})
	select {
	case s := <-reply:
		return s, nil
	case <-time.After(queryTimeout):
		return ui.Snapshot{}, errors.New("the TUI did not answer")
	}
}

// mcpSrv turns MCP tool calls into model.Cmd runs on cmds (mutations) or reads.
type mcpSrv struct {
	r    *runner
	cs   *cmds
	snap func() (ui.Snapshot, error)
	uid  int

	mu     sync.Mutex
	joined map[string]bool // callers already in &vic (I6)
}

func newMCPSrv(r *runner, cs *cmds, snap func() (ui.Snapshot, error)) *mcpSrv {
	return &mcpSrv{r: r, cs: cs, snap: snap, uid: os.Getuid(), joined: map[string]bool{}}
}

// serve accepts MCP sessions on l until ctx is done; then l and every session close.
func (s *mcpSrv) serve(ctx context.Context, l *peer.Listener) {
	stop := context.AfterFunc(ctx, func() { _ = l.Close() })
	defer stop()
	note := rateLimited(func(l model.Line) { s.r.emit(ui.NoteMsg{L: l}) }, noteEvery, time.Now)
	serveLoop(ctx, l.Accept, func(c *peer.Conn) {
		stop := context.AfterFunc(ctx, func() { _ = c.Close() })
		defer stop()
		s.handle(c)
	}, note, acceptBackoff)
}

// handle is one MCP session. Another uid or a caller with no live session up
// its /proc chain is refused: the connection closes unanswered (I1, I3).
func (s *mcpSrv) handle(c *peer.Conn) {
	pid, uid, err := c.PeerCred()
	if err != nil || uid != s.uid {
		return
	}
	a, ok := s.r.callerOf(pid)
	if !ok {
		return
	}
	_ = mcp.Serve(c, c, func(name string, args json.RawMessage) (string, error) { return s.call(a, name, args) })
}

// callerOf walks the /proc parent chain from pid to a live session: that
// session's nick is the caller (I3). Never the TUI author.
func (r *runner) callerOf(pid int) (author, bool) {
	for range maxWalk {
		r.mu.Lock()
		_, live := r.live[pid]
		ch := r.names[pid]
		r.mu.Unlock()
		if live && ch != "" {
			return author{nick: strings.TrimPrefix(ch, "#"), pid: pid}, true
		}
		next, err := registry.ParentPID(r.procRoot, pid)
		if err != nil || next <= 1 || next == pid {
			return author{}, false
		}
		pid = next
	}
	return author{}, false
}

// toolArgs holds the arguments of every tool (I4 schemas).
type toolArgs struct {
	Room, Nick, Text, To, Channel, Since string
	Limit                                int
	Tools                                bool
}

func (s *mcpSrv) call(a author, name string, raw json.RawMessage) (string, error) {
	s.joinVic(a)
	var in toolArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	switch name {
	case "ircc_list":
		return s.list()
	case "ircc_names":
		return s.room(in.Room)
	case "ircc_whois":
		return s.r.whois(in.Nick)
	case "ircc_history":
		return s.history(in)
	case "ircc_grep":
		return s.grep(in)
	case "ircc_msg":
		to := in.To
		if !strings.HasPrefix(to, "#") && !strings.HasPrefix(to, "&") {
			to = "#" + to
		}
		if err := s.mutate(a, to, model.CmdMsg, to, in.Text); err != nil {
			return "", err
		}
		return "delivered: yes", nil
	case "ircc_close":
		if err := s.mutate(a, in.Room, model.CmdClose, in.Room); err != nil {
			return "", err
		}
		return "closed " + in.Room, nil
	case "ircc_part":
		if err := s.mutate(a, in.Room, model.CmdPart, in.Room); err != nil {
			return "", err
		}
		return "left " + in.Room, nil
	}
	var err error
	switch name {
	case "ircc_join":
		err = s.mutate(a, in.Room, model.CmdJoin, in.Room)
	case "ircc_invite":
		err = s.mutate(a, in.Room, model.CmdInvite, in.Nick, in.Room)
	case "ircc_kick":
		err = s.mutate(a, in.Room, model.CmdKick, in.Nick)
	case "ircc_topic":
		err = s.mutate(a, in.Room, model.CmdTopic, in.Text)
	default:
		return "", fmt.Errorf("tool %s is not handled here", name)
	}
	if err != nil {
		return "", err
	}
	return s.room(in.Room)
}

// mutate builds the Cmd a / line would, runs it on cmds.do as a (one path for
// TUI and MCP) and shows the dim "<nick> via mcp: /cmd" line in ch (I5).
func (s *mcpSrv) mutate(a author, ch string, name model.CmdName, args ...string) error {
	cm, err := model.NewCmd(name, args...)
	if err != nil {
		return err
	}
	if err := s.cs.do(a, ch, cm); err != nil {
		return err
	}
	via := dim(a.nick + " via mcp: " + cm.String())
	if name == model.CmdClose { // the channel is gone
		s.r.emit(ui.ToastMsg{Text: via.Text})
	} else {
		s.r.emit(ui.LineMsg{Ch: ch, L: via})
	}
	return nil
}

// joinVic makes a a member of &vic on its first call (I6); a failure is retried
// on the next call.
func (s *mcpSrv) joinVic(a author) {
	s.mu.Lock()
	done := s.joined[a.nick]
	s.mu.Unlock()
	if done || s.mutate(a, vicRoom, model.CmdJoin, vicRoom) != nil {
		return
	}
	s.mu.Lock()
	s.joined[a.nick] = true
	s.mu.Unlock()
}

type nickStatus struct {
	Nick   string `json:"nick"`
	Status string `json:"status"`
}

type roomOut struct {
	Room    string       `json:"room"`
	Topic   string       `json:"topic"`
	Members []nickStatus `json:"members"`
}

// jsonText is v as JSON with & < > kept as is: room names start with &.
func jsonText(v any) (string, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	err := enc.Encode(v)
	return strings.TrimSuffix(b.String(), "\n"), err
}

// room is a room with its topic and members' status.
func (s *mcpSrv) room(name string) (string, error) {
	_, r, err := s.cs.existing(name)
	if err != nil {
		return "", err
	}
	out := roomOut{Room: r.Name.String(), Topic: r.Topic, Members: []nickStatus{}}
	for _, m := range r.Members {
		out.Members = append(out.Members, nickStatus{m.Nick, s.r.status(m.Nick)})
	}
	return jsonText(out)
}

// status is busy or idle for a session nick or a teammate agent@session.
func (r *runner) status(nick string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	agent, sess, teammate := strings.Cut(nick, "@")
	if !teammate {
		sess = nick
	}
	for pid, ch := range r.names {
		if ch != "#"+sess {
			continue
		}
		busy := r.live[pid].Status == registry.Busy
		if teammate {
			t := r.tails[pid]
			busy = t != nil && slices.ContainsFunc(t.list, func(ag model.Agent) bool { return ag.Nick == agent && ag.Busy })
		}
		if busy {
			return "busy"
		}
	}
	return "idle"
}

type chanOut struct {
	Name    string       `json:"name"`
	Kind    string       `json:"kind"`
	Topic   string       `json:"topic,omitempty"`
	Members []nickStatus `json:"members"`
	Unread  int          `json:"unread"`
}

// list is the channel pane and nick pane (I4): the TUI user first in each.
func (s *mcpSrv) list() (string, error) {
	sn, err := s.snap()
	if err != nil {
		return "", err
	}
	user := nickStatus{sn.User, "idle"}
	if sn.Typing {
		user.Status = "typing"
	}
	out := []chanOut{}
	for _, c := range sn.Model.Channels() {
		co := chanOut{Name: c.Name, Kind: "#", Members: []nickStatus{user}, Unread: c.Unread}
		if c.Kind == model.RoomChan {
			co.Kind = "&"
			if n, err := rooms.ParseName(c.Name); err == nil {
				r, _ := s.r.rt.room(n)
				co.Topic = r.Topic
				for _, m := range r.Members {
					co.Members = append(co.Members, nickStatus{m.Nick, s.r.status(m.Nick)})
				}
			}
		} else {
			main := strings.TrimPrefix(c.Name, "#")
			co.Members = append(co.Members, nickStatus{main, s.r.status(main)})
			for _, ag := range c.Agents {
				st := "idle"
				if ag.Busy {
					st = "busy"
				}
				co.Members = append(co.Members, nickStatus{ag.Nick + "@" + main, st})
			}
		}
		out = append(out, co)
	}
	return jsonText(map[string]any{"channels": out})
}

type lineOut struct {
	Channel string `json:"channel,omitempty"`
	Time    string `json:"time"`
	Nick    string `json:"nick"`
	Text    string `json:"text"`
}

func toLine(ch string, l model.Line) lineOut {
	return lineOut{Channel: ch, Time: l.At.Format(time.RFC3339), Nick: l.Nick, Text: l.Text}
}

// history is the scrollback of one channel: the last limit lines at or after
// since, tool lines only with tools.
func (s *mcpSrv) history(in toolArgs) (string, error) {
	sn, err := s.snap()
	if err != nil {
		return "", err
	}
	var since time.Time
	if in.Since != "" {
		if since, err = time.Parse(time.RFC3339, in.Since); err != nil {
			return "", fmt.Errorf("since: %w", err)
		}
	}
	limit := in.Limit
	if limit <= 0 || limit > historyMax {
		limit = historyMax
	}
	chans := sn.Model.Channels()
	i := slices.IndexFunc(chans, func(c model.Channel) bool { return c.Name == in.Channel })
	if i < 0 {
		return "", fmt.Errorf("no channel %s", in.Channel)
	}
	out := []lineOut{}
	for _, l := range chans[i].Lines.All() {
		if (l.Kind == model.Tool && !in.Tools) || l.At.Before(since) {
			continue
		}
		out = append(out, toLine("", l))
	}
	return jsonText(map[string]any{"lines": out[max(len(out)-limit, 0):]})
}

// grep is ctrl+g over one channel or all: case-insensitive substring of the
// text; tool lines are left out, as the TUI hides them by default.
func (s *mcpSrv) grep(in toolArgs) (string, error) {
	sn, err := s.snap()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Text) == "" {
		return "", errors.New("grep: empty text")
	}
	q := strings.ToLower(in.Text)
	out, found := []lineOut{}, false
	for _, c := range sn.Model.Channels() {
		if in.Channel != "all" && c.Name != in.Channel {
			continue
		}
		found = true
		for _, l := range c.Lines.All() {
			if l.Kind != model.Tool && strings.Contains(strings.ToLower(l.Text), q) {
				out = append(out, toLine(c.Name, l))
			}
		}
	}
	if !found {
		return "", fmt.Errorf("no channel %s", in.Channel)
	}
	return jsonText(map[string]any{"lines": out})
}

// sendAs sends text on ch as a: the TUI user's own send, or an MCP caller's
// line under its nick and its own permission class, never the target's
// (R1; the class rule of rooms.AgentWriter, R8). Unknown mode fails closed.
func (r *runner) sendAs(a author, ch, text string) error {
	if a.vic {
		return r.send(ch, text)
	}
	r.mu.Lock()
	mode := ""
	if t := r.tails[a.pid]; t != nil {
		mode = t.mode
	}
	r.mu.Unlock()
	class, err := peer.ClassOf(mode)
	if err != nil {
		return fmt.Errorf("permission mode of %s unknown: %w", a.nick, err)
	}
	if strings.HasPrefix(ch, "&") {
		return r.agentSay(a, class, ch, text)
	}
	e, _, ep, err := r.target(ch)
	if err != nil {
		return err
	}
	f, err := peer.NewUserFrame(ep.Addr, a.nick, class, text)
	if err != nil {
		return err
	}
	if err := r.sendFrame(e.SocketPath, f.Bytes()); err != nil {
		return err
	}
	r.emit(ui.LineMsg{Ch: ch, L: model.Line{At: time.Now(), Nick: a.nick, Text: text}})
	return nil
}

// agentSay is say for an MCP caller: a member's line, fanned out like a frame
// from that member's session.
func (r *runner) agentSay(a author, class peer.Class, ch, text string) error {
	name, err := rooms.ParseName(ch)
	if err != nil {
		return err
	}
	room, ep, err := r.openRoom(name)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(room.Members, func(m rooms.Member) bool { return m.Nick == a.nick }) {
		return fmt.Errorf("%s is not in %s: join it first", a.nick, ch)
	}
	w, err := rooms.AgentWriter(a.nick, class)
	if err != nil {
		return err
	}
	ends, skipped := r.endsFor(room)
	r.skipNote(name, skipped)
	err = rooms.Fanout(r.sendTo, ep.Addr, room, ends, w, text)
	r.roomLine(ch, a.nick, text, false)
	return err
}
