package ui

import (
	"cmp"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
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

// NickMsg sets the user's nick after the endpoint took a fallback name.
type NickMsg struct{ Nick string }

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
	promptShell        = '$'

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
	quote    *model.Line                          // a clicked line being replied to; sent above the next line, not in the input
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
	pick     bool            // ctrl+o or /: the picker is open
	pickWhat pickKind        // what the picker lists: channels (ctrl+o), lines (/), speakers (S)
	speakers []string        // S: show only these nicks' lines; none = all
	grep     string          // ctrl+g: show only lines fuzzy-matching this; "" off
	marks    []string        // S picker: nicks marked with space, applied by enter
	resumeTo string          // channel to focus once it shows up (the last one of the previous run)
	toasts   []toast         // notification cards, newest last; the tick drops old ones
	helpOff  int             // help box scroll (j k, wheel) when it is taller than the screen
	pq       textinput.Model // picker query (own input: a typed message survives)
	files    []string        // pickFiles: the session's files, from git ls-files (filesMsg)
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
	case NickMsg:
		a.user = msg.Nick
	case ToastMsg:
		a = a.toast(msg.Text, msg.Err)
	case tickMsg:
		now := a.now()
		a.toasts = slices.DeleteFunc(slices.Clone(a.toasts), func(t toast) bool { return now.Sub(t.at) >= toastFor })
		return a, tick()
	case filesMsg:
		a.files = msg
	case LiteralResultMsg:
		if msg.Err != nil {
			a = a.note("literal send failed: " + msg.Err.Error())
		}
	case execDoneMsg:
		if msg.Err != nil {
			a = a.note(msg.What + " failed: " + msg.Err.Error())
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
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft && !a.help && !a.pick && a.prompt == 0 {
			return a.click(tea.Mouse(msg))
		}
	case tea.MouseWheelMsg:
		if a.help { // the wheel scrolls the help box while it is open
			step := wheelRows
			if msg.Button == tea.MouseWheelUp {
				step = -wheelRows
			}
			return a.helpScroll(step), nil
		}
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
		switch k { // scroll keys move the box; any other key closes it
		case "j", "down":
			return a.helpScroll(1), nil
		case "k", "up":
			return a.helpScroll(-1), nil
		case "ctrl+d":
			return a.helpScroll(a.h / 2), nil
		case "ctrl+u":
			return a.helpScroll(-a.h / 2), nil
		}
		a.help, a.helpOff = false, 0
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
			a.quote = nil // esc drops the reply; the typed text stays
			a.in.Blur()
		case act.Kind == None:
			var cmd tea.Cmd
			a.in, cmd = a.in.Update(msg)
			if a.in.Value() == "" { // deleting back to the start ends the reply
				a.quote = nil
			}
			return a, cmd
		case act.Kind == Scroll:
			return a.scroll(act), nil
		case act.Kind == LineUp || act.Kind == LineDown:
			return a.lineStep(act.Kind == LineDown), nil
		case act.Kind == PickChan:
			return a.openPick(pickChans), nil
		case act.Kind == PickFile:
			return a.openFilePick()
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
		case "esc": // NORMAL esc clears the selected line, the input text and the reply
			a.hit, a.quote = -1, nil
			a.in.Reset()
			return a, nil
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
		if act.Kind == ReplySelected { // A: reply to the selected line, else to the last one, which gets selected
			if ls := a.lines(a.m.Current()); len(ls) > 0 {
				if a.hit < 0 || a.hit >= len(ls) {
					a.hit = len(ls) - 1
				}
				if l := ls[a.hit]; l.Nick != "" && l.Nick != a.user {
					a = a.mention(l.Nick)
					a.quote = &l
				}
			}
		}
	}
	switch act.Kind {
	case CmdLine:
		return a.openPrompt(promptCmd), nil
	case ShellLine:
		return a.openPrompt(promptShell), nil
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
	case PickInvite:
		if a.m.Current().Kind != model.RoomChan {
			return a.toast("invite works in a & room: /join &name first", true), nil
		}
		return a.openPick(pickInvite), nil
	case PickSpeaker:
		return a.openPick(pickSpeakers), nil
	case PickChan:
		return a.openPick(pickChans), nil
	case GrepLines:
		return a.openPick(pickGrep), nil
	case PickFile:
		return a.openFilePick()
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
	case "ctrl+t": // the file finder fills the prompt with a path
		return a.openFilePick()
	case "enter":
		kind := a.prompt
		var v string
		a, v = a.closePrompt()
		if kind == promptShell {
			return a.runShell(v)
		}
		return a.sendLiteral(v) // : sends as is
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
			a.m, _ = a.m.Focus(i)                   // i is in range: it comes from Channels
			a.hit, a.speakers, a.grep = -1, nil, "" // the speaker and grep filters belong to the channel they were set in
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
		if a.quote != nil { // the agent sees the line replied to, as markdown quote
			text = "> <" + a.quote.Nick + "> " + a.quote.Text + "\n" + text
			a.quote = nil
		}
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
		return "NORMAL", ": " + withCursor(a.in.Value(), a.in.Position()), keyHint("enter", "send as is", "esc", "cancel")
	case a.prompt == promptShell:
		return "NORMAL", "$ " + withCursor(a.in.Value(), a.in.Position()), keyHint("enter", "run locally", "esc", "cancel")
	case a.mode == Insert:
		prompt := "> "
		if a.quote != nil { // RE: while replying; the quote is dropped once the input is empty
			prompt = "RE: "
		}
		if u := a.cmdUsage(); u != "" { // typing a /command: its usage is the hint (vic 2026-10-23)
			return "INSERT", prompt + withCursor(a.in.Value(), a.in.Position()), u
		}
		hint := keyHint("enter", "send", "/", "command", "@", "mention", "tab", "complete", "↑↓", "line", "C-t", "files", "C-o", "pick", "esc", "normal")
		if a.quote != nil { // the replied-to line is shown here, not in the input
			hint = keyHint("re", "<"+a.quote.Nick+"> "+a.quote.Text) + "   " + hint
		}
		return "INSERT", prompt + withCursor(a.in.Value(), a.in.Position()), hint
	case a.focus == focusChans:
		return "CHANNELS", a.footer(), keyHint("type", "filter "+a.fq, "↑↓", "select", "enter", "join", "tab", "nicks", "esc", "chat")
	case a.focus == focusNicks:
		return "NICKS", a.footer(), keyHint("type", "filter "+a.fq, "↑↓", "select", "enter", "mention", "tab", "chat", "esc", "chat")
	}
	return "NORMAL", a.footer(), keyHint("↑↓", "line", "C-n/C-p", "channel", "tab", "panes", "C-t", "files", "C-o", "pick", "i", "type", "?", "help", "$", "shell")
}

