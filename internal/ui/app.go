package ui

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ircc/internal/model"
)

// SessionsMsg replaces the live session list.
type SessionsMsg []model.Session

// LineMsg appends L to channel Ch; an unknown channel (including "") is reported as a
// note. Old is history replayed at attach: no unread count, no move to the front.
type LineMsg struct {
	Old bool
	Ch  string
	L   model.Line
}

// NoteMsg adds L to the focused channel (producer notes with no channel of their own).
type NoteMsg struct{ L model.Line }

// RoomsMsg sets the visible room (&) channels to exactly these names.
type RoomsMsg []string

// LiteralResultMsg reports a finished : send; Err is nil on success.
type LiteralResultMsg struct{ Err error }

// ToastMsg shows Text as a notification card; Err makes it red.
type ToastMsg struct {
	Text string
	Err  bool
}

// FocusMsg makes channel Ch current (after /join).
type FocusMsg string

// AgentsMsg sets the subagents of session channel Ch (nicks and busy state).
type AgentsMsg struct {
	Ch     string
	Agents []model.Agent
}

// MembersMsg sets the member nicks of room Ch.
type MembersMsg struct {
	Ch    string
	Nicks []string
}

// InfoMsg replaces the per-pid status the modeline shows.
type InfoMsg map[int]model.SessionInfo

// SendResultMsg reports a finished send of Text on Ch; Err is nil on success.
type SendResultMsg struct {
	Ch, Text string
	Err      error
}

// CmdMsg carries a parsed command (other than /quit) to the program owner,
// which performs the side effect (join, kick, ...).
type CmdMsg struct{ Cmd model.Cmd }

const (
	defaultW, defaultH = 80, 24
	promptCmd          = ':'

	wheelRows = 3 // rows one wheel notch scrolls
)

// App is the bubbletea model: keys.go decides, App applies. No sockets here;
// sending goes through the injected send func.
type App struct {
	m        model.Model
	mode     EditMode
	pending  string
	in       textinput.Model
	send     func(ch, text string) error
	cmdf     func(ch string, c model.Cmd) tea.Cmd // runs a parsed command; ch is the focused channel
	literal  func(ch, text string) error          // the : prompt: text as is to the session socket
	user     string                               // nick of the local echo line
	info     map[int]model.SessionInfo
	now      func() time.Time
	w, h     int
	prompt   rune // 0 or promptCmd (:): the input line holds a prompt
	query    string
	hit      int             // index in the current channel of the last search hit, -1 none
	offs     map[string]int  // per channel: lines scrolled back from the newest
	help     bool            // the ? popup is shown; the next key closes it
	focus    paneFocus       // tab: which pane takes keys (chat, channels, nicks)
	fq       string          // query typed in a focused side pane (fuzzy filter)
	fsel     int             // selected row in the focused side pane
	stamps   bool            // s: show "15:04 " before each line
	pick     bool            // ctrl+t or /: the picker is open
	pickWhat pickKind        // what the picker lists: channels (ctrl+t), lines (/), speakers (S)
	speaker  string          // S: show only this nick's lines; "" all
	resumeTo string          // channel to focus once it shows up (the last one of the previous run)
	toasts   []toast         // notification cards, newest last; the tick drops old ones
	pq       textinput.Model // picker query (own input: a typed message survives)
	psel     int             // picker selection, an index in pickList
	comp     completion      // tab completion state of the INSERT line
	tools    bool            // t: show tool lines
	mentions bool            // m: show only direct lines (see shows)
}

// New returns an App over m that sends channel text through send.
func New(m model.Model, send func(ch, text string) error) App {
	in := textinput.New()
	in.Prompt = ""
	return App{m: m, in: in, send: send, user: "me", now: time.Now, w: defaultW, h: defaultH, hit: -1}
}

// Init starts the 1 s tick: it drops old toasts and keeps the clock current.
func (a App) Init() tea.Cmd { return tick() }

func tick() tea.Cmd { return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} }) }

type tickMsg struct{}

// toastFor is how long a toast shows (jjui default: 4 s).
const toastFor = 4 * time.Second

