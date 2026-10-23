package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"ircc/internal/model"
)

// checkFrame fails unless View is exactly h rows of exactly w cells: a taller or
// wider frame scrolls the terminal and the screen looks frozen.
func checkFrame(t *testing.T, a App, w, h int) string {
	t.Helper()
	v := a.screen()
	rows := strings.Split(v, "\n")
	if len(rows) != h {
		t.Fatalf("%dx%d: %d rows", w, h, len(rows))
	}
	for i, r := range rows {
		if got := lipgloss.Width(r); got != w {
			t.Fatalf("%dx%d: row %d is %d cells: %q", w, h, i, got, r)
		}
	}
	return v
}

func TestFrameExactSizeAndKeysMove(t *testing.T) {
	hostile := []string{
		"tab\there", "two\nlines", "cr\rreturn", "wide 漢字テスト 👍🏽 ✻ ●",
		"\x1b[31mansi\x1b[0m", strings.Repeat("long ", 80), "zero​width", "",
	}
	for _, sz := range [][2]int{{120, 30}, {80, 24}, {200, 60}, {60, 15}} {
		w, h := sz[0], sz[1]
		var log []sent
		a := newApp(t, &log, nil)
		next, _ := a.Update(tea.WindowSizeMsg{Width: w, Height: h})
		a = next.(App)
		for _, s := range hostile {
			next, _ = a.Update(LineMsg{Ch: "#a", L: model.Line{Nick: "bot 🤖", Text: s}})
			a = next.(App)
		}
		checkFrame(t, a, w, h)
		a, _ = press(t, a, "g", "t")
		if cur(a) != "#b" {
			t.Fatalf("gt: current %s", cur(a))
		}
		if v := checkFrame(t, a, w, h); !strings.Contains(v, "#b") {
			t.Fatalf("gt: frame does not show #b")
		}
	}
}

func TestHelpAndArrows(t *testing.T) {
	for _, sz := range [][2]int{{120, 30}, {80, 24}, {40, 10}} {
		var log []sent
		a := newApp(t, &log, nil)
		next, _ := a.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		a, _ = press(t, next.(App), "?")
		if v := checkFrame(t, a, sz[0], sz[1]); !strings.Contains(v, "one # channel per Claude session") {
			t.Fatalf("%v: no help on screen", sz)
		}
		a, _ = press(t, a, "j") // closes help, does not scroll
		if a.help || strings.Contains(checkFrame(t, a, sz[0], sz[1]), "one # channel per Claude session") {
			t.Fatalf("%v: help still shown", sz)
		}
		a, _ = press(t, a, "ctrl+n")
		if cur(a) != "#b" {
			t.Fatalf("ctrl+n: %s", cur(a))
		}
		a, _ = press(t, a, "ctrl+p", "ctrl+p")
		if cur(a) != "#c" { // wraps
			t.Fatalf("ctrl+p ctrl+p: %s", cur(a))
		}
		a, _ = press(t, a, "i", "ctrl+n", "down", "up") // insert mode: switches, arrows never type
		if cur(a) != "#a" || a.in.Value() != "" {
			t.Fatalf("insert ctrl+n: %s %q", cur(a), a.in.Value())
		}
	}
}

func TestWheelScrollsAndNickPane(t *testing.T) {
	a := scrollApp(t) // 12 lines in #a, page 6
	wheel := func(b tea.MouseButton) {
		next, _ := a.Update(tea.MouseWheelMsg{Button: b})
		a = next.(App)
	}
	wheel(tea.MouseWheelUp)
	wheel(tea.MouseWheelUp)
	if cur(a) != "#a" || a.off() != 6 { // 3 rows a notch, clamped at 12-6
		t.Fatalf("wheel up x2: %s off %d", cur(a), a.off())
	}
	wheel(tea.MouseWheelDown)
	if a.off() != 3 {
		t.Fatalf("wheel down: off %d", a.off())
	}
	a.user = "vic"
	if got := chanNicks(a.m.Current(), a.user); len(got) != 2 || got[0] != "vic" || got[1] != "a" {
		t.Fatalf("session nicks %v", got)
	}
	next, _ := a.Update(RoomsMsg{"&r"})
	next, _ = next.(App).Update(MembersMsg{Ch: "&r", Nicks: []string{"foo", "bar"}})
	next, _ = next.(App).Update(MembersMsg{Ch: "#a", Nicks: []string{"x"}}) // NEG: # has no members
	for _, c := range next.(App).m.Channels() {
		got := chanNicks(c, "vic")
		if c.Name == "&r" && strings.Join(got, ",") != "vic,foo,bar" || c.Name == "#a" && strings.Join(got, ",") != "vic,a" {
			t.Fatalf("%s nicks %v", c.Name, got)
		}
	}
	if v := ansi.Strip(next.(App).screen()); !strings.Contains(v, "💤vic") {
		t.Fatal("nick pane empty in View")
	}
}

