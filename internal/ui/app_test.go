package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"time"

	tea "charm.land/bubbletea/v2"

	"ircc/internal/model"
)

var special = map[string]tea.KeyPressMsg{
	"esc":    {Code: tea.KeyEscape},
	"enter":  {Code: tea.KeyEnter},
	"tab":    {Code: tea.KeyTab},
	"ctrl+d": {Code: 'd', Mod: tea.ModCtrl},
	"ctrl+u": {Code: 'u', Mod: tea.ModCtrl},
	"up":     {Code: tea.KeyUp},
	"down":   {Code: tea.KeyDown},
	"ctrl+n": {Code: 'n', Mod: tea.ModCtrl},
	"ctrl+p": {Code: 'p', Mod: tea.ModCtrl},
	"ctrl+t": {Code: 't', Mod: tea.ModCtrl},
}

type sent struct{ ch, text string }

// press feeds keys to a; each key is a rune string or a name from special.
func press(t *testing.T, a App, keys ...string) (App, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, k := range keys {
		msg, ok := special[k]
		if !ok {
			msg = tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
		}
		var next tea.Model
		next, cmd = a.Update(msg)
		a = next.(App)
	}
	return a, cmd
}

func typeText(t *testing.T, a App, s string) App {
	t.Helper()
	for _, r := range s {
		a, _ = press(t, a, string(r))
	}
	return a
}

func newApp(t *testing.T, log *[]sent, sendErr error) App {
	t.Helper()
	m, err := model.New(20)
	if err != nil {
		t.Fatal(err)
	}
	a := New(m, func(ch, text string) error {
		*log = append(*log, sent{ch, text})
		return sendErr
	})
	a.now = func() time.Time { return time.Date(2026, 10, 22, 12, 4, 0, 0, time.UTC) }
	next, _ := a.Update(SessionsMsg{{PID: 1, Name: "a"}, {PID: 2, Name: "b"}, {PID: 3, Name: "c"}})
	return next.(App)
}

func cur(a App) string { return a.m.Current().Name }

func TestAppChannelKeys(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	for _, c := range []struct {
		name string
		keys []string
		want string
	}{
		{"gt next", []string{"g", "t"}, "#b"},
		{"gT wraps to last", []string{"g", "T"}, "#c"},
		{"3gt", []string{"3", "g", "t"}, "#c"},
		{"gg does not switch", []string{"g", "g"}, "#a"},
		{"NEG g then x", []string{"g", "x"}, "#a"},
		{"NEG 9gt out of range", []string{"9", "g", "t"}, "#a"},
	} {
		got, _ := press(t, a, c.keys...)
		if cur(got) != c.want {
			t.Errorf("%s: current %q want %q", c.name, cur(got), c.want)
		}
	}
}

func TestAppQuit(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	a, _ = press(t, a, "i")
	a = typeText(t, a, "/quit")
	_, cmd := press(t, a, "enter")
	if cmd == nil || cmd() != tea.Quit() {
		t.Fatalf("want tea.Quit, got %v", cmd)
	}
}

func TestAppCommandMsgAndErrors(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	a, _ = press(t, a, "i")
	a = typeText(t, a, "/join &x")
	a, cmd := press(t, a, "enter")
	if cmd == nil {
		t.Fatal("no cmd")
	}
	if cm, ok := cmd().(CmdMsg); !ok || cm.Cmd.Name() != model.CmdJoin {
		t.Fatalf("got %#v", cmd())
	}
	// NEG: unknown command -> error line, no cmd, no send
	a, _ = press(t, a, "esc", "i")
	a = typeText(t, a, "/bogus")
	a, cmd = press(t, a, "enter")
	if cmd != nil || len(log) != 0 {
		t.Fatalf("cmd %v log %v", cmd, log)
	}
	if ls := curLines(a); len(ls) != 1 || !strings.Contains(ls[0].Text, "bogus") {
		t.Fatalf("error line: %+v", ls)
	}
	// esc cancels an open : prompt
	a, _ = press(t, a, "esc")
	a, _ = press(t, a, ":")
	a = typeText(t, a, "q")
	a, cmd = press(t, a, "esc")
	if cmd != nil || a.prompt != 0 {
		t.Fatal("esc did not cancel")
	}
}

