package ui

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ircc/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

func viewModel(t *testing.T) model.Model {
	t.Helper()
	m, err := model.New(20)
	if err != nil {
		t.Fatal(err)
	}
	m = m.WithSessions([]model.Session{{PID: 1, Name: "vix-05"}, {PID: 2, Name: "ren"}, {PID: 3, Name: "dnx3"}})
	for _, l := range []struct{ ch, nick, text string }{
		{"#vix-05", "vic", "which inputs do you pin?"},
		{"#vix-05", "vix-05", "none, flake.lock pins them"},
		{"#ren", "ren", "unread one"},
		{"#ren", "ren", "unread two"},
	} {
		var err error
		if m, err = m.AddLine(l.ch, model.Line{Nick: l.nick, Text: l.text}); err != nil {
			t.Fatal(err)
		}
	}
	return m.WithSessions([]model.Session{{PID: 1, Name: "vix-05"}, {PID: 2, Name: "ren"}}) // dnx3 is gone
}

var ml = model.Modeline{Mode: "NORMAL", Left: "#vix-05 idle default", Right: "2 unread 12:04"}

func TestViewGolden(t *testing.T) {
	got := ansi.Strip(View(viewModel(t), ml, "hello", 80, 10, 0, -1)) + "\n" // colors are tested apart
	const path = "testdata/view_80x10.golden"
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("golden mismatch:\n%s\nwant:\n%s", got, want)
	}
	rows := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(rows) != 10 {
		t.Fatalf("rows %d", len(rows))
	}
	for i, r := range rows {
		if w := lipgloss.Width(r); w != 80 {
			t.Errorf("row %d width %d", i, w)
		}
	}
}

func TestViewNarrowFallback(t *testing.T) {
	got := View(viewModel(t), ml, "x", 30, 8, 0, -1)
	if strings.Contains(got, "│") {
		t.Fatalf("panes shown at w=30:\n%s", got)
	}
	rows := strings.Split(got, "\n")
	if len(rows) != 8 || !strings.Contains(got, "which inputs") {
		t.Fatalf("fallback:\n%s", got)
	}
}

func TestViewDegenerateSizes(t *testing.T) { // NEG: no panic, no row wider than w
	m := viewModel(t)
	for _, c := range [][2]int{{0, 0}, {1, 1}, {80, 2}, {-5, 10}, {1, 3}, {39, 3}, {40, 3}} {
		for i, r := range strings.Split(View(m, ml, "x", c[0], c[1], 0, -1), "\n") {
			if lipgloss.Width(r) > max(c[0], 0) {
				t.Errorf("%v row %d width %d > %d: %q", c, i, lipgloss.Width(r), c[0], r)
			}
		}
	}
	empty, err := model.New(3)
	if err != nil {
		t.Fatal(err)
	}
	_ = View(empty, model.Modeline{}, "", 80, 10, 0, -1)
}

func TestViewRed(t *testing.T) {
	row := func(red bool) string {
		rows := strings.Split(View(viewModel(t), model.Modeline{Left: "L", Right: "R", Red: red}, "", 80, 10, 0, -1), "\n")
		return rows[len(rows)-2]
	}
	if !strings.Contains(row(true), "\x1b[") {
		t.Fatal("red modeline has no styling")
	}
	if row(false) == row(true) || !strings.Contains(row(false), "\x1b[") { // plain is the blue bar, bypass is red
		t.Fatal("bypass modeline looks like the plain one")
	}
}

func TestViewScrollOffset(t *testing.T) {
	m := viewModel(t) // #vix-05 has 2 lines
	at0 := View(m, ml, "", 80, 6, 0, -1)
	at1 := View(m, ml, "", 80, 6, 1, -1)
	// h=6: pane shows 2 lines. offset 1 ends one line earlier: only line 1 remains.
	if !strings.Contains(at0, "flake.lock pins them") || !strings.Contains(at0, "which inputs") {
		t.Fatalf("offset 0:\n%s", at0)
	}
	if strings.Contains(at1, "flake.lock pins them") || !strings.Contains(at1, "which inputs") {
		t.Fatalf("offset 1:\n%s", at1)
	}
	for _, off := range []int{-3, 2, 99} { // NEG: out of range clamps, no panic
		_ = View(m, ml, "", 80, 5, off, -1)
	}
}

func TestViewHitReversed(t *testing.T) {
	row := func(hit int) string { // the pane row of "none, flake.lock" (line 1)
		for _, r := range strings.Split(View(viewModel(t), ml, "", 80, 10, 0, hit), "\n") {
			if strings.Contains(r, "none, flake.lock") {
				return r
			}
		}
		t.Fatal("row not found")
		return ""
	}
	if !strings.Contains(row(1), hitNick("vix-05")) {
		t.Fatal("hit line is not highlighted")
	}
	for _, hit := range []int{-1, 0, 99} { // NEG: no hit, other line, out of range
		if strings.Contains(row(hit), hitNick("vix-05")) {
			t.Fatalf("hit %d reversed line 1", hit)
		}
	}
}