func TestToggles(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	for _, l := range []model.Line{{Nick: "me", Text: "hi"}, {Nick: "a", Text: "thinking out loud", Kind: model.Transcript}, {Nick: "a", Text: "* Bash: ls", Kind: model.Tool}} {
		next, _ := a.Update(LineMsg{Ch: "#a", L: l})
		a = next.(App)
	}
	shown := func() string {
		var s []string
		for _, l := range a.lines(a.m.Current()) {
			s = append(s, l.Text)
		}
		return strings.Join(s, "|")
	}
	if got := shown(); got != "hi|thinking out loud" { // default: tools hidden
		t.Fatalf("default %q", got)
	}
	a, _ = press(t, a, "t")
	if got := shown(); got != "hi|thinking out loud|* Bash: ls" || !strings.Contains(a.screen(), "* Bash: ls") {
		t.Fatalf("t %q", got)
	}
	a, _ = press(t, a, "m")
	if got := shown(); got != "hi" || strings.Contains(a.screen(), "thinking out loud") {
		t.Fatalf("m %q", got)
	}
	a, _ = press(t, a, "g", "t") // NEG: gt still switches channel, does not toggle
	if cur(a) != "#b" || !a.tools {
		t.Fatalf("gt: %s tools %v", cur(a), a.tools)
	}
}

func TestColorsKeepFrameSize(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	for _, l := range []model.Line{{Nick: "carol", Text: strings.Repeat("wrap me ", 30)}, {Nick: "alice", Text: "hi"}, {Nick: "bob", Text: "* Bash: ls", Kind: model.Tool, Dim: true}} {
		next, _ := a.Update(LineMsg{Ch: "#a", L: l})
		a = next.(App)
	}
	a, _ = press(t, a, "t")
	for _, sz := range [][2]int{{120, 30}, {80, 24}, {39, 10}} {
		next, _ := a.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		v := checkFrame(t, next.(App), sz[0], sz[1])
		if !strings.Contains(v, nickStyle("alice").Render("<alice>")) {
			t.Fatalf("%v: nick not colored", sz)
		}
	}
	if nickStyle("alice").Render("x") == nickStyle("bob").Render("x") && nickStyle("alice").Render("x") == nickStyle("carol").Render("x") {
		t.Fatal("all nicks one color")
	}
	if nickStyle("alice").Render("x") != nickStyle("alice").Render("x") { // stable
		t.Fatal("nick color not stable")
	}
}

func TestInputGrows(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	next, _ := a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a, _ = press(t, next.(App), "i")
	short := a.page()
	a = typeText(t, a, strings.Repeat("word ", 40)+"END")
	v := checkFrame(t, a, 80, 24)
	if a.page() >= short || !strings.Contains(v, "END") {
		t.Fatalf("page %d (was %d), END shown %v", a.page(), short, strings.Contains(v, "END"))
	}
	a = typeText(t, a, strings.Repeat(" more", 400))
	checkFrame(t, a, 80, 24)
	if got := len(inputRows(a.in.Value(), 80, 24)); got != 24/3 { // capped
		t.Fatalf("input rows %d", got)
	}
}