// finish runs cmd and feeds its message back into a (sends complete here, off the Update path).
func finish(t *testing.T, a App, cmd tea.Cmd) App {
	t.Helper()
	if cmd == nil {
		t.Fatal("no cmd")
	}
	next, _ := a.Update(cmd())
	return next.(App)
}

func TestAppInsertSend(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	a, _ = press(t, a, "i")
	a = typeText(t, a, "hi there")
	a, cmd := press(t, a, "enter")
	if len(log) != 0 { // Update never blocks on send
		t.Fatalf("send ran inside Update: %v", log)
	}
	a = finish(t, a, cmd)
	if len(log) != 1 || log[0] != (sent{"#a", "hi there"}) {
		t.Fatalf("log %v", log)
	}
	if ls := curLines(a); len(ls) != 1 || ls[0].Nick != "me" || ls[0].Text != "hi there" {
		t.Fatalf("echo: %+v", ls)
	}
	if a.in.Value() != "" {
		t.Fatalf("input not cleared: %q", a.in.Value())
	}
	// "/quit" in insert mode is a command, not a send
	a = typeText(t, a, "/quit")
	a, cmd = press(t, a, "enter")
	if len(log) != 1 || cmd == nil || cmd() != tea.Quit() {
		t.Fatalf("log %v cmd %v", log, cmd)
	}
	// esc returns to normal: j is a key again, not text
	a, _ = press(t, a, "esc", "j")
	if a.in.Value() != "" {
		t.Fatalf("j typed in normal mode: %q", a.in.Value())
	}
}

func TestAppSendErrorShown(t *testing.T) {
	var log []sent
	a := newApp(t, &log, errors.New("boom"))
	a, _ = press(t, a, "i")
	a = typeText(t, a, "x")
	a, cmd := press(t, a, "enter")
	a = finish(t, a, cmd)
	ls := curLines(a)
	if len(ls) != 1 || !strings.Contains(ls[0].Text, "send failed") || !strings.Contains(ls[0].Text, "boom") {
		t.Fatalf("lines %+v", ls) // and no echo of the unsent text
	}
}

func TestAppCtrlCQuits(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	for _, pre := range [][]string{nil, {"i"}, {":"}} {
		b, _ := press(t, a, pre...)
		_, cmd := b.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		if cmd == nil || cmd() != tea.Quit() {
			t.Errorf("ctrl+c after %v did not quit", pre)
		}
	}
}

func TestAppUnknownChannelLineNoted(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	next, _ := a.Update(LineMsg{Ch: "#zz", L: model.Line{Text: "x"}})
	ls := curLines(next.(App))
	if len(ls) != 1 || !strings.Contains(ls[0].Text, "#zz") {
		t.Fatalf("lines %+v", ls)
	}
}

func TestAppInfoMsgCopied(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	info := map[int]model.SessionInfo{1: {Status: "idle", PermMode: "default"}}
	next, _ := a.Update(InfoMsg(info))
	a = next.(App)
	info[1] = model.SessionInfo{Status: "idle", PermMode: "bypassPermissions"}
	if got := a.info[1].PermMode; got != "default" {
		t.Fatalf("InfoMsg aliased: %q", got)
	}
}