// footer is the last row outside INSERT: more keys and the toggle states, since
// there is no input to show (vic 2026-10-23).
func (a App) footer() string {
	// states first: on a narrow screen the keys fall off the end (they are in ? help)
	return fit(cell, " "+keyHint("t", "tools:"+onOff(a.tools), "m", "mentions:"+onOff(a.mentions), "s", "time:"+onOff(a.stamps),
		"S", "speaker:"+cmp.Or(strings.Join(a.speakers, ","), "all"), "C-g", "grep:"+cmp.Or(a.grep, "off"), "/", "search", "J K", "turns", "A", "reply @", ":", "literal"), max(a.w, 1)) // one row
}

// View is the full-screen frame; screen is its text.
func (a App) View() tea.View {
	v := tea.NewView(a.screen())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion // wheel comes as MouseWheelMsg, so arrows stay keys
	return v
}

// screen is the chat screen, with the help box or the picker drawn over it.
func (a App) screen() string {
	screen := a.chatScreen()
	switch {
	case a.help:
		return overlayCentered(screen, a.w, a.h, helpBox(a.h, a.helpOff))
	case a.pick && a.pickWhat == pickGrep:
		return overlayCentered(screen, a.w, a.h, grepBox(a.w, a.pq.Value()))
	case a.pick:
		what := [...]string{pickChans: "channel / @nick", pickLines: "search", pickSpeakers: "speaker", pickInvite: "invite to " + a.m.Current().Name, pickFiles: "file"}[a.pickWhat]
		return overlayCentered(screen, a.w, a.h, pickBox(a.w, a.h, what, a.pq.Value(), pickList(a.pickSource(), a.pq.Value()), a.psel, a.pickWhat == pickSpeakers, a.shownMarks()))
	}
	return screen
}