func TestRecentFirstAndPaneFocus(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil) // #a #b #c, focus #a
	order := func() string {
		var s []string
		for _, c := range a.m.Channels() {
			s = append(s, c.Name)
		}
		return strings.Join(s, " ")
	}
	next, _ := a.Update(LineMsg{Ch: "#c", L: model.Line{Nick: "c", Text: "news"}})
	if a = next.(App); order() != "#c #a #b" || cur(a) != "#a" { // newest first, focus kept
		t.Fatalf("order %s cur %s", order(), cur(a))
	}
	// tab focuses the channel pane: type to filter, enter joins (vic 2026-10-22)
	a, _ = press(t, a, "tab")
	a = typeText(t, a, "b")
	a, _ = press(t, a, "enter")
	if cur(a) != "#b" || a.focus != focusChat {
		t.Fatalf("channel pane: %s focus %d", cur(a), a.focus)
	}
	// tab tab focuses nicks: enter starts a line mentioning the selected nick
	a, _ = press(t, a, "tab", "tab", "down", "enter")
	if a.mode != Insert || a.in.Value() != "@b " {
		t.Fatalf("nick pane: mode %v input %q", a.mode, a.in.Value())
	}
	a, _ = press(t, a, "esc", "tab", "esc") // NEG: esc leaves the pane, nothing else moves
	if a.focus != focusChat || cur(a) != "#b" {
		t.Fatalf("esc: focus %d cur %s", a.focus, cur(a))
	}
	checkFrame(t, a, a.w, a.h)
}

func TestNickColorsEverywhere(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	next, _ := a.Update(LineMsg{Ch: "#a", L: model.Line{Nick: "bob", Text: "ask @carol and @ren"}})
	v := next.(App).screen()
	for _, want := range []string{nickStyle("carol").Bold(true).Render("@carol"), nickStyle("ren").Bold(true).Render("@ren"), nickStyle("b").Render("#b")} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if got := ansi.Strip(mentions("x @y z", cell)); got != "x @y z" { // text unchanged
		t.Fatalf("mentions changed text: %q", got)
	}
}

func TestStampsToggle(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	at := time.Date(2026, 10, 22, 9, 7, 0, 0, time.Local)
	next, _ := a.Update(LineMsg{Ch: "#a", L: model.Line{At: at, Nick: "bob", Text: strings.Repeat("long text ", 20)}})
	a = next.(App)
	if strings.Contains(ansi.Strip(a.screen()), "09:07") {
		t.Fatal("stamp shown while off")
	}
	a, _ = press(t, a, "s")
	v := checkFrame(t, a, 80, 24)
	if !strings.Contains(ansi.Strip(v), "09:07 <bob>") || !strings.Contains(v, nickStyle("bob").Render("<bob>")) {
		t.Fatalf("stamp or nick color missing:\n%s", ansi.Strip(v))
	}
}

func TestPicker(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil) // #a #b #c
	next, _ := a.Update(RoomsMsg{"&handson", "&review"})
	a, _ = press(t, next.(App), "i")
	a = typeText(t, a, "half typed")
	a, _ = press(t, a, "ctrl+t")
	a = typeText(t, a, "hon") // fuzzy: h..o..n in &handson only
	v := checkFrame(t, a, 80, 24)
	if !strings.Contains(ansi.Strip(v), "◆ &handson") || strings.Contains(ansi.Strip(v), "&review") {
		t.Fatalf("picker:\n%s", ansi.Strip(v))
	}
	a, _ = press(t, a, "enter")
	if a.pick || cur(a) != "&handson" || a.in.Value() != "half typed" {
		t.Fatalf("enter: pick %v cur %s input %q", a.pick, cur(a), a.in.Value())
	}
	a, _ = press(t, a, "ctrl+t", "x", "y", "z", "enter") // NEG: no match, enter stays
	if a.pick || cur(a) != "&handson" {
		t.Fatalf("no-match enter: %s", cur(a))
	}
	if _, ok := fuzzy("hn", "#handson"); !ok {
		t.Fatal("subsequence not matched")
	}
	if _, ok := fuzzy("nh", "#handson"); ok { // NEG: order matters
		t.Fatal("out of order matched")
	}
}

func TestTabCompletion(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil) // #a #b #c
	next, _ := a.Update(RoomsMsg{"&handson", "&hacks"})
	a, _ = press(t, next.(App), "i")
	a = typeText(t, a, "/inv")
	if s := a.suggest(); len(s) != 1 || !strings.Contains(ansi.Strip(s[0]), "/invite") {
		t.Fatalf("suggest %q", s)
	}
	a, _ = press(t, a, "tab")
	if a.in.Value() != "/invite" {
		t.Fatalf("command: %q", a.in.Value())
	}
	a = typeText(t, a, " b")
	a, _ = press(t, a, "tab")
	if a.in.Value() != "/invite b" { // NEG: "b" is a full nick, nothing longer
		t.Fatalf("nick: %q", a.in.Value())
	}
	a = typeText(t, a, " &ha")
	a, _ = press(t, a, "tab")
	first := a.in.Value()
	a, _ = press(t, a, "tab")
	if first == a.in.Value() || !strings.HasPrefix(first, "/invite b &ha") || !strings.HasPrefix(a.in.Value(), "/invite b &ha") { // cycles
		t.Fatalf("channel cycle: %q then %q", first, a.in.Value())
	}
	checkFrame(t, a, 80, 24)
}

