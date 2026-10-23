package ui

import (
	"maps"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"ircc/internal/model"
)

// SessionsMsg replaces the live session list.
type SessionsMsg []model.Session

// LineMsg appends L to channel Ch; an unknown channel (including "") is reported as a note.
type LineMsg struct {
	Ch string
	L  model.Line
}

// NoteMsg adds L to the focused channel (producer notes with no channel of their own).
type NoteMsg struct{ L model.Line }

// RoomsMsg sets the visible room (&) channels to exactly these names.
type RoomsMsg []string

// LiteralResultMsg reports a finished : send; Err is nil on success.
type LiteralResultMsg struct{ Err error }

// FocusMsg makes channel Ch current (after /join).
type FocusMsg string

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
	promptSearch       = '/'
	wheelRows          = 3 // rows one wheel notch scrolls
)

// App is the bubbletea model: keys.go decides, App applies. No sockets here;
// sending goes through the injected send func.
type App struct {
	m       model.Model
	mode    EditMode
	pending string
	in      textinput.Model
	send    func(ch, text string) error
	cmdf    func(ch string, c model.Cmd) tea.Cmd // runs a parsed command; ch is the focused channel
	literal func(ch, text string) error          // the : prompt: text as is to the session socket
	user    string                               // nick of the local echo line
	info    map[int]model.SessionInfo
	now     func() time.Time
	w, h    int
	prompt  rune // 0, promptCmd or promptSearch: the input line holds a prompt
	query   string
	hit     int            // index in the current channel of the last search hit, -1 none
	offs    map[string]int // per channel: lines scrolled back from the newest
	help    bool           // the ? popup is shown; the next key closes it
	prev    string         // channel focused before the current one; tab returns to it
	tools   bool           // t: show tool lines
	chat    bool           // c: show only Chat lines (replies, rooms, own lines)
}

// New returns an App over m that sends channel text through send.
func New(m model.Model, send func(ch, text string) error) App {
	in := textinput.New()
	in.Prompt = ""
	return App{m: m, in: in, send: send, user: "me", now: time.Now, w: defaultW, h: defaultH, hit: -1}
}

func (a App) Init() tea.Cmd { return nil }

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case SessionsMsg:
		a.m = a.m.WithSessions(msg)
	case LineMsg:
		if next, err := a.addLine(msg.Ch, msg.L); err == nil {
			a = next
		} else {
			a = a.note("line for unknown channel " + msg.Ch)
		}
	case RoomsMsg:
		a.m = a.m.WithRooms(msg)
	case CmdMsg:
		if a.cmdf == nil {
			return a.note("no command handler installed"), nil
		}
		return a, a.cmdf(a.m.Current().Name, msg.Cmd)
	case NoteMsg:
		if next, err := a.addLine(a.m.Current().Name, msg.L); err == nil {
			a = next
		}
	case LiteralResultMsg:
		if msg.Err != nil {
			a = a.note("literal send failed: " + msg.Err.Error())
		}
	case SendResultMsg:
		a = a.sent(msg)
	case FocusMsg:
		a = a.focusName(string(msg))
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
		case act.Kind == NextChan || act.Kind == PrevChan:
			return a.switchChan(act), nil
		}
		return a, nil // Complete: nick completion is not implemented
	}
	if a.pending == "" {
		switch k {
		case "/":
			return a.openPrompt(promptSearch), nil
		case "n", "N":
			return a.searchStep(k == "N"), nil
		}
	}
	act, mode, pending := Key(Normal, a.pending, k)
	a.pending = pending
	if mode == Insert {
		a.mode = Insert
		a.in.Focus()
	}
	switch act.Kind {
	case CmdLine:
		return a.openPrompt(promptCmd), nil
	case Scroll, HalfPage, Top, Bottom:
		a = a.scroll(act)
	case ToggleTools, ToggleChat:
		a.tools = a.tools != (act.Kind == ToggleTools)
		a.chat = a.chat != (act.Kind == ToggleChat)
		a.hit, a.offs = -1, nil // positions index the shown lines, which changed
	case Help:
		a.help = true
	case LastChan:
		a = a.focusName(a.prev)
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
		p := a.prompt
		var v string
		a, v = a.closePrompt()
		if p == promptCmd {
			return a.sendLiteral(v)
		}
		a.query = v // search continues from the last hit, kept when nothing matches
		return a.searchStep(false), nil
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

// focusName makes channel name current and remembers the one it leaves, for tab.
func (a App) focusName(name string) App {
	from := a.m.Current().Name
	for i, c := range a.m.Channels() {
		if c.Name == name && name != from {
			a.m, _ = a.m.Focus(i) // i is in range: it comes from Channels
			a.prev, a.hit = from, -1
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
	if c.Name() == model.CmdQuit {
		return a, tea.Quit
	}
	return a, func() tea.Msg { return CmdMsg{c} }
}

// note adds a dim local line to the current channel.
func (a App) note(text string) App {
	if m, err := a.m.AddLine(a.m.Current().Name, model.Line{At: a.now(), Nick: "*", Text: text, Dim: true}); err == nil {
		a.m = m
	}
	return a
}

// inputLine is the modeline label and the input row text for the current mode.
func (a App) inputLine() (label, input string) {
	switch {
	case a.prompt != 0:
		return "NORMAL", string(a.prompt) + a.in.Value()
	case a.mode == Insert:
		return "INSERT", a.in.Value()
	}
	return "NORMAL", keyHint("?", "help", "i", "type", ":", "literal", "t", "tools "+onOff(a.tools), "c", "chat only "+onOff(a.chat))
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
	label, input := a.inputLine()
	return render(a.m, model.Status(a.m, label, a.info, a.now()), input, a.w, a.h, a.off(), a.hit, chanNicks(a.m.Current(), a.user), a.lines(a.m.Current()))
}

// page is the number of scrollback rows View shows; a long input takes rows from it.
func (a App) page() int {
	_, input := a.inputLine()
	return paneRows(a.w, a.h, len(inputRows(input, a.w, a.h)))
}

// off is the current channel's scroll offset.
func (a App) off() int { return a.offs[a.m.Current().Name] }

// setOff stores ch's offset clamped to [0, lines-page], copy-on-write.
func (a App) setOff(ch string, off int) App {
	n := 0
	for _, c := range a.m.Channels() {
		if c.Name == ch {
			n = rowCount(a.lines(c), textWidth(a.w, a.h))
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
		return a.setOff(ch, rowCount(a.lines(cur), textWidth(a.w, a.h)))
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
	if a.offs[ch] > 0 && a.shows(l) {
		a = a.setOff(ch, a.offs[ch]+len(wrapLine(l, textWidth(a.w, a.h))))
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

// shows reports whether l passes the t and c toggles.
func (a App) shows(l model.Line) bool {
	return (a.tools || l.Kind != model.Tool) && (!a.chat || l.Kind == model.Chat)
}

// lines is the part of c's scrollback the toggles show; render, scroll and search all use it.
func (a App) lines(c model.Channel) []model.Line {
	var out []model.Line
	for _, l := range c.Lines.All() {
		if a.shows(l) {
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