// chatScreen is the scrollback, input and modeline, with the completion and toasts floating over them.
func (a App) chatScreen() string {
	label, input, hint := a.inputLine()
	screen := render(a.m, model.Status(a.m, label, a.info, a.now()), input, a.w, a.h, a.off(), a.hit, a.sides(), a.lines(a.m.Current()), a.wrap(), nil, hint, a.grep)
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
	if a.grep != "" {
		if !strings.Contains(strings.ToLower(l.Text), strings.ToLower(a.grep)) { // case-insensitive substring
			return false
		}
	}
	if len(a.speakers) > 0 && !slices.Contains(a.speakers, l.Nick) {
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
	pickChans    pickKind = iota // ctrl+o: channels and @nicks
	pickLines                    // /: the current channel's shown lines; enter jumps
	pickSpeakers                 // S: who spoke here; enter shows only that nick (vic 2026-10-23)
	pickInvite                   // I: who can be invited to this room; enter invites (vic 2026-10-23)
	pickFiles                    // ctrl+t: the session's files; enter inserts the path
	pickGrep                     // ctrl+g: filter the shown lines to fuzzy matches, live
)

// allSpeakers is the speaker picker's first entry: it clears the filter.
const allSpeakers = "(all)"

// shownMarks is the S picker's checked entries: (all) when no nick is marked, else the nicks.
func (a App) shownMarks() []string {
	if len(a.marks) == 0 {
		return []string{allSpeakers}
	}
	return a.marks
}

// openPick opens the fuzzy picker over k.
func (a App) openPick(k pickKind) App {
	a.pick, a.pickWhat, a.psel, a.pq = true, k, 0, textinput.New()
	a.marks = slices.Clone(a.speakers) // S starts with the current filter marked
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
	case pickFiles:
		out = a.files
	case pickInvite: // every session's main agent and its agents as agent@session, minus members
		cur := a.m.Current()
		for _, c := range a.m.Channels() {
			if c.Kind != model.SessionChan {
				continue
			}
			s := strings.TrimPrefix(c.Name, "#")
			for _, n := range append([]string{s}, agentsAt(c, s)...) {
				if !slices.ContainsFunc(cur.Members, func(m model.Member) bool { return m.Nick == n }) {
					out = append(out, n)
				}
			}
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
		if a.pickWhat == pickGrep { // esc clears the grep filter
			a.grep, a.hit, a.offs = "", -1, nil
		}
		a.pick = false
	case "enter":
		if a.psel < len(ms) && a.pickWhat == pickFiles {
			if a.prompt == 0 { // a : or $ prompt keeps its own mode
				a.mode = Insert
			}
			a.in.SetValue(a.in.Value() + ms[a.psel].name + " ")
			a.in.CursorEnd()
			a.in.Focus()
		} else if a.psel < len(ms) && a.pickWhat == pickInvite {
			a.pick = false
			return a.runCmd("/invite " + ms[a.psel].name + " " + a.m.Current().Name)
		}
		if a.pickWhat == pickSpeakers { // an unchecked result alone; a checked one applies the checks
			speakers := slices.Clone(a.marks)
			if a.psel < len(ms) && !slices.Contains(a.shownMarks(), ms[a.psel].name) {
				speakers = []string{ms[a.psel].name}
				if speakers[0] == allSpeakers {
					speakers = nil
				}
			}
			a.speakers, a.hit, a.offs = speakers, -1, nil // positions index the shown lines, which changed
			a.pick = false
			return a, nil
		}
		if a.psel < len(ms) && a.pickWhat == pickLines {
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
	case "space": // S picker: mark or unmark the selected nick; (all) clears the marks. Other pickers type a space.
		if a.pickWhat != pickSpeakers {
			var cmd tea.Cmd
			a.pq, cmd = a.pq.Update(msg)
			a.psel = 0
			a = a.grepSync()
			return a, cmd
		}
		if a.pickWhat == pickSpeakers && a.psel < len(ms) {
			n := ms[a.psel].name
			if n == allSpeakers {
				a.marks = nil
			} else if slices.Contains(a.marks, n) {
				a.marks = slices.DeleteFunc(slices.Clone(a.marks), func(s string) bool { return s == n })
			} else {
				a.marks = append(slices.Clone(a.marks), n)
			}
		}
	case "up", "ctrl+p":
		a.psel = max(a.psel-1, 0)
	case "down", "ctrl+n":
		a.psel = max(min(a.psel+1, len(ms)-1), 0)
	default:
		var cmd tea.Cmd
		a.pq, cmd = a.pq.Update(msg)
		a.psel = 0       // a new query starts at its best match
		a = a.grepSync() // grep filters live as you type
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
			a = a.mention(names[a.fsel])
		}
		a.focus = focusChat
	default:
		if msg.Text != "" {
			a.fq, a.fsel = a.fq+msg.Text, 0
		}
	}
	return a, nil
}

// click handles a left click in wide mode: a channel row switches to that channel,
// a nick row starts an insert line mentioning it (vic 2026-10-23). Row 0 is the pane title.
func (a App) click(m tea.Mouse) (tea.Model, tea.Cmd) {
	if !wide(a.w, a.h) || m.Y < 1 {
		return a, nil
	}
	i := m.Y - 1
	switch {
	case m.X < chanW+2: // the channel pane and its borders
		if _, names := a.paneItems(focusChans); i < len(names) {
			a = a.focusName(names[i])
		}
	case m.X >= a.w-nickW-2: // the nick pane and its borders
		if _, names := a.paneItems(focusNicks); i < len(names) {
			a = a.mention(names[i])
		}
	default: // the lines pane: a file or `command` opens, else a click replies to that line's nick
		l, row, ok := a.lineAt(i)
		if !ok {
			break
		}
		if path, cmd, hit := clickTarget(row, m.X-chanW-3); hit { // the text starts after the border and a space
			return a.openTarget(path, cmd)
		}
		if l.Nick != "" && l.Nick != a.user {
			a = a.mention(l.Nick)
			a.quote = &l
		}
	}
	return a, nil
}

// openTarget opens path in $EDITOR, suspending the app while it runs. cmd goes to
// the : prompt as !cmd, so Enter sends the command to the agent as is.
func (a App) openTarget(path, cmd string) (App, tea.Cmd) {
	if cmd != "" {
		a = a.openPrompt(promptCmd)
		a.in.SetValue("!" + cmd)
		a.in.CursorEnd()
		return a, nil
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, rest)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.m.Current().Cwd, path)
	}
	argv := append(strings.Fields(cmp.Or(strings.TrimSpace(os.Getenv("EDITOR")), "vi")), path)
	return a, tea.ExecProcess(exec.Command(argv[0], argv[1:]...), execDone("editor"))
}