type toast struct {
	text string
	err  bool
	at   time.Time
}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case SessionsMsg:
		a.m = a.m.WithSessions(msg)
		a = a.resume()
	case LineMsg:
		if msg.Old {
			if m, err := a.m.AddHistory(msg.Ch, msg.L); err == nil {
				a.m = m
			}
		} else if next, err := a.addLine(msg.Ch, msg.L); err == nil {
			a = next
		} else {
			a = a.note("line for unknown channel " + msg.Ch)
		}
	case RoomsMsg:
		a.m = a.m.WithRooms(msg)
		a = a.resume()
	case CmdMsg:
		if a.cmdf == nil {
			return a.note("no command handler installed"), nil
		}
		return a, a.cmdf(a.m.Current().Name, msg.Cmd)
	case NoteMsg:
		a = a.toast(msg.L.Text, false) // runtime notes are notifications, not scrollback
	case ToastMsg:
		a = a.toast(msg.Text, msg.Err)
	case tickMsg:
		now := a.now()
		a.toasts = slices.DeleteFunc(slices.Clone(a.toasts), func(t toast) bool { return now.Sub(t.at) >= toastFor })
		return a, tick()
	case LiteralResultMsg:
		if msg.Err != nil {
			a = a.note("literal send failed: " + msg.Err.Error())
		}
	case SendResultMsg:
		a = a.sent(msg)
	case FocusMsg:
		a = a.focusName(string(msg))
	case AgentsMsg:
		if m, err := a.m.WithAgents(msg.Ch, msg.Agents); err == nil {
			a.m = m
		}
	case MembersMsg:
		ms := make([]model.Member, len(msg.Nicks))
		for i, n := range msg.Nicks {
			ms[i].Nick = n
		}
		if m, err := a.m.WithRoomMembers(msg.Ch, ms); err == nil {
			a.m = m
		}
	case InfoMsg:
		a.info = maps.Clone(msg)
	case tea.WindowSizeMsg:
		a.w, a.h = msg.Width, msg.Height
	case tea.PasteMsg: // pasted text is never keys; it goes to an open input only
		if a.mode == Insert || a.prompt != 0 {
			var cmd tea.Cmd
			a.in, cmd = a.in.Update(msg)
			return a, cmd
		}
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			a = a.scroll(Action{Scroll, -wheelRows})
		case tea.MouseWheelDown:
			a = a.scroll(Action{Scroll, wheelRows})
		}
	case tea.KeyPressMsg: // v2 decodes one key per event, also when one read holds "gt"
		return a.key(msg)
	}
	return a, nil
}

func (a App) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" {
		return a, tea.Quit
	}
	if a.focus != focusChat && a.mode == Normal && a.prompt == 0 && !a.pick && !a.help {
		return a.paneKey(msg)
	}
	if a.pick {
		return a.pickKey(msg)
	}
	if a.help {
		a.help = false
		return a, nil
	}
	if a.prompt != 0 {
		return a.promptKey(msg)
	}
	if a.mode == Insert {
		act, mode, _ := Key(Insert, "", k)
		switch {
		case act.Kind == Send:
			return a.sendLine()
		case act.Kind == None && mode == Normal:
			a.mode = Normal
			a.in.Blur()
		case act.Kind == None:
			var cmd tea.Cmd
			a.in, cmd = a.in.Update(msg)
			return a, cmd
		case act.Kind == Scroll:
			return a.scroll(act), nil
		case act.Kind == LineUp || act.Kind == LineDown:
			return a.lineStep(act.Kind == LineDown), nil
		case act.Kind == PickChan:
			return a.openPick(pickChans), nil
		case act.Kind == NextChan || act.Kind == PrevChan:
			return a.switchChan(act), nil
		}
		if act.Kind == Complete {
			v, comp := a.comp.step(a.in.Value(), a.chanNames(), a.nickPool())
			a.in.SetValue(v)
			a.in.CursorEnd()
			a.comp = comp
		}
		return a, nil
	}
	if a.pending == "" {
		switch k {
		case "/":
			return a.openPick(pickLines), nil
		case "n", "N":
			return a.searchStep(k == "N"), nil
		}
	}
	act, mode, pending := Key(Normal, a.pending, k)
	a.pending = pending
	if mode == Insert {
		a.mode = Insert
		a.in.Focus()
		if act.Kind == ReplyMention {
			if n := a.lastMention(); n != "" {
				a.in.SetValue("@" + n + " ")
				a.in.CursorEnd()
			}
		}
	}
	switch act.Kind {
	case CmdLine:
		return a.openPrompt(promptCmd), nil
	case Scroll, HalfPage, Top, Bottom:
		a = a.scroll(act)
	case ToggleTools, ToggleMentions:
		a.tools = a.tools != (act.Kind == ToggleTools)
		a.mentions = a.mentions != (act.Kind == ToggleMentions)
		a.hit, a.offs = -1, nil // positions index the shown lines, which changed
	case Help:
		a.help = true
	case ToggleStamps:
		a.stamps, a.offs = !a.stamps, nil // row counts changed
	case Yank:
		return a.yank()
	case PickSpeaker:
		return a.openPick(pickSpeakers), nil
	case PickChan:
		return a.openPick(pickChans), nil
	case LineUp, LineDown:
		a = a.lineStep(act.Kind == LineDown)
	case NextTurn, PrevTurn:
		a = a.turn(act.Kind == NextTurn)
	case FocusPane:
		a.focus, a.fq, a.fsel = focusChans, "", max(slices.Index(a.chanNames(), a.m.Current().Name), 0) // start on the current channel
	case NextChan, PrevChan, GotoChan:
		a = a.switchChan(act)
	}
	return a, nil
}