func TestViewBoxBorders(t *testing.T) {
	rows := strings.Split(ansi.Strip(View(viewModel(t), ml, "", 80, 10, 0, -1)), "\n")
	for i, r := range rows[:8] {
		rs := []rune(r)
		first, last := string(rs[0]), string(rs[len(rs)-1])
		want := map[int][2]string{0: {"┌", "┐"}, 7: {"└", "┘"}}[i]
		if want == [2]string{} {
			want = [2]string{"│", "│"}
		}
		if first != want[0] || last != want[1] {
			t.Errorf("row %d borders %s..%s want %v: %q", i, first, last, want, r)
		}
	}
	if strings.ContainsAny(rows[8]+rows[9], "│┌└") {
		t.Fatal("modeline or input inside the box")
	}
	// NEG: too short for the box (h < 5) falls back to the lines pane only
	if strings.ContainsAny(View(viewModel(t), ml, "", 80, 4, 0, -1), "│┌└") {
		t.Fatal("box drawn at h=4")
	}
}

func wrapModel(t *testing.T, texts ...string) model.Model {
	t.Helper()
	m, err := model.New(20)
	if err != nil {
		t.Fatal(err)
	}
	m = m.WithSessions([]model.Session{{PID: 1, Name: "a"}})
	for _, s := range texts {
		if m, err = m.AddLine("#a", model.Line{Nick: "bob", Text: s}); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func TestViewWrapsLongLines(t *testing.T) {
	long := "one two three four five six seven eight nine ten eleven twelve" // 2 rows at 39 columns
	got := ansi.Strip(View(wrapModel(t, long), ml, "", 80, 10, 0, -1))
	rows := strings.Split(got, "\n")
	var body []string
	for _, r := range rows[1:7] {
		if i := strings.Index(r[len("│"):], "│"); i >= 0 {
			body = append(body, strings.TrimSpace(strings.Split(r, "│")[2]))
		}
	}
	if len(body) < 2 || body[0] == "" || body[1] == "" || body[2] != "" {
		t.Fatalf("want 2 wrapped rows, got %q", body)
	}
	if !strings.HasPrefix(body[0], "<bob>") || strings.HasPrefix(body[1], "<bob>") {
		t.Fatalf("nick only on the first row: %q", body)
	}
	joined := strings.Join(strings.Fields(strings.Join(body, " ")), " ")
	if joined != "<bob> "+long { // nothing lost or cut
		t.Fatalf("text changed:\n%q\n%q", joined, long)
	}
	for _, r := range rows {
		if lipgloss.Width(r) != 80 {
			t.Errorf("row width %d: %q", lipgloss.Width(r), r)
		}
	}
}

func TestViewWrapCountsRowsForScrollAndHit(t *testing.T) {
	long := strings.Repeat("word ", 30) // wraps to 3+ rows at 80 cols
	m := wrapModel(t, "first", long, "last")
	// h=7 -> 3 pane rows: bottom = "last" plus the end of the wrapped line
	bottom := View(m, ml, "", 80, 7, 0, -1)
	if !strings.Contains(bottom, "last") || strings.Contains(bottom, "first") {
		t.Fatalf("bottom:\n%s", bottom)
	}
	// off counts wrapped rows: off 1 drops "last" from the bottom row
	if up := View(m, ml, "", 80, 7, 1, -1); strings.Contains(up, "last") {
		t.Fatalf("off=1 still shows last:\n%s", up)
	}
	// hit marks the <nick> on the first row of the wrapped line (h=9 keeps that row on screen)
	n := 0
	for _, r := range strings.Split(View(m, ml, "", 80, 9, 1, 1), "\n") {
		if strings.Contains(r, "word") && strings.Contains(r, hitNick("bob")) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("hit marked %d rows, want the nick row only (1)", n)
	}
}

func TestViewWrapsOnlyVisibleLines(t *testing.T) {
	m, err := model.New(1000)
	if err != nil {
		t.Fatal(err)
	}
	m = m.WithSessions([]model.Session{{PID: 1, Name: "a"}})
	for i := 0; i < 1000; i++ {
		if m, err = m.AddLine("#a", model.Line{Nick: "n", Text: fmt.Sprintf("line%04d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	cur := m.Current()
	for _, c := range []struct{ off, rows int }{{0, 6}, {5, 6}, {900, 6}} {
		calls := 0
		rows, _ := tailRows(cur.Lines.All(), c.rows, c.off, 50, func(l model.Line, w int) []string {
			calls++
			return wrapLine(l, w)
		})
		if calls > c.off+c.rows+1 {
			t.Errorf("off %d: %d wrap calls for %d rows needed", c.off, calls, c.off+c.rows)
		}
		newest := fmt.Sprintf("line%04d", 999-c.off)
		if len(rows) != c.rows || !strings.HasSuffix(rows[len(rows)-1], newest) {
			t.Errorf("off %d: bottom row is not %s: %q", c.off, newest, rows)
		}
	}
}

// hitNick is the <nick> of a selected line: the nick bold italic in its color, the brackets in the cursor color.
func hitNick(nick string) string {
	br := fg("13").Bold(true).Italic(true)
	return br.Render("<") + nickStyle(nick).Bold(true).Italic(true).Render(nick) + br.Render(">")
}
