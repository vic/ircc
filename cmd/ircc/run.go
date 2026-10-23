package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"ircc/internal/model"
	"ircc/internal/peer"
	"ircc/internal/registry"
	"ircc/internal/rooms"
	"ircc/internal/transcript"
	"ircc/internal/ui"
)

const (
	ringCap   = 1000
	pollEvery = 500 * time.Millisecond
	// seedBytes is how much of a transcript's tail is read to learn the current
	// permissionMode when a session is first seen.
	seedBytes = 256 << 10
	// watchRetries re-Watches after the channel closes under a live ctx; backoff
	// grows linearly (watchBackoff, 2x, 3x). A watch that stayed up for
	// watchStable resets the count.
	watchRetries = 3
	watchBackoff = 500 * time.Millisecond
	watchStable  = 10 * time.Second
)

// claudeDir is ~/.claude, or IRCC_CLAUDE_DIR (tests).
func claudeDir() (string, error) {
	if d := os.Getenv("IRCC_CLAUDE_DIR"); d != "" {
		return d, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("claude dir: %w", err)
	}
	return filepath.Join(h, ".claude"), nil
}

type tail struct {
	t      *transcript.Tailer // used by the poller only
	mode   string             // last permissionMode seen, "" until one is; guarded by runner.mu
	old    bool               // the first batch is history: shown, not acted on, not unread; poller only
	reply  bool               // the session is answering a line we sent: its text is a direct reply; poller only
	path   string             // the session transcript; its dir holds <sessionId>/subagents
	sizes  map[string]int64   // per subagent transcript: size when busy was last read; poller only
	busy   map[string]bool    // per subagent transcript: last Busy result; poller only
	agents string             // last AgentsMsg sent, as a key; poller only
	list   []model.Agent      // last agent list; guarded by runner.mu (invitable reads it)
}

// runner holds the producers' state; emit hands messages to the UI. r.mu guards
// the maps only: file, socket and emit calls happen outside it.
type runner struct {
	claude, procRoot, user string
	emit                   func(tea.Msg)
	sendFrame              func(sock string, b []byte) error

	mu       sync.Mutex
	live     map[int]registry.Entry
	names    map[int]string // pid -> "#channel"
	tails    map[int]*tail
	warned   map[int]bool // transcript-open failure already shown
	epWarned bool         // endpoint-open failure already shown

	rt *roomRT // room runtime (T40); nil until the TUI wires it

	epMu   sync.Mutex
	ep     *peer.Endpoint     // user endpoint, opened on first send
	epDir  string             // its socks dir: every target must live there (m26)
	stopIn context.CancelFunc // stops serveInbound
	inDone chan struct{}
}

func newRunner(claude, procRoot, user string, emit func(tea.Msg)) *runner {
	return &runner{claude: claude, procRoot: procRoot, user: user, emit: emit, sendFrame: peer.Send,
		live: map[int]registry.Entry{}, names: map[int]string{}, tails: map[int]*tail{}, warned: map[int]bool{}}
}

func (r *runner) sessDir() string { return filepath.Join(r.claude, "sessions") }

func dim(text string) model.Line { return model.Line{At: time.Now(), Text: text, Dim: true} }

// warn shows a dim note on the first channel by pid; dropped when no session is live.
func (r *runner) warn(text string) {
	r.mu.Lock()
	pids := make([]int, 0, len(r.names))
	for p := range r.names {
		pids = append(pids, p)
	}
	sort.Ints(pids)
	ch := ""
	if len(pids) > 0 {
		ch = r.names[pids[0]]
	}
	r.mu.Unlock()
	if ch != "" {
		r.emit(ui.LineMsg{Ch: ch, L: dim(text)})
	}
}

// note shows a dim note on pid's channel.
func (r *runner) note(pid int, text string) {
	r.mu.Lock()
	ch := r.names[pid]
	r.mu.Unlock()
	r.emit(ui.LineMsg{Ch: ch, L: dim(text)})
}

