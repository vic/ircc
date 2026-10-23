package ui

import (
	"strings"
	"testing"

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
	if v := ansi.Strip(next.(App).screen()); !strings.Contains(v, " vic") {
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
	a, _ = press(t, a, "c")
	if got := shown(); got != "hi" || strings.Contains(a.screen(), "thinking out loud") {
		t.Fatalf("c %q", got)
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

func TestRecentFirstAndTab(t *testing.T) {
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
	a, _ = press(t, a, "down") // #a -> #b
	a, _ = press(t, a, "tab")
	if cur(a) != "#a" {
		t.Fatalf("tab: %s", cur(a))
	}
	a, _ = press(t, a, "tab")
	if cur(a) != "#b" { // tab again flips back
		t.Fatalf("tab tab: %s", cur(a))
	}
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