func (a App) openPrompt(p rune) App {
	a.prompt = p
	a.in.Reset()
	a.in.Focus()
	return a
}

func (a App) closePrompt() (App, string) {
	v := a.in.Value()
	a.prompt = 0
	a.in.Reset()
	a.in.Blur()
	return a, v
}

func (a App) promptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		a, _ = a.closePrompt()
	case "enter":
		var v string
		a, v = a.closePrompt()
		return a.sendLiteral(v) // the only prompt is :
	default:
		var cmd tea.Cmd
		a.in, cmd = a.in.Update(msg)
		return a, cmd
	}
	return a, nil
}

// searchStep moves hit to the next (or previous) match of the last query.
func (a App) searchStep(back bool) App {
	c := a.m.Current()
	if i, ok := Search(a.lines(c), a.query, a.hit, back); ok {
		a.hit = i
	}
	return a
}

func (a App) switchChan(act Action) App {
	cs := a.m.Channels()
	cur := -1
	for i, c := range cs {
		if c.Name == a.m.Current().Name {
			cur = i
		}
	}
	to := act.N - 1 // GotoChan is 1-based
	switch act.Kind {
	case NextChan:
		to = cur + 1
	case PrevChan:
		to = cur - 1
	}
	if len(cs) > 0 && act.Kind != GotoChan {
		to = (to + len(cs)) % len(cs)
	}
	if to < 0 || to >= len(cs) {
		return a
	}
	return a.focusName(cs[to].Name)
}

// focusName makes channel name current.
func (a App) focusName(name string) App {
	from := a.m.Current().Name
	for i, c := range a.m.Channels() {
		if c.Name == name && name != from {
			a.m, _ = a.m.Focus(i)     // i is in range: it comes from Channels
			a.hit, a.speaker = -1, "" // the speaker filter belongs to the channel it was set in
		}
	}
	return a
}

func (a App) sendLine() (tea.Model, tea.Cmd) {
	text := a.in.Value()
	a.in.Reset()
	ch := a.m.Current().Name
	switch {
	case text == "" || ch == "":
	case strings.HasPrefix(text, "/"):
		return a.runCmd(text)
	default:
		send := a.send // blocking I/O runs in the Cmd, never in Update
		return a, func() tea.Msg { return SendResultMsg{Ch: ch, Text: text, Err: send(ch, text)} }
	}
	return a, nil
}

func (a App) runCmd(line string) (tea.Model, tea.Cmd) {
	c, err := model.ParseCmd(line)
	if err != nil {
		return a.note(err.Error()), nil
	}
	if c.Name() == model.CmdHelp { // the same screen as ? (vic 2026-10-23)
		a.help = true
		return a, nil
	}
	if c.Name() == model.CmdQuit {
		return a, tea.Quit
	}
	return a, func() tea.Msg { return CmdMsg{c} }
}

// note adds a dim local line to the current channel.
func (a App) note(text string) App { return a.toast(text, true) }

// toast shows text in a card at the bottom right for toastFor (vic 2026-10-23,
// like jjui flash messages); err cards are red, others green.
func (a App) toast(text string, err bool) App {
	a.toasts = append(slices.Clone(a.toasts), toast{text: text, err: err, at: a.now()})
	return a
}