// onSnapshot replaces the live set and tells the UI.
func (r *runner) onSnapshot(s registry.Snapshot) {
	live := map[int]registry.Entry{}
	var ss []model.Session
	for _, e := range s.Entries {
		if registry.IsOwn(e.SocketPath) {
			continue // our own endpoints
		}
		live[e.PID] = e
		ss = append(ss, model.Session{PID: e.PID, Name: e.Name, Cwd: e.Cwd})
	}
	names := model.ChanNames(ss)
	r.mu.Lock()
	r.live, r.names = live, names
	for pid := range r.tails {
		if _, ok := live[pid]; !ok {
			delete(r.tails, pid)
			delete(r.warned, pid)
		}
	}
	r.mu.Unlock()
	r.emit(ui.SessionsMsg(ss))
	r.ensureEndpoint(live)
	for pid, e := range live {
		r.attach(pid, e)
	}
	r.emit(r.info())
	for _, err := range s.Errs {
		r.warn("! " + err.Error())
	}
}

// ensureEndpoint opens the user endpoint at the first snapshot with a live entry, in
// the socks dir of the lowest pid (GAP-5), so inbound replies work before the first
// send. A failure is noted once; send retries and reports its own error.
func (r *runner) ensureEndpoint(live map[int]registry.Entry) {
	if len(live) == 0 {
		return
	}
	first := 0
	for pid := range live {
		if first == 0 || pid < first {
			first = pid
		}
	}
	if _, err := r.endpoint(live[first]); err != nil {
		r.mu.Lock()
		again := r.epWarned
		r.epWarned = true
		r.mu.Unlock()
		if !again {
			r.warn("! " + err.Error())
		}
	}
}

// info is the modeline data; a mode peer.ClassOf rejects is shown as unknown.
func (r *runner) info() ui.InfoMsg {
	r.mu.Lock()
	defer r.mu.Unlock()
	info := ui.InfoMsg{}
	for pid, e := range r.live {
		si := model.SessionInfo{Status: "idle"}
		if e.Status == registry.Busy {
			si.Status = "busy"
		}
		if t := r.tails[pid]; t != nil {
			if _, err := peer.ClassOf(t.mode); err == nil {
				si.PermMode = t.mode
			}
		}
		info[pid] = si
	}
	return info
}

// seed reads the last seedBytes of the transcript at path. It returns the last
// permissionMode ("" if none) and the offset of the first whole line in that span:
// the tail starts there, so a channel shows recent history (vic 2026-10-22).
// On a read error the tail starts at size: no history, mode unknown.
func seed(path string, size int64) (mode string, start int64) {
	f, err := os.Open(path)
	if err != nil {
		return "", size
	}
	defer f.Close()
	off := max(size-seedBytes, 0)
	buf := make([]byte, size-off)
	if _, err := f.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
		return "", size
	}
	if off > 0 { // the first line is cut: drop it
		i := 0
		for i < len(buf) && buf[i] != '\n' {
			i++
		}
		i = min(i+1, len(buf))
		buf, off = buf[i:], off+int64(i)
	}
	var evs []transcript.Event
	from := 0
	for i, c := range buf {
		if c != '\n' {
			continue
		}
		if ev, err := transcript.ParseEvent(buf[from:i]); err == nil {
			evs = append(evs, ev)
		}
		from = i + 1
	}
	return transcript.LastMode(evs), off
}