func TestMentionsInRooms(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	a.user = "vic"
	next, _ := a.Update(RoomsMsg{"&r"})
	a = next.(App)
	for _, l := range []model.Line{{Nick: "lib1", Text: "public note"}, {Nick: "lib2", Text: "@vic look"}, {Nick: "vic", Text: "mine"}, {Nick: "x", Text: "@vicky no"}} {
		next, _ = a.Update(LineMsg{Ch: "&r", L: l})
		a = next.(App)
	}
	a, _ = press(t, a, "m")
	var got []string
	for _, c := range a.m.Channels() {
		if c.Name == "&r" {
			for _, l := range a.lines(c) {
				got = append(got, l.Text)
			}
		}
	}
	if strings.Join(got, "|") != "@vic look|mine" { // NEG: @vicky is not @vic
		t.Fatalf("mentions %q", got)
	}
}

func TestInsertKeys(t *testing.T) {
	for _, k := range []string{"i", "I", "a", "A", "o", "O"} {
		var log []sent
		a, _ := press(t, newApp(t, &log, nil), k)
		if a.mode != Insert || a.in.Value() != "" { // O with no mention: plain insert
			t.Errorf("%s: mode %v input %q", k, a.mode, a.in.Value())
		}
	}
	var log []sent
	a := newApp(t, &log, nil)
	a.user = "vic"
	for _, txt := range []string{"ask @lib1", "@lib2 and @vic please"} {
		next, _ := a.Update(LineMsg{Ch: "#a", L: model.Line{Nick: "x", Text: txt}})
		a = next.(App)
	}
	a, _ = press(t, a, "O")
	if a.in.Value() != "@lib2 " { // newest mention that is not the user
		t.Fatalf("O: %q", a.in.Value())
	}
}

func TestSearchPickerJumps(t *testing.T) {
	a := scrollApp(t) // 12 lines in #a, page 6
	next, _ := a.Update(LineMsg{Ch: "#a", L: model.Line{Nick: "ren", Text: "the needle " + strings.Repeat("long ", 60)}})
	a = next.(App)
	for i := 0; i < 8; i++ { // push the needle out of view
		next, _ = a.Update(LineMsg{Ch: "#a", L: model.Line{Nick: "x", Text: "filler"}})
		a = next.(App)
	}
	a, _ = press(t, a, "/")
	a = typeText(t, a, "ndl")
	checkFrame(t, a, a.w, a.h)
	a, _ = press(t, a, "enter")
	if a.pick || a.hit < 0 || !strings.Contains(a.lines(a.m.Current())[a.hit].Text, "needle") || a.off() == 0 {
		t.Fatalf("pick %v hit %d off %d", a.pick, a.hit, a.off())
	}
	if !strings.Contains(ansi.Strip(a.screen()), "the needle") {
		t.Fatal("hit line not on screen")
	}
}

func TestMultilineKeepsLayout(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	next, _ := a.Update(LineMsg{Ch: "#a", L: model.Line{Nick: "bob", Text: "Plan:\n1. first\n    code indented\n\nlast"}})
	a = next.(App)
	v := ansi.Strip(checkFrame(t, a, 80, 24))
	for _, want := range []string{"<bob>      Plan:", "           1. first", "               code indented", "           last"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q in\n%s", want, v)
		}
	}
}

func TestMarkdownColors(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	txt := "# Plan\n- run `go test` **now**\n```\nfmt.Println(1)\n```\nsee *this* and snake_case_name"
	next, _ := a.Update(LineMsg{Ch: "#a", L: model.Line{Nick: "bob", Text: txt}})
	a = next.(App)
	v := checkFrame(t, a, 80, 24)
	for _, want := range []string{title.Render("# Plan"), codeSt.Render("go test"), cell.Bold(true).Render("now"), codeSt.Render("fmt.Println(1)"), cell.Italic(true).Render("this"), shortcut.Render("-")} {
		if !strings.Contains(v, want) {
			t.Errorf("missing styled %q", ansi.Strip(want))
		}
	}
	if strings.Contains(v, cell.Italic(true).Render("case")) { // NEG: snake_case is not emphasis
		t.Error("snake_case styled as italic")
	}
	if s := ansi.Strip(v); !strings.Contains(s, "- run `go test` **now**") || !strings.Contains(s, "snake_case_name") {
		t.Fatalf("text changed:\n%s", s)
	}
}