// inputLine is the modeline label, the input row text and the key hints the
// modeline shows for the current mode.
func (a App) inputLine() (label, input, hint string) {
	switch {
	case a.prompt == promptCmd:
		return "NORMAL", ":" + withCursor(a.in.Value(), a.in.Position()), keyHint("enter", "send as is", "esc", "cancel")
	case a.mode == Insert:
		return "INSERT", "> " + withCursor(a.in.Value(), a.in.Position()), keyHint("enter", "send", "/", "command", "@", "mention", "tab", "complete", "↑↓", "line", "C-t", "pick", "esc", "normal")
	case a.focus == focusChans:
		return "CHANNELS", a.footer(), keyHint("type", "filter "+a.fq, "↑↓", "select", "enter", "join", "tab", "nicks", "esc", "chat")
	case a.focus == focusNicks:
		return "NICKS", a.footer(), keyHint("type", "filter "+a.fq, "↑↓", "select", "enter", "mention", "tab", "chat", "esc", "chat")
	}
	return "NORMAL", a.footer(), keyHint("↑↓", "line", "C-n/C-p", "channel", "tab", "panes", "C-t", "pick", "i", "type", "?", "help")
}

// footer is the last row outside INSERT: more keys and the toggle states, since
// there is no input to show (vic 2026-10-23).
func (a App) footer() string {
	// states first: on a narrow screen the keys fall off the end (they are in ? help)
	return fit(cell, " "+keyHint("t", "tools:"+onOff(a.tools), "m", "mentions:"+onOff(a.mentions), "s", "time:"+onOff(a.stamps),
		"S", "speaker:"+cmp.Or(a.speaker, "all"), "/", "search", "J K", "turns", "O", "reply @", ":", "literal"), max(a.w, 1)) // one row
}

// View is the full-screen frame; screen is its text.
func (a App) View() tea.View {
	v := tea.NewView(a.screen())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion // wheel comes as MouseWheelMsg, so arrows stay keys
	return v
}

func (a App) screen() string {
	if a.help {
		return helpView(a.w, a.h)
	}
	if a.pick {
		what := [...]string{pickChans: "channel / @nick", pickLines: "search", pickSpeakers: "speaker"}[a.pickWhat]
		return pickView(a.w, a.h, what, a.pq.Value(), pickList(a.pickSource(), a.pq.Value()), a.psel)
	}
	label, input, hint := a.inputLine()
	screen := render(a.m, model.Status(a.m, label, a.info, a.now()), input, a.w, a.h, a.off(), a.hit, a.sides(), a.lines(a.m.Current()), a.wrap(), nil, hint)
	modeline := a.h - len(inputRows(input, a.w, a.h)) - 1
	screen = overlaySuggest(screen, a.w, modeline-1, a.suggest()) // floats above the prompt: the layout does not move
	return overlayToasts(screen, a.w, modeline-1, a.toasts)       // cards sit just above the modeline
}

// page is the number of scrollback rows View shows; a long input takes rows from it.
func (a App) page() int {
	_, input, _ := a.inputLine()
	return paneRows(a.w, a.h, len(inputRows(input, a.w, a.h)))
}

// off is the current channel's scroll offset.
func (a App) off() int { return a.offs[a.m.Current().Name] }

// setOff stores ch's offset clamped to [0, lines-page], copy-on-write.
func (a App) setOff(ch string, off int) App {
	n := 0
	for _, c := range a.m.Channels() {
		if c.Name == ch {
			n = rowCount(a.lines(c), textWidth(a.w, a.h), a.wrap())
		}
	}
	offs := make(map[string]int, len(a.offs)+1)
	for k, v := range a.offs {
		offs[k] = v
	}
	offs[ch] = min(max(off, 0), max(n-a.page(), 0))
	a.offs = offs
	return a
}

// scroll applies a keys.go action to the current channel; Scroll/HalfPage N>0
// means down (toward the newest), so the offset shrinks.
func (a App) scroll(act Action) App {
	ch := a.m.Current().Name
	switch act.Kind {
	case Scroll:
		return a.setOff(ch, a.off()-act.N)
	case HalfPage:
		return a.setOff(ch, a.off()-act.N*max(a.page()/2, 1))
	case Top:
		cur := a.m.Current()
		return a.setOff(ch, rowCount(a.lines(cur), textWidth(a.w, a.h), a.wrap()))
	}
	return a.setOff(ch, 0)
}

// sent applies a finished send: a failure becomes a note, success echoes the line.
func (a App) sent(r SendResultMsg) App {
	if r.Err != nil {
		return a.note("send failed: " + r.Err.Error())
	}
	if strings.HasPrefix(r.Ch, "&") {
		return a // room lines are echoed by the room runtime, which also logs them
	}
	if m, err := a.m.AddLine(r.Ch, model.Line{At: a.now(), Nick: a.user, Text: r.Text}); err == nil {
		a.m = m
	}
	return a
}