// attach returns pid's tail, opening it at the end of the transcript (no history
// replay) with the mode seeded from its tail. nil and one dim note per session
// when the transcript cannot be opened; retried on the next call.
func (r *runner) attach(pid int, e registry.Entry) *tail {
	r.mu.Lock()
	t := r.tails[pid]
	r.mu.Unlock()
	if t != nil {
		return t
	}
	t, err := r.open(e)
	if err != nil {
		r.mu.Lock()
		first := !r.warned[pid]
		r.warned[pid] = true
		r.mu.Unlock()
		if first {
			r.note(pid, "! transcript: "+err.Error())
		}
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur := r.tails[pid]; cur != nil { // lost a race with the other producer
		return cur
	}
	r.tails[pid] = t
	delete(r.warned, pid)
	return t
}

func (r *runner) open(e registry.Entry) (*tail, error) {
	p, err := transcript.Find(r.claude, e.SessionID)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	mode, start := seed(p, fi.Size())
	tl, err := transcript.Open(p, start)
	if err != nil {
		return nil, err
	}
	return &tail{t: tl, mode: mode, old: start < fi.Size(), path: p, sizes: map[string]int64{}, busy: map[string]bool{}}, nil
}

// poll reads new transcript lines of every live session. A line that does not
// parse becomes a note; the tail goes on.
func (r *runner) poll() {
	type job struct {
		pid  int
		e    registry.Entry
		name string
	}
	r.mu.Lock()
	jobs := make([]job, 0, len(r.live))
	for pid, e := range r.live {
		jobs = append(jobs, job{pid, e, r.names[pid]})
	}
	r.mu.Unlock()
	changed := false
	for _, j := range jobs {
		t := r.attach(j.pid, j.e)
		if t == nil {
			continue
		}
		if m, ok := t.agentsMsg(j.name, j.e.SessionID); ok {
			r.mu.Lock()
			t.list = m.Agents
			r.mu.Unlock()
			r.emit(m)
		}
		lines, err := t.t.Lines()
		old := t.old
		t.old = false
		if errors.Is(err, transcript.ErrTruncated) {
			r.mu.Lock()
			delete(r.tails, j.pid) // reattach next poll
			r.mu.Unlock()
			continue
		}
		if err != nil {
			r.note(j.pid, "! transcript read: "+err.Error())
		}
		for _, l := range lines {
			ev, err := transcript.ParseEvent(l)
			if err != nil {
				r.emit(ui.LineMsg{Ch: j.name, L: dim("! unparsable event: " + err.Error())})
				continue
			}
			if ev.PermissionMode != "" {
				r.mu.Lock()
				if ev.PermissionMode != t.mode {
					t.mode, changed = ev.PermissionMode, true
				}
				r.mu.Unlock()
			}
			if !old { // history must not redo side effects (held lines into room logs)
				r.onEvent(j.pid, ev)
			}
			if ev.Type == "" {
				continue
			}
			switch prompt(ev) {
			case promptOurs:
				t.reply = true
			case promptOther:
				t.reply = false
			}
			for _, l := range blockLines(ev, j.e.Name, r.user) {
				if t.reply && l.Kind == model.Transcript && ev.Type == "assistant" {
					l.Kind = model.Chat // an answer to our line: m (mentions) shows it
				}
				r.emit(ui.LineMsg{Ch: j.name, L: l, Old: old})
			}
		}
	}
	if changed {
		r.emit(r.info())
	}
}

// endpoint opens the user endpoint on first use, in the target's socket dir (GAP-5).
func (r *runner) endpoint(e registry.Entry) (*peer.Endpoint, error) {
	r.epMu.Lock()
	defer r.epMu.Unlock()
	if r.ep == nil {
		ep, err := peer.OpenUser(filepath.Dir(e.SocketPath), r.sessDir(), r.procRoot, r.user, e.PIDDomain)
		if err != nil {
			return nil, fmt.Errorf("open user endpoint: %w", err)
		}
		r.ep, r.epDir = ep, filepath.Dir(e.SocketPath)
		if r.rt != nil {
			r.rt.setSocks(r.epDir)
			go r.loadRooms()
		}
		ctx, stop := context.WithCancel(context.Background())
		r.stopIn, r.inDone = stop, make(chan struct{})
		go func() {
			defer close(r.inDone)
			serveInbound(ctx, ep.Listener(), r.liveCopy,
				func(ch string, l model.Line) { r.emit(ui.LineMsg{Ch: ch, L: l}) },
				func(l model.Line) { r.emit(ui.NoteMsg{L: l}) })
		}()
	}
	if d := filepath.Dir(e.SocketPath); d != r.epDir { // m26: replies only reach targets in our socks dir
		return nil, fmt.Errorf("target in another socks dir (%s, endpoint in %s)", d, r.epDir)
	}
	return r.ep, nil
}

// liveCopy is the live entry set for serveInbound.
func (r *runner) liveCopy() []registry.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	es := make([]registry.Entry, 0, len(r.live))
	for _, e := range r.live {
		es = append(es, e)
	}
	return es
}