func TestBusyMarks(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)                                     // #a pid 1, user "me"
	if got := strings.Join(a.paneNicks(), ","); got != "💤me,💤a" { // control: idle, not typing
		t.Fatalf("idle %q", got)
	}
	next, _ := a.Update(InfoMsg{1: {Status: "busy"}})
	a, _ = press(t, next.(App), "i", "x")
	if got := strings.Join(a.paneNicks(), ","); got != "💬me,⌛a" {
		t.Fatalf("busy+typing %q", got)
	}
	if !strings.Contains(ansi.Strip(a.screen()), "⌛a") {
		t.Fatal("mark not on screen")
	}
	next, _ = a.Update(RoomsMsg{"&r"})
	next, _ = next.(App).Update(MembersMsg{Ch: "&r", Nicks: []string{"lib1"}})
	a = next.(App).focusName("&r")
	a.m, _ = a.m.WithRoomMembers("&r", []model.Member{{Nick: "a"}, {Nick: "x@a"}, {Nick: "b"}}) // no PIDs, as at runtime
	if got := strings.Join(a.paneNicks(), ","); got != "💬me,⌛a,⌛x@a,💤b" {                       // NEG: b (pid 2) is idle
		t.Fatalf("room %q", got)
	}
}

func TestAgentsInPane(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	next, _ := a.Update(AgentsMsg{Ch: "#a", Agents: []model.Agent{{Nick: "driver-1", Busy: true}, {Nick: "scribe-1"}}})
	a = next.(App)
	if got := strings.Join(a.paneNicks(), ","); got != "💤me,💤a,⌛driver-1,💤scribe-1" {
		t.Fatalf("pane %q", got)
	}
	next, _ = a.Update(AgentsMsg{Ch: "&nope", Agents: []model.Agent{{Nick: "x"}}}) // NEG: unknown channel ignored
	a, _ = press(t, next.(App), "i")
	a = typeText(t, a, "ask @dri")
	a, _ = press(t, a, "tab")
	if a.in.Value() != "ask @driver-1" {
		t.Fatalf("complete %q", a.in.Value())
	}
}

func TestSpeakerJumps(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	for _, n := range []string{"vic", "vic", "vic", "x", "vic"} {
		next, _ := a.Update(LineMsg{Ch: "#a", L: model.Line{Nick: n, Text: "t"}})
		a = next.(App)
	}
	var at []int
	for _, k := range []string{"J", "J", "J", "J", "K", "K"} {
		a, _ = press(t, a, k)
		at = append(at, a.hit)
	}
	if fmt.Sprint(at) != "[0 3 4 4 3 0]" { // run starts 0 (vic) 3 (x) 4 (vic); NEG: stops at the ends
		t.Fatalf("hits %v", at)
	}
	var log2 []sent
	b := newApp(t, &log2, nil)
	for _, n := range []string{"vic", "x", "x"} {
		next, _ := b.Update(LineMsg{Ch: "#a", L: model.Line{Nick: n, Text: "t"}})
		b = next.(App)
	}
	if b, _ = press(t, b, "K"); b.hit != 1 { // no cursor: K starts from the newest
		t.Fatalf("K from bottom: %d", b.hit)
	}
}

func TestFocusBorder(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	hl := focusBorder.Render("│")
	row := func(a App) string { return strings.Split(a.screen(), "\n")[1] }
	if r := row(a); strings.HasPrefix(r, hl) || !strings.Contains(r, hl) { // chat: middle borders only
		t.Fatalf("chat row %q", r)
	}
	a, _ = press(t, a, "tab")
	if r := row(a); !strings.HasPrefix(r, hl) || strings.HasSuffix(r, hl) { // channels: left edge, NEG: not the right edge
		t.Fatalf("channels row %q", r)
	}
	a, _ = press(t, a, "tab")
	if r := row(a); strings.HasPrefix(r, hl) || !strings.HasSuffix(r, hl) {
		t.Fatalf("nicks row %q", r)
	}
	checkFrame(t, a, a.w, a.h)
}