func TestAppSearch(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	for _, s := range []string{"alpha", "needle one", "gamma", "needle two"} {
		next, _ := a.Update(LineMsg{Ch: "#a", L: model.Line{Nick: "n", Text: s}})
		a = next.(App)
	}
	a, _ = press(t, a, "/")
	a = typeText(t, a, "needle")
	a, _ = press(t, a, "enter")
	if a.hit != 1 {
		t.Fatalf("first hit %d", a.hit)
	}
	a, _ = press(t, a, "n")
	if a.hit != 3 {
		t.Fatalf("n: %d", a.hit)
	}
	a, _ = press(t, a, "N")
	if a.hit != 1 {
		t.Fatalf("N: %d", a.hit)
	}
	// NEG: no match leaves hit unchanged
	a, _ = press(t, a, "/")
	a = typeText(t, a, "zzz")
	a, _ = press(t, a, "enter")
	if a.hit != 1 {
		t.Fatalf("no-match moved hit to %d", a.hit)
	}
}

func TestAppMessagesAndView(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	next, _ := a.Update(LineMsg{Ch: "#b", L: model.Line{Nick: "bob", Text: "yo"}})
	a = next.(App)
	next, _ = a.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	a = next.(App)
	v := ansi.Strip(a.screen())
	if !strings.Contains(v, "#b"+strings.Repeat(" ", 9)+"1") || !strings.Contains(v, "NORMAL") || !strings.Contains(v, "12:04") {
		t.Fatalf("view:\n%s", v)
	}
	// NEG: line for an unknown channel is dropped without panic
	next, _ = a.Update(LineMsg{Ch: "#zz", L: model.Line{Text: "x"}})
	_ = next.(App).screen()
}

func curLines(a App) []model.Line {
	c := a.m.Current()
	return c.Lines.All()
}

// scrollApp has 12 lines in #a and a 10-row screen: page 6, max offset 6, half page 3.
func scrollApp(t *testing.T) App {
	t.Helper()
	var log []sent
	a := newApp(t, &log, nil)
	for i := 0; i < 12; i++ {
		next, _ := a.Update(LineMsg{Ch: "#a", L: model.Line{Nick: "n", Text: "msg" + string(rune('A'+i))}})
		a = next.(App)
	}
	next, _ := a.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	return next.(App)
}

func TestAppScroll(t *testing.T) {
	for _, c := range []struct {
		name string
		keys []string
		want int
	}{
		{"k scrolls back one", []string{"k"}, 1},
		{"k then j", []string{"k", "k", "j"}, 1},
		{"NEG j at newest stays 0", []string{"j"}, 0},
		{"NEG k clamps at oldest page", []string{"k", "k", "k", "k", "k", "k", "k", "k"}, 6},
		{"ctrl+u half page back", []string{"ctrl+u"}, 3},
		{"ctrl+u twice clamps", []string{"ctrl+u", "ctrl+u"}, 6},
		{"ctrl+d half page forward", []string{"gg", "ctrl+d"}, 3},
		{"G to newest", []string{"gg", "G"}, 0},
	} {
		a := scrollApp(t)
		keys := []string{}
		for _, k := range c.keys {
			if k == "gg" {
				keys = append(keys, "g", "g")
			} else {
				keys = append(keys, k)
			}
		}
		a, _ = press(t, a, keys...)
		if a.off() != c.want {
			t.Errorf("%s: off %d want %d", c.name, a.off(), c.want)
		}
	}
	if a, _ := press(t, scrollApp(t), "g", "g"); a.off() != 6 {
		t.Errorf("gg: off %d want 6", a.off())
	}
}

func TestAppScrollAnchorsAndIsPerChannel(t *testing.T) {
	a := scrollApp(t)
	a, _ = press(t, a, "k", "k")
	next, _ := a.Update(LineMsg{Ch: "#a", L: model.Line{Text: "new"}})
	a = next.(App)
	if a.off() != 3 { // new line keeps the view anchored
		t.Fatalf("anchored off %d want 3", a.off())
	}
	a, _ = press(t, a, "G")
	next, _ = a.Update(LineMsg{Ch: "#a", L: model.Line{Text: "newer"}})
	if next.(App).off() != 0 { // at the bottom, stay following
		t.Fatalf("followed off %d want 0", next.(App).off())
	}
	a, _ = press(t, a, "k", "g", "t")
	if a.off() != 0 {
		t.Fatalf("offset leaked to #b: %d", a.off())
	}
	a, _ = press(t, a, "g", "T")
	if a.off() != 1 {
		t.Fatalf("#a offset lost: %d", a.off())
	}
}