// send writes one frame to the session behind channel ch. The permission class
// comes from the last mode seen; unknown mode fails closed.
func (r *runner) send(ch, text string) error {
	if strings.HasPrefix(ch, "&") {
		return r.say(ch, text) // fans out and echoes the line itself (logged to disk)
	}
	e, class, ep, err := r.target(ch)
	if err != nil {
		return err
	}
	f, err := peer.NewUserFrame(ep.Addr, r.user, class, text)
	if err != nil {
		return err
	}
	return r.sendFrame(e.SocketPath, f.Bytes())
}

// sendLiteral writes text as is (no wrapper) to the session behind # channel ch.
func (r *runner) sendLiteral(ch, text string) error {
	if !strings.HasPrefix(ch, "#") {
		return fmt.Errorf("%s: literal text goes to a # session; a room has no Claude socket", ch)
	}
	e, _, ep, err := r.target(ch)
	if err != nil {
		return err
	}
	f, err := peer.NewLiteralFrame(ep.Addr, text)
	if err != nil {
		return err
	}
	return r.sendFrame(e.SocketPath, f.Bytes())
}

// target is the live session behind # channel ch, its permission class and our endpoint.
// The class comes from the last mode seen; unknown mode fails closed.
func (r *runner) target(ch string) (registry.Entry, peer.Class, *peer.Endpoint, error) {
	r.mu.Lock()
	pid, mode := 0, ""
	for p, n := range r.names {
		if n == ch {
			pid = p
		}
	}
	e, ok := r.live[pid]
	if t := r.tails[pid]; t != nil {
		mode = t.mode
	}
	r.mu.Unlock()
	if !ok {
		return registry.Entry{}, 0, nil, fmt.Errorf("no live session for channel %q", ch)
	}
	class, err := peer.ClassOf(mode)
	if err != nil {
		return registry.Entry{}, 0, nil, fmt.Errorf("permission mode of %s unknown: %w", ch, err)
	}
	ep, err := r.endpoint(e)
	if err != nil {
		return registry.Entry{}, 0, nil, err
	}
	return e, class, ep, nil
}

func (r *runner) close() error {
	r.epMu.Lock()
	defer r.epMu.Unlock()
	if r.ep == nil {
		return nil
	}
	r.stopIn()
	<-r.inDone // serveInbound closed the listener and drained its connections
	return r.ep.Close()
}

// watchLoop feeds snapshots to r until ctx is done. If the channel closes under a
// live ctx it notes it and re-Watches, at most watchRetries times in a row, then
// returns an error. backoff is the base delay between tries.
func watchLoop(ctx context.Context, r *runner, watch func(context.Context) (<-chan registry.Snapshot, error), backoff time.Duration) error {
	tries := 0
	for {
		start := time.Now()
		ch, err := watch(ctx)
		if err == nil {
			for s := range ch {
				r.onSnapshot(s)
			}
			err = errors.New("registry watch closed")
		}
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(start) >= watchStable {
			tries = 0
		}
		tries++
		if tries > watchRetries {
			return fmt.Errorf("giving up after %d tries: %w", watchRetries, err)
		}
		r.warn(fmt.Sprintf("! %v; retry %d/%d", err, tries, watchRetries))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff * time.Duration(tries)):
		}
	}
}