func TestPickerNicks(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	next, _ := a.Update(AgentsMsg{Ch: "#a", Agents: []model.Agent{{Nick: "driver-1"}}})
	a, _ = press(t, next.(App), "ctrl+t")
	a = typeText(t, a, "@dri")
	a, _ = press(t, a, "enter")
	if a.pick || a.mode != Insert || a.in.Value() != "@driver-1 " || cur(a) != "#a" {
		t.Fatalf("nick pick: mode %v input %q cur %s", a.mode, a.in.Value(), cur(a))
	}
	a, _ = press(t, a, "esc", "ctrl+t")
	a = typeText(t, a, "#b")
	a, _ = press(t, a, "enter")
	if cur(a) != "#b" { // control: channels still work
		t.Fatalf("channel pick: %s", cur(a))
	}
}

func TestLineCursor(t *testing.T) {
	a := scrollApp(t) // 12 lines in #a, page 6
	a, _ = press(t, a, "up")
	if a.hit != 11 || a.off() != 0 || cur(a) != "#a" { // newest line, no scroll, channel kept
		t.Fatalf("up: hit %d off %d cur %s", a.hit, a.off(), cur(a))
	}
	for i := 0; i < 7; i++ {
		a, _ = press(t, a, "up")
	}
	if a.hit != 4 || a.off() != 2 { // scrolled just enough: line 4 is the top row
		t.Fatalf("up x8: hit %d off %d", a.hit, a.off())
	}
	for i := 0; i < 8; i++ {
		a, _ = press(t, a, "down")
	}
	if a.hit != -1 || a.off() != 0 { // down past the newest clears the cursor
		t.Fatalf("down: hit %d off %d", a.hit, a.off())
	}
}

func TestInputCursor(t *testing.T) {
	if got := withCursor("abc", 1); got != "a"+current.Render("b")+"c" {
		t.Fatalf("mid %q", got)
	}
	if got := withCursor("ab", 2); got != "ab"+current.Render(" ") {
		t.Fatalf("end %q", got)
	}
	var log []sent
	a, _ := press(t, newApp(t, &log, nil), "i")
	a = typeText(t, a, "hi")
	if !strings.Contains(a.screen(), "hi"+current.Render(" ")) {
		t.Fatal("no cursor on screen")
	}
	checkFrame(t, a, a.w, a.h)
}

func TestPromptOnlyInInsert(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	last := func(a App) string { rs := strings.Split(ansi.Strip(a.screen()), "\n"); return rs[len(rs)-1] }
	if l := last(a); strings.HasPrefix(l, ">") || !strings.Contains(l, "tools:off") || !strings.Contains(l, "speaker:all") {
		t.Fatalf("normal last row %q", l)
	}
	a, _ = press(t, a, "i")
	if l := last(a); !strings.HasPrefix(l, "> ") {
		t.Fatalf("insert last row %q", l)
	}
	a, _ = press(t, a, "esc", ":")
	if l := last(a); !strings.HasPrefix(l, ":") {
		t.Fatalf(": last row %q", l)
	}
	checkFrame(t, a, a.w, a.h)
}

func TestSpeakerFilter(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	for _, n := range []string{"vic", "lib1", "vic", "lib2"} {
		next, _ := a.Update(LineMsg{Ch: "#a", L: model.Line{Nick: n, Text: "from " + n}})
		a = next.(App)
	}
	a, _ = press(t, a, "S")
	if got := strings.Join(a.pickSource(), ","); got != "(all),lib2,vic,lib1,me,a" { // speakers newest first, then the rest of the pane
		t.Fatalf("speakers %q", got)
	}
	a = typeText(t, a, "lib1")
	a, _ = press(t, a, "enter")
	v := ansi.Strip(a.screen())
	if a.speaker != "lib1" || !strings.Contains(v, "from lib1") || strings.Contains(v, "from vic") || !strings.Contains(v, "speaker:lib1") {
		t.Fatalf("filter %q:\n%s", a.speaker, v)
	}
	a, _ = press(t, a, "S", "enter") // (all) clears
	if a.speaker != "" || !strings.Contains(ansi.Strip(a.screen()), "from vic") {
		t.Fatalf("clear: %q", a.speaker)
	}
}