func TestAppViewScrolled(t *testing.T) {
	a := scrollApp(t)
	if v := a.screen(); !strings.Contains(v, "msgL") || strings.Contains(v, "msgA") {
		t.Fatalf("bottom view:\n%s", v)
	}
	a, _ = press(t, a, "g", "g")
	if v := a.screen(); !strings.Contains(v, "msgA") || strings.Contains(v, "msgL") {
		t.Fatalf("top view:\n%s", v)
	}
}

func TestAppSearchHitDisplayed(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	for _, s := range []string{"alpha", "needle one", "gamma"} {
		next, _ := a.Update(LineMsg{Ch: "#a", L: model.Line{Nick: "n", Text: s}})
		a = next.(App)
	}
	hasRev := func() bool {
		for _, r := range strings.Split(a.screen(), "\n") {
			if strings.Contains(r, "needle one") {
				return strings.Contains(r, "\x1b[7m")
			}
		}
		t.Fatal("line not on screen")
		return false
	}
	if hasRev() {
		t.Fatal("reversed before any search")
	}
	a, _ = press(t, a, "/")
	a = typeText(t, a, "needle")
	a, _ = press(t, a, "enter")
	if !hasRev() {
		t.Fatal("hit line not reversed")
	}
}

func TestAppNoteMsgGoesToFocused(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	a, _ = press(t, a, "g", "t") // focus #b
	next, _ := a.Update(NoteMsg{L: model.Line{Text: "hello", Dim: true}})
	a = next.(App)
	if ls := curLines(a); len(ls) != 1 || ls[0].Text != "hello" {
		t.Fatalf("focused lines %+v", ls)
	}
	for _, c := range a.m.Channels() {
		if c.Name != "#b" && c.Lines.Len() != 0 {
			t.Fatalf("%s got a line", c.Name)
		}
	}
	// NEG: no channels at all -> nothing to land on, no panic
	empty, err := model.New(3)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = New(empty, nil).Update(NoteMsg{L: model.Line{Text: "x"}})
}

func TestAppLineMsgEmptyChIsUnknown(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	next, _ := a.Update(LineMsg{L: model.Line{Text: "x"}}) // no sentinel: "" is an unknown channel
	ls := curLines(next.(App))
	if len(ls) != 1 || !strings.Contains(ls[0].Text, "unknown channel") || ls[0].Text == "x" {
		t.Fatalf("lines %+v", ls)
	}
}

func TestAppNoteMsgAnchorsWhenScrolled(t *testing.T) {
	a := scrollApp(t)
	a, _ = press(t, a, "k", "k")
	next, _ := a.Update(NoteMsg{L: model.Line{Text: "n"}})
	if got := next.(App).off(); got != 3 {
		t.Fatalf("off %d want 3", got)
	}
}

func TestAppScrollCountsWrappedRows(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	next, _ := a.Update(tea.WindowSizeMsg{Width: 80, Height: 8}) // 4 pane rows
	a = next.(App)
	for _, s := range []string{"a", strings.Repeat("word ", 40), "z"} { // the middle line wraps to >= 3 rows
		next, _ := a.Update(LineMsg{Ch: "#a", L: model.Line{Nick: "n", Text: s}})
		a = next.(App)
	}
	a, _ = press(t, a, "g", "g")
	if a.off() < 2 { // 3 lines would give max 0; wrapped rows give more
		t.Fatalf("gg off %d: wrapped rows not counted", a.off())
	}
	before := a.off()
	next, _ = a.Update(LineMsg{Ch: "#a", L: model.Line{Nick: "n", Text: strings.Repeat("word ", 40)}})
	if got := next.(App).off(); got < before+2 { // anchored by the new line's wrapped rows
		t.Fatalf("anchor %d -> %d", before, got)
	}
}