// runTUI wires registry watch, transcript polling and the user endpoint into the App.
func runTUI(opts ...tea.ProgramOption) int {
	claude, err := claudeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ircc:", err)
		return 1
	}
	// GAP-6 (Vic gate): the user endpoint is named $USER; a session of the same name clashes.
	user := os.Getenv("USER")
	if user == "" {
		user = "user"
	}
	const procRoot = "/proc"
	if err := registry.RemoveStale(filepath.Join(claude, "sessions"), procRoot); err != nil {
		fmt.Fprintln(os.Stderr, "ircc: remove stale:", err)
	}
	m, err := model.New(ringCap)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ircc:", err)
		return 1
	}
	var p *tea.Program
	r := newRunner(claude, procRoot, user, func(msg tea.Msg) { p.Send(msg) })
	defer r.close()
	dir, err := roomsDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ircc:", err)
		return 1
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ircc:", err)
		return 1
	}
	r.rt = newRoomRT(dir, "", r.sessDir(), procRoot, self) // socks dir set by the first live session
	r.rt.onOpen = r.serveRoom
	defer r.rt.shutdown()
	cs := &cmds{rt: r.rt, dir: dir, emit: r.emit, send: r.sendTo, inviteFn: rooms.Invite, ends: r.invitable, sendCh: r.send}
	p = tea.NewProgram(ui.New(m, r.send).WithUser(user).WithCmd(cs.cmd).WithLiteral(r.sendLiteral), opts...) // alt screen: App.View

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchErr := make(chan error, 1)
	go func() {
		err := watchLoop(ctx, r, func(c context.Context) (<-chan registry.Snapshot, error) {
			return registry.Watch(c, r.sessDir(), procRoot)
		}, watchBackoff)
		if err != nil {
			watchErr <- err
			p.Quit()
		}
	}()
	go func() {
		tk := time.NewTicker(pollEvery)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				r.poll()
			}
		}
	}()
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "ircc:", err)
		return 1
	}
	select {
	case err := <-watchErr:
		fmt.Fprintln(os.Stderr, "ircc: registry:", err)
		return 1
	default:
	}
	return 0
}

// blockLines renders an event's blocks for a # channel (DESIGN D:276-286): assistant
// text as a line from the agent, tool_use as a dim "* tool", thinking and good tool
// results hidden, a failed tool result as a dim "! tool error", user text from user.
// Sidechain events speak as <agentId>@<session> (m15).
// A tool_use addressed to an ircc endpoint is hidden: the endpoint already shows it (D:282-283).
func blockLines(ev transcript.Event, session, user string) []model.Line {
	nick := session
	if ev.Sidechain && ev.AgentID != "" {
		nick = ev.AgentID + "@" + session
	}
	now := time.Now()
	var out []model.Line
	for _, b := range ev.Blocks {
		switch {
		case b.Kind == transcript.TextBlock && ev.Type == "user" && strings.Contains(b.Text, teammatePrefix):
			out = append(out, teammateLines(b.Text, now)...)
		case b.Kind == transcript.TextBlock && ev.Type == "user" && strings.Contains(b.Text, wrapperPrefix):
			if l, ok := wrappedLine(b.Text, user, now); ok {
				out = append(out, l)
			}
		case b.Kind == transcript.TextBlock && ev.Type == "user":
			out = append(out, model.Line{At: now, Nick: user, Text: b.Text, Dim: strings.HasPrefix(b.Text, "<"), Kind: model.Transcript}) // injected tags, not typed
		case b.Kind == transcript.TextBlock:
			out = append(out, model.Line{At: now, Nick: nick, Text: b.Text, Kind: model.Transcript})
		case b.Kind == transcript.ToolUseBlock && (toIRCC(b.ToolTo) || b.ToolTo == user || strings.HasPrefix(b.ToolTo, "&")): // reply shows via the endpoint
			continue
		case b.Kind == transcript.ToolUseBlock:
			out = append(out, model.Line{At: now, Nick: nick, Text: toolText(b), Dim: true, Kind: model.Tool})
		case b.Kind == transcript.ToolResultBlock && b.IsError:
			out = append(out, model.Line{At: now, Nick: nick, Text: "! tool error", Dim: true, Kind: model.Tool})
		}
	}
	return out
}

// toIRCC reports whether to is the uds: address of an ircc-owned socket.
func toIRCC(to string) bool {
	a, err := peer.ParseAddr(to)
	return err == nil && registry.IsOwn(a.Path())
}

// wrapperPrefix opens the cross-session wrapper; split so display tooling cannot mangle "<".
const wrapperPrefix = "<" + "cross-session-message"