// WithUser sets the nick shown on the local echo of sent lines (default "me").
func (a App) WithUser(nick string) App {
	a.user = nick
	return a
}

// addLine appends l to channel ch; a scrolled-back channel keeps its view anchored.
func (a App) addLine(ch string, l model.Line) (App, error) {
	m, err := a.m.AddLine(ch, l)
	if err != nil {
		return a, err
	}
	a.m = m
	if a.offs[ch] > 0 && a.shows(chanKind(ch), l) {
		a = a.setOff(ch, a.offs[ch]+len(a.wrap()(l, textWidth(a.w, a.h))))
	}
	return a, nil
}

// WithCmd sets the handler for parsed commands (other than /quit). It runs on the
// bubbletea loop, so it must return fast; slow work belongs in the returned tea.Cmd.
// WithLiteral sets the sender of : lines (text as is, no wrapper).
func (a App) WithLiteral(f func(ch, text string) error) App {
	a.literal = f
	return a
}

func (a App) WithCmd(f func(ch string, c model.Cmd) tea.Cmd) App {
	a.cmdf = f
	return a
}

// shows reports whether l, in a channel of kind k, passes the t and m toggles.
// m (mentions) keeps direct lines only: in a # channel the Chat lines (replies to
// you, your lines), in a & room only lines that @mention you or are yours.
func (a App) shows(k model.ChanKind, l model.Line) bool {
	if a.speaker != "" && l.Nick != a.speaker {
		return false
	}
	if !a.tools && l.Kind == model.Tool {
		return false
	}
	if !a.mentions {
		return true
	}
	if k == model.RoomChan {
		return l.Nick == a.user || mentionsUser(l.Text, a.user)
	}
	return l.Kind == model.Chat
}

// mentionsUser reports whether text has @user as a whole mention.
func mentionsUser(text, user string) bool {
	for _, m := range mentionRE.FindAllString(text, -1) {
		if m[1:] == user {
			return true
		}
	}
	return false
}