// runShell runs cmd with sh -c, suspending the app. It waits for Enter before the
// app comes back, so the output stays readable (vic 2026-10-23, like jjui $).
func (a App) runShell(cmd string) (App, tea.Cmd) {
	if strings.TrimSpace(cmd) == "" {
		return a, nil
	}
	script := cmd + "\necho; echo 'press enter to return'; read _"
	return a, tea.ExecProcess(exec.Command("sh", "-c", script), execDone("command"))
}

// execDoneMsg reports that a suspended process exited; Err is nil on a clean exit.
type execDoneMsg struct {
	What string
	Err  error
}

// execDone is the callback for tea.ExecProcess that reports the exit as an execDoneMsg.
func execDone(what string) func(error) tea.Msg {
	return func(err error) tea.Msg { return execDoneMsg{What: what, Err: err} }
}

// lineAt is the shown line of lines-pane row i, as render draws it, and the row's plain text.
func (a App) lineAt(i int) (model.Line, string, bool) {
	shown := a.lines(a.m.Current())
	rows, of := tailRows(shown, a.page(), a.off(), textWidth(a.w, a.h), a.wrap())
	if i >= len(rows) {
		return model.Line{}, "", false
	}
	return shown[of[i]], rows[i], true
}

// mention starts an insert line with @nick, after any text already typed.
func (a App) mention(nick string) App {
	a.mode = Insert
	a.in.SetValue(a.in.Value() + "@" + nick + " ")
	a.in.CursorEnd()
	a.in.Focus()
	return a
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
		return s + cursor.Render(" ")
	}
	return string(rs[:pos]) + cursor.Render(string(rs[pos])) + string(rs[pos+1:])
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

// agentsAt is c's agents as agent@session (D:229).
func agentsAt(c model.Channel, session string) []string {
	var out []string
	for _, ag := range c.Agents {
		out = append(out, ag.Nick+"@"+session)
	}
	return out
}

func (a App) helpScroll(n int) App {
	a.helpOff = helpOff(helpRows(), a.h, a.helpOff+n)
	return a
}

// cmdUsage is the usage of the /command being typed in INSERT, as hint text: every
// command that matches the first word, or "" when the line is not a command.
func (a App) cmdUsage() string {
	word, _, _ := strings.Cut(a.in.Value(), " ")
	rest, ok := strings.CutPrefix(word, "/")
	if !ok {
		return ""
	}
	us := model.Usages(rest)
	if len(us) == 0 {
		return alert.Render("unknown command")
	}
	for i, u := range us {
		us[i] = shortcut.Render(u)
	}
	return dimmed.Render("usage ") + strings.Join(us, dimmed.Render("  ·  "))
}

// filesMsg is the session's file list for the file finder.
type filesMsg []string

// openFilePick opens the file finder; the list comes from git in the session's directory.
func (a App) openFilePick() (App, tea.Cmd) {
	cwd := a.m.Current().Cwd
	return a.openPick(pickFiles), func() tea.Msg { return filesMsg(listFiles(cwd)) }
}

// grepSync sets the ctrl+g filter to the picker's text while the grep picker is open.
func (a App) grepSync() App {
	if a.pickWhat == pickGrep {
		a.grep, a.hit, a.offs = strings.TrimSpace(a.pq.Value()), -1, nil
	}
	return a
}