// wrappedLine shows a cross-session wrapper typed into a session. Our own
// sent line (from an ircc- socket) is hidden, already echoed; a session-to-session
// message stays visible, dim, from its from-name; an unparsable wrapper is shown raw.
func wrappedLine(text, user string, now time.Time) (model.Line, bool) {
	w, err := peer.FindWrapper(text)
	if err != nil {
		return model.Line{At: now, Nick: user, Text: text, Dim: true, Kind: model.Transcript}, true
	}
	if registry.IsOwn(w.From.Path()) {
		return model.Line{}, false
	}
	return model.Line{At: now, Nick: w.FromName, Text: w.Body, Dim: true, Kind: model.Transcript}, true
}

// toolText is a tool line: "* Bash: go test ./...", or "* Bash" with no telling input.
func toolText(b transcript.Block) string {
	if b.ToolArg == "" {
		return "* " + b.Tool
	}
	return "* " + b.Tool + ": " + b.ToolArg
}

type promptKind uint8

const (
	promptNone  promptKind = iota // not a prompt (assistant event, tool results)
	promptOurs                    // a line ircc sent (our own wrapper)
	promptOther                   // typed in Claude, or sent by another session
)

// prompt tells who prompted the session in a user event; tool results are not prompts.
func prompt(ev transcript.Event) promptKind {
	if ev.Type != "user" {
		return promptNone
	}
	for _, b := range ev.Blocks {
		if b.Kind != transcript.TextBlock {
			continue
		}
		if w, err := peer.FindWrapper(b.Text); err == nil && registry.IsOwn(w.From.Path()) {
			return promptOurs
		}
		if strings.HasPrefix(b.Text, "<") { // injected tags (system reminders) are nobody's prompt
			continue
		}
		return promptOther
	}
	return promptNone
}

// agentsMsg lists the subagents of the session behind channel ch with their busy
// state; ok only when the list changed since the last call. A subagent transcript
// is re-read only when its size changed. Busy ones come first.
func (t *tail) agentsMsg(ch, sid string) (ui.AgentsMsg, bool) {
	as, _ := transcript.Subagents(filepath.Dir(t.path), sid) // a corrupt meta drops that agent only
	out := make([]model.Agent, 0, len(as))
	for _, a := range as {
		if fi, err := os.Stat(a.Path); err == nil && fi.Size() != t.sizes[a.Path] {
			t.sizes[a.Path] = fi.Size()
			t.busy[a.Path], _ = transcript.Busy(a.Path) // unknown counts as idle
		}
		out = append(out, model.Agent{Nick: a.Nick, Busy: t.busy[a.Path]})
	}
	slices.SortStableFunc(out, func(x, y model.Agent) int {
		switch {
		case x.Busy == y.Busy:
			return 0
		case x.Busy:
			return -1
		}
		return 1
	})
	key := fmt.Sprint(out)
	if key == t.agents {
		return ui.AgentsMsg{}, false
	}
	t.agents = key
	return ui.AgentsMsg{Ch: ch, Agents: out}, true
}

// teammatePrefix opens a teammate message block; split so display tooling cannot mangle "<".
const teammatePrefix = "<" + "teammate-message "

// teammateRE is one teammate message block as Claude records it (measured 2026-10-22).
var teammateRE = regexp.MustCompile(`(?s)<teammate-message teammate_id="([^"]+)"[^>]*>\n(.*?)\n</teammate-message>`)

// teammateLines turns each teammate message block in text into a line from that
// teammate. idle_notification bodies (JSON) are dropped: busy marks show idle.
func teammateLines(text string, now time.Time) []model.Line {
	var out []model.Line
	for _, m := range teammateRE.FindAllStringSubmatch(text, -1) {
		body := strings.TrimSpace(m[2])
		if strings.HasPrefix(body, "{") && strings.Contains(body, `"idle_notification"`) {
			continue
		}
		out = append(out, model.Line{At: now, Nick: m[1], Text: body, Kind: model.Transcript})
	}
	return out
}