func TestAppCmdMsgCallsHandlerWithFocusedChannel(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	var gotCh string
	var gotName model.CmdName
	a = a.WithCmd(func(ch string, c model.Cmd) tea.Cmd {
		gotCh, gotName = ch, c.Name()
		return func() tea.Msg { return NoteMsg{L: model.Line{Text: "done"}} }
	})
	a, _ = press(t, a, "g", "t") // focus #b
	c, err := model.ParseCmd("/names")
	if err != nil {
		t.Fatal(err)
	}
	next, cmd := a.Update(CmdMsg{c})
	a = next.(App)
	if gotCh != "#b" || gotName != model.CmdNames || cmd == nil {
		t.Fatalf("handler got ch=%q name=%q cmd=%v", gotCh, gotName, cmd)
	}
	if n, ok := cmd().(NoteMsg); !ok || n.L.Text != "done" {
		t.Fatalf("cmd result %#v", cmd())
	}
	// NEG: no handler installed -> a visible note, no panic, no cmd
	b := newApp(t, &log, nil)
	next, cmd = b.Update(CmdMsg{c})
	b = next.(App)
	if cmd != nil || len(curLines(b)) != 1 {
		t.Fatalf("no-handler: cmd=%v lines=%+v", cmd, curLines(b))
	}
}

func TestAppRoomsMsgSetsRoomChannels(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	next, _ := a.Update(RoomsMsg{"&x", "&y"})
	a = next.(App)
	var got []string
	for _, c := range a.m.Channels() {
		if c.Kind == model.RoomChan {
			got = append(got, c.Name)
		}
	}
	if len(got) != 2 || got[0] != "&x" || got[1] != "&y" {
		t.Fatalf("rooms %v", got)
	}
	next, _ = a.Update(RoomsMsg{})
	for _, c := range next.(App).m.Channels() {
		if c.Kind == model.RoomChan {
			t.Fatalf("room %s survived empty RoomsMsg", c.Name)
		}
	}
}

func TestAppNoLocalEchoForRooms(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	next, _ := a.Update(RoomsMsg{"&r"})
	next, _ = next.(App).Update(SendResultMsg{Ch: "&r", Text: "x"})
	next, _ = next.(App).Update(SendResultMsg{Ch: "#a", Text: "y"}) // control: sessions still echo
	for _, c := range next.(App).m.Channels() {
		n := len(c.Lines.All())
		if (c.Name == "&r" && n != 0) || (c.Name == "#a" && n != 1) {
			t.Fatalf("%s has %d lines", c.Name, n)
		}
	}
}

func TestAppPasteIsText(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	next, _ := a.Update(tea.PasteMsg{Content: "gt"}) // NORMAL: paste is not keys
	if a = next.(App); cur(a) != "#a" {
		t.Fatalf("paste switched channel: %s", cur(a))
	}
	a, _ = press(t, a, "i")
	next, _ = a.Update(tea.PasteMsg{Content: "gt"})
	if a = next.(App); cur(a) != "#a" || a.in.Value() != "gt" {
		t.Fatalf("insert paste: %s %q", cur(a), a.in.Value())
	}
}

func TestColonSendsLiteral(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	var lit []sent
	a = a.WithLiteral(func(ch, text string) error { lit = append(lit, sent{ch, text}); return nil })
	a, _ = press(t, a, ":")
	a = typeText(t, a, "/join &x")
	a, cmd := press(t, a, "enter")
	a = finish(t, a, cmd)
	if len(lit) != 1 || lit[0] != (sent{"#a", "/join &x"}) || len(log) != 0 { // literal, not a command, not a wrapped send
		t.Fatalf("literal %v sends %v", lit, log)
	}
	if len(curLines(a)) != 0 { // no local echo: the transcript shows it
		t.Fatalf("echo %+v", curLines(a))
	}
}