func TestResumeLastChannel(t *testing.T) {
	m, err := model.New(20)
	if err != nil {
		t.Fatal(err)
	}
	a := New(m, func(string, string) error { return nil }).WithResume("&r")
	next, _ := a.Update(SessionsMsg{{PID: 1, Name: "a"}})
	if a = next.(App); a.Channel() != "#a" { // not there yet: first channel
		t.Fatalf("before: %s", a.Channel())
	}
	next, _ = a.Update(RoomsMsg{"&r"})
	if a = next.(App); a.Channel() != "&r" {
		t.Fatalf("resume: %s", a.Channel())
	}
	a, _ = press(t, a, "ctrl+n")
	next, _ = a.Update(RoomsMsg{"&r"}) // NEG: only once
	if a = next.(App); a.Channel() == "&r" {
		t.Fatal("resumed twice")
	}
}

func TestSameAgentNameInTwoSessions(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil) // #a #b #c, focus #a
	for _, ch := range []string{"#a", "#b"} {
		next, _ := a.Update(AgentsMsg{Ch: ch, Agents: []model.Agent{{Nick: "driver-1"}}})
		a = next.(App)
	}
	if got := candidates("@driver", a.chanNames(), a.nickPool()); strings.Join(got, ",") != "@driver-1,@driver-1@a,@driver-1@b" {
		t.Fatalf("completion %q", got) // own plain first; other sessions only qualified
	}
	a, _ = press(t, a, "ctrl+t")
	var at []string
	for _, s := range a.pickSource() {
		if strings.HasPrefix(s, "@driver") {
			at = append(at, s)
		}
	}
	if strings.Join(at, ",") != "@driver-1,@driver-1@b" { // NEG: no second plain @driver-1 for #b
		t.Fatalf("picker %q", at)
	}
}

func TestYank(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	a, cmd := press(t, a, "y")
	if cmd != nil || !strings.Contains(ansi.Strip(a.screen()), "nothing to yank") { // NEG: no cursor line
		t.Fatalf("no line: cmd %v", cmd)
	}
	next, _ := a.Update(LineMsg{Ch: "#a", L: model.Line{Nick: "bob", Text: "copy\nme"}})
	a, _ = press(t, next.(App), "up")
	a, cmd = press(t, a, "y")
	if cmd == nil || fmt.Sprint(cmd()) != fmt.Sprint(tea.SetClipboard("copy\nme")()) {
		t.Fatalf("clipboard cmd %v", cmd)
	}
	if !strings.Contains(ansi.Strip(a.screen()), "yanked: copy me") {
		t.Fatal("no flash")
	}
	t0 := a.now()
	a.now = func() time.Time { return t0.Add(toastFor) } // the tick drops it after toastFor
	if next, _ := a.Update(tickMsg{}); strings.Contains(ansi.Strip(next.(App).screen()), "yanked") {
		t.Fatal("toast stayed after toastFor")
	}
}

func TestToastCard(t *testing.T) {
	var log []sent
	a := newApp(t, &log, nil)
	a = a.toast("Claude needs your attention", false).toast("send failed", true)
	v := checkFrame(t, a, 80, 24)
	rows := strings.Split(ansi.Strip(v), "\n")
	at := -1
	for i, r := range rows {
		if strings.Contains(r, "send failed") {
			at = i
		}
	}
	if at < 0 || !strings.Contains(rows[at-3], "needs your attention") || !strings.Contains(v, "91m╭") { // 91 = color 9, bright red
		t.Fatalf("cards:\n%s", ansi.Strip(v)) // newest lowest, the older one above it, error card red
	}
	if at != len(rows)-4 { // the lowest card ends just above the modeline
		t.Fatalf("card row %d of %d", at, len(rows))
	}
}

func TestSuggestFloats(t *testing.T) {
	var log []sent
	a, _ := press(t, newApp(t, &log, nil), "i")
	a = typeText(t, a, "/i")
	before, page := strings.Split(a.screen(), "\n"), a.page()
	a = typeText(t, a, "nv") // now there is a suggestion
	after := strings.Split(a.screen(), "\n")
	if a.page() != page || after[0] != before[0] || after[1] != before[1] { // NEG: no layout row moved
		t.Fatalf("layout moved: page %d -> %d", page, a.page())
	}
	s := ansi.Strip(strings.Join(after, "\n"))
	rows := strings.Split(s, "\n")
	if !strings.Contains(rows[len(rows)-4], "/invite") || !strings.HasPrefix(rows[len(rows)-1], "> /inv") {
		t.Fatalf("no floating box above the prompt:\n%s", s)
	}
	checkFrame(t, a, a.w, a.h)
}