// lines is the part of c's scrollback the toggles show; render, scroll and search all use it.
func (a App) lines(c model.Channel) []model.Line {
	var out []model.Line
	for _, l := range c.Lines.All() {
		if a.shows(c.Kind, l) {
			out = append(out, l)
		}
	}
	return out
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// sendLiteral sends a : line as is. The session logs it, so the transcript shows it;
// there is no local echo. Failures become a note (SendResultMsg).
func (a App) sendLiteral(text string) (tea.Model, tea.Cmd) {
	ch := a.m.Current().Name
	if strings.TrimSpace(text) == "" || ch == "" {
		return a, nil
	}
	if a.literal == nil {
		return a.note("no literal sender installed"), nil
	}
	lit := a.literal // blocking I/O runs in the Cmd, never in Update
	return a, func() tea.Msg { return LiteralResultMsg{Err: lit(ch, text)} }
}

// wrap is the row wrapper the s toggle selects.
func (a App) wrap() wrapFunc {
	if a.stamps {
		return wrapStamped
	}
	return wrapLine
}

func (a App) chanNames() []string {
	var out []string
	for _, c := range a.m.Channels() {
		out = append(out, c.Name)
	}
	return out
}

// pickKind is what the fuzzy picker lists.
type pickKind uint8

const (
	pickChans    pickKind = iota // ctrl+t: channels and @nicks
	pickLines                    // /: the current channel's shown lines; enter jumps
	pickSpeakers                 // S: who spoke here; enter shows only that nick (vic 2026-10-23)
)

// allSpeakers is the speaker picker's first entry: it clears the filter.
const allSpeakers = "(all)"

// openPick opens the fuzzy picker over k.
func (a App) openPick(k pickKind) App {
	a.pick, a.pickWhat, a.psel, a.pq = true, k, 0, textinput.New()
	a.pq.Prompt = ""
	a.pq.Focus()
	return a
}

// pickSource is what the picker searches: channels and @nicks, "<nick> text" per
// shown line, or the distinct speakers of the current channel, newest first.
func (a App) pickSource() []string {
	var out []string
	switch a.pickWhat {
	case pickChans:
		out = a.chanNames()
		cur := a.m.Current()
		nicks := chanNicks(cur, a.user)[1:] // this channel: plain nicks
		for _, c := range a.m.Channels() {  // other sessions: agents only as agent@session, never plain
			if c.Kind == model.SessionChan && c.Name != cur.Name {
				for _, ag := range c.Agents {
					nicks = append(nicks, ag.Nick+"@"+strings.TrimPrefix(c.Name, "#"))
				}
			}
		}
		for _, n := range nicks {
			if !slices.Contains(out, "@"+n) {
				out = append(out, "@"+n)
			}
		}
	case pickLines:
		for _, l := range a.lines(a.m.Current()) {
			out = append(out, "<"+l.Nick+"> "+l.Text)
		}
	case pickSpeakers:
		out = []string{allSpeakers}
		cur := a.m.Current()
		ls := cur.Lines.All()
		for i := len(ls) - 1; i >= 0; i-- {
			if !slices.Contains(out, ls[i].Nick) {
				out = append(out, ls[i].Nick)
			}
		}
		for _, n := range chanNicks(cur, a.user) { // then everyone in the pane who has not spoken yet
			if !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	return out
}

// pickKey runs one key in the open picker: enter goes to the selected channel or line.
func (a App) pickKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	ms := pickList(a.pickSource(), a.pq.Value())
	switch msg.String() {
	case "esc":
		a.pick = false
	case "enter":
		if a.psel < len(ms) && a.pickWhat == pickSpeakers {
			a.speaker, a.hit, a.offs = ms[a.psel].name, -1, nil // positions index the shown lines, which changed
			if a.speaker == allSpeakers {
				a.speaker = ""
			}
		} else if a.psel < len(ms) && a.pickWhat == pickLines {
			a.hit, a.query = ms[a.psel].idx, a.pq.Value() // n / N go on from here
			a = a.showHit()
		} else if a.psel < len(ms) {
			if nick, ok := strings.CutPrefix(ms[a.psel].name, "@"); ok {
				a.mode = Insert
				a.in.SetValue(a.in.Value() + "@" + nick + " ")
				a.in.CursorEnd()
				a.in.Focus()
			} else {
				a = a.focusName(ms[a.psel].name)
			}
		}
		a.pick = false
	case "up", "ctrl+p":
		a.psel = max(a.psel-1, 0)
	case "down", "ctrl+n":
		a.psel = max(min(a.psel+1, len(ms)-1), 0)
	default:
		var cmd tea.Cmd
		a.pq, cmd = a.pq.Update(msg)
		a.psel = 0 // a new query starts at its best match
		return a, cmd
	}
	return a, nil
}

// showHit scrolls so the first row of the hit line is the top row of the lines pane.
func (a App) showHit() App {
	ls := a.lines(a.m.Current())
	if a.hit < 0 || a.hit >= len(ls) {
		return a
	}
	after := rowCount(ls[a.hit:], textWidth(a.w, a.h), a.wrap()) // rows from the hit line to the newest
	return a.setOff(a.m.Current().Name, max(after-a.page(), 0))
}

// nickPool is every nick tab can complete: the current channel's agents and members,
// then every session main agent and its agents as agent@session.
func (a App) nickPool() []string {
	cur := a.m.Current()
	var out []string // the current channel's nicks first: tab offers them first
	for _, ag := range cur.Agents {
		out = append(out, ag.Nick)
	}
	for _, m := range cur.Members {
		out = append(out, m.Nick)
	}
	for _, c := range a.m.Channels() {
		if c.Kind == model.SessionChan {
			s := strings.TrimPrefix(c.Name, "#")
			out = append(out, s)
			for _, ag := range c.Agents { // teammate outside its channel: agent@session (D:229)
				out = append(out, ag.Nick+"@"+s)
			}
		}
	}
	return out
}

// suggest is the completion row above an INSERT line (0 or 1 row): what tab puts in.
func (a App) suggest() []string {
	if a.mode != Insert || a.prompt != 0 {
		return nil
	}
	cs := candidates(a.in.Value(), a.chanNames(), a.nickPool())
	if len(cs) == 0 {
		return nil
	}
	_, word := lastWord(a.in.Value())
	var parts []string
	for i, c := range cs {
		st := dimmed
		if i == 0 {
			st = cell.Bold(true)
		}
		parts = append(parts, shortcut.Bold(true).Render(c[:len(word)])+st.Render(c[len(word):]))
	}
	return []string{ansi.Truncate(dimmed.Render("tab ")+strings.Join(parts, "  "), max(a.w-6, 1), "…")} // fits in the box
}

// chanKind is the kind a channel name has: & rooms, else # sessions.
func chanKind(name string) model.ChanKind {
	if strings.HasPrefix(name, "&") {
		return model.RoomChan
	}
	return model.SessionChan
}

// lastMention is the newest @nick in the current channel's shown lines that is
// not the user ("" if none); O starts a reply to it.
func (a App) lastMention() string {
	ls := a.lines(a.m.Current())
	for i := len(ls) - 1; i >= 0; i-- {
		ms := mentionRE.FindAllString(ls[i].Text, -1)
		for j := len(ms) - 1; j >= 0; j-- {
			if n := ms[j][1:]; n != a.user {
				return n
			}
		}
	}
	return ""
}

// Status marks before each nick in the nick pane (vic 2026-10-23). All are wide
// (2 cells) emoji with no variation selector, so widths stay exact.
const (
	busyMark   = "⌛" // working: registry busy, or a subagent mid-turn
	typingMark = "💬" // you, while your INSERT line has text
	idleMark   = "💤" // idle, or not live
)

// statusMarks lists every mark, for the view to split it from the nick.
var statusMarks = []string{busyMark, typingMark, idleMark}

// paneNicks is chanNicks with a status mark before each nick.
func (a App) paneNicks() []string {
	cur := a.m.Current()
	out := chanNicks(cur, a.user)
	busy := func(pid int) bool { return a.info[pid].Status == "busy" }
	mark := func(b bool) string {
		if b {
			return busyMark
		}
		return idleMark
	}
	out[0] = mark(false) + out[0]
	if a.mode == Insert && a.in.Value() != "" {
		out[0] = typingMark + chanNicks(cur, a.user)[0]
	}
	for i := 1; i < len(out); i++ {
		switch {
		case cur.Kind == model.RoomChan:
			out[i] = mark(busy(a.pidOf(cur.Members[i-1].Nick))) + out[i]
		case i == 1: // the main agent
			out[i] = mark(busy(cur.PID)) + out[i]
		default:
			out[i] = mark(cur.Agents[i-2].Busy) + out[i]
		}
	}
	return out
}

// pidOf is the session pid behind a member nick: a main agent nick is its # channel
// name without "#", a teammate "x@s" belongs to session s (D:229). 0 if none.
func (a App) pidOf(nick string) int {
	if _, s, ok := strings.Cut(nick, "@"); ok {
		nick = s
	}
	for _, c := range a.m.Channels() {
		if c.Kind == model.SessionChan && c.Name == "#"+nick {
			return c.PID
		}
	}
	return 0
}

// turn moves the cursor (the reversed line) to the next (fwd) or previous start of a
// speaker run: the first line whose nick differs from the line before it. With no
// cursor, fwd starts from the oldest line and back from the newest.
func (a App) turn(fwd bool) App {
	ls := a.lines(a.m.Current())
	start := func(i int) bool { return i == 0 || ls[i].Nick != ls[i-1].Nick }
	i, step := a.hit, 1
	if !fwd {
		step = -1
	}
	if i < 0 || i >= len(ls) {
		i = len(ls)
		if fwd {
			i = -1
		}
	}
	for j := i + step; j >= 0 && j < len(ls); j += step {
		if start(j) {
			a.hit = j
			return a.showHit()
		}
	}
	return a
}

// paneFocus is the pane that takes NORMAL keys; tab cycles it (vic 2026-10-22).
type paneFocus uint8

const (
	focusChat paneFocus = iota
	focusChans
	focusNicks
)

// paneItems is the focused side pane's rows: names (channels, or nicks with busy
// marks) filtered fuzzily by fq, and the plain names to act on. Unfocused or
// with no query, every row in pane order.
func (a App) paneItems(f paneFocus) (show, names []string) {
	if f == focusChans {
		names = a.chanNames()
		show = names
	} else {
		names = chanNicks(a.m.Current(), a.user)
		show = a.paneNicks()
	}
	if f != a.focus || strings.TrimSpace(a.fq) == "" {
		return show, names
	}
	var fs, fn []string
	for _, m := range pickList(names, a.fq) {
		fs, fn = append(fs, show[m.idx]), append(fn, names[m.idx])
	}
	return fs, fn
}

// paneKey is paneStep plus the channel preview: moving over the channel pane shows
// that channel in the chat and nick panes (vic 2026-10-23).
func (a App) paneKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	next, cmd := a.paneStep(msg)
	b := next.(App)
	if k := msg.String(); b.focus == focusChans && k != "esc" && k != "enter" && k != "tab" {
		if _, names := b.paneItems(focusChans); b.fsel < len(names) {
			b = b.focusName(names[b.fsel])
		}
	}
	return b, cmd
}

// paneStep runs one NORMAL key while a side pane has focus: type to filter, up/down
// to select, enter to join a channel or start a line mentioning a nick, tab to the
// next pane, esc back to chat.
func (a App) paneStep(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	_, names := a.paneItems(a.focus)
	switch k := msg.String(); k {
	case "esc":
		a.focus = focusChat
	case "tab":
		a.focus, a.fq, a.fsel = (a.focus+1)%3, "", 0
	case "up", "ctrl+p":
		a.fsel = max(a.fsel-1, 0)
	case "down", "ctrl+n":
		a.fsel = max(min(a.fsel+1, len(names)-1), 0)
	case "backspace":
		if r := []rune(a.fq); len(r) > 0 {
			a.fq, a.fsel = string(r[:len(r)-1]), 0
		}
	case "enter":
		if a.fsel >= len(names) {
			return a, nil
		}
		if a.focus == focusChans {
			a = a.focusName(names[a.fsel])
		} else {
			a.mode = Insert
			a.in.SetValue(a.in.Value() + "@" + names[a.fsel] + " ")
			a.in.CursorEnd()
			a.in.Focus()
		}
		a.focus = focusChat
	default:
		if msg.Text != "" {
			a.fq, a.fsel = a.fq+msg.Text, 0
		}
	}
	return a, nil
}

func (a App) sides() sidePanes {
	chans, _ := a.paneItems(focusChans)
	nicks, _ := a.paneItems(focusNicks)
	return sidePanes{chans: chans, nicks: nicks, focus: a.focus, sel: a.fsel, q: a.fq}
}

// lineStep moves the cursor (the reversed line) one line down (to newer) or up,
// and scrolls just enough to keep it in view (vic 2026-10-23). Up with no cursor
// starts at the newest line; down past the newest clears it.
func (a App) lineStep(down bool) App {
	ls := a.lines(a.m.Current())
	switch {
	case len(ls) == 0:
		return a
	case a.hit < 0 || a.hit >= len(ls):
		if down {
			return a
		}
		a.hit = len(ls) - 1
	case down && a.hit == len(ls)-1:
		a.hit = -1
		return a.setOff(a.m.Current().Name, 0)
	case down:
		a.hit++
	default:
		a.hit = max(a.hit-1, 0)
	}
	w := textWidth(a.w, a.h)
	after := rowCount(ls[a.hit:], w, a.wrap())   // rows from the cursor line's top to the newest
	below := after - len(a.wrap()(ls[a.hit], w)) // rows under the cursor line
	off := min(a.off(), below)                   // its bottom row is on screen
	off = max(off, after-a.page())               // and its top row
	return a.setOff(a.m.Current().Name, off)
}

// withCursor draws the input cursor at rune position pos: that rune reversed, or a
// reversed space at the end (vic 2026-10-23).
func withCursor(s string, pos int) string {
	rs := []rune(s)
	pos = min(max(pos, 0), len(rs))
	if pos == len(rs) {
		return s + current.Render(" ")
	}
	return string(rs[:pos]) + current.Render(string(rs[pos])) + string(rs[pos+1:])
}

// WithResume makes ch current as soon as it shows up in the channel list, once:
// ircc starts on the channel it was on when it last quit (vic 2026-10-23).
func (a App) WithResume(ch string) App {
	a.resumeTo = ch
	return a
}

func (a App) resume() App {
	if a.resumeTo == "" || !slices.Contains(a.chanNames(), a.resumeTo) {
		return a
	}
	a, a.resumeTo = a.focusName(a.resumeTo), ""
	return a
}

// Channel is the current channel's name ("" when there is none).
func (a App) Channel() string { return a.m.Current().Name }

// yank copies the cursor line's text to the system clipboard (OSC 52) (vic 2026-10-23).
func (a App) yank() (tea.Model, tea.Cmd) {
	ls := a.lines(a.m.Current())
	if a.hit < 0 || a.hit >= len(ls) {
		a = a.toast("nothing to yank: ↑ selects a line", true)
		return a, nil
	}
	text := ls[a.hit].Text
	a = a.toast("yanked: "+strings.ReplaceAll(text, "\n", " "), false)
	return a, tea.SetClipboard(text)
}
