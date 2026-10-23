package ui

import (
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ircc/internal/model"
)

const (
	chanW    = 15 // channel list pane, inner width
	nickW    = 10 // nick pane, inner width
	minWide  = 40 // below this width (or h < minWideH) View shows the lines pane only
	minWideH = 5
	nickCol  = 11 // "<nick>" column in the lines pane
)

// Styles are named by role, one table; ANSI 16 colors only, so the terminal theme
// picks the shades.
var (
	cell        = lipgloss.NewStyle().Inline(true)
	dimmed      = fg("8")
	border      = dimmed
	focusBorder = fg("13").Bold(true) // the pane that takes keys
	faint       = dimmed
	title       = fg("5").Bold(true)
	shortcut    = fg("5")
	prompt      = fg("6").Bold(true)
	unread      = fg("3").Bold(true)
	roomName    = fg("13")
	alert       = fg("9").Bold(true)
	current     = cell.Background(lipgloss.Color("236"))                                           // selected line and search hit: dark, not white
	cursor      = cell.Background(lipgloss.Color("13"))                                            // the input cursor: the focused pane border color
	selected    = cell.Bold(true).Background(lipgloss.Color("8")).Foreground(lipgloss.Color("15")) // current channel
	helpHead    = fg("2").Bold(true)
	// nickColors leaves out red (bypass) and gray (dim lines).
	nickColors = []string{"2", "3", "4", "5", "6", "10", "11", "12", "13", "14"}
)

func fg(c string) lipgloss.Style { return cell.Foreground(lipgloss.Color(c)) }

// badge is the mode block at the left of the modeline: black on a mode color, red in bypass.
func badge(mode string, bypass bool) string {
	bg := "5"
	if mode == "INSERT" {
		bg = "2"
	}
	if bypass {
		bg = "9"
	}
	return cell.Bold(true).Background(lipgloss.Color(bg)).Foreground(lipgloss.Color("0")).Render(" " + mode + " ") // Inline drops Padding
}

// keyHint is "key desc" pairs: keys in the shortcut color, descriptions dimmed.
func keyHint(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, shortcut.Render(pairs[i])+" "+dimmed.Render(pairs[i+1]))
	}
	return strings.Join(parts, "  ")
}

// nickStyle gives each nick a stable color.
func nickStyle(nick string) lipgloss.Style {
	h := fnv.New32a()
	h.Write([]byte(nick))
	return fg(nickColors[h.Sum32()%uint32(len(nickColors))])
}

// fit pads or truncates s to exactly n columns.
func fit(st lipgloss.Style, s string, n int) string {
	return st.Width(n).MaxWidth(n).Render(strings.ReplaceAll(s, "\n", " "))
}

func wide(w, h int) bool { return w >= minWide && h >= minWideH }

// paneRows is how many scrollback rows View shows in a w x h screen with in input rows.
func paneRows(w, h, in int) int {
	if wide(w, h) {
		return max(h-3-in, 1) // top and bottom border, modeline, input
	}
	return max(h-2-in, 1) // title, modeline, input
}

// inputRows is the input row text (with its prompt) wrapped to w columns: the last rows, at most a third of h,
// so the end being typed stays visible.
func inputRows(input string, w, h int) []string {
	rs := strings.Split(lipgloss.NewStyle().Width(max(w, 1)).Render(input), "\n")
	if k := max(h/3, 1); len(rs) > k {
		rs = rs[len(rs)-k:]
	}
	return rs
}

// seg is a pane title in a top border: " title ───" filled to n columns.
func seg(name string, n int, line lipgloss.Style) string {
	t := " " + name + " "
	if lipgloss.Width(t) >= n {
		return fit(title, t, n)
	}
	return title.Render(t) + line.Render(strings.Repeat("─", n-lipgloss.Width(t)))
}

// lineRow is scrollback row i in n columns: the search hit reversed, else the
// nick in its color, dim or tool text gray, chat text with markdown colors.
// code[i]: the row is inside a ``` block of its line.
func lineRow(rows []string, of []int, shown []model.Line, hit, i, n int, lead string, code []bool) string {
	if i >= len(rows) {
		return fit(cell, "", n)
	}
	if of[i] == hit {
		return fit(current, lead+rows[i], n)
	}
	l, r := shown[of[i]], rows[i]
	plain := l.Dim || l.Kind == model.Tool
	st := cell
	if plain {
		st = faint
	}
	text := func(s string) string {
		if plain {
			return mentions(s, st)
		}
		pad := len(s) - len(strings.TrimLeft(s, " ")) // indentation stays unstyled
		return s[:pad] + markdown(s[pad:], st, code[i])
	}
	if s := stampRE.FindString(r); s != "" {
		lead, r = lead+dimmed.Render(s), r[len(s):]
	}
	nk := "<" + l.Nick + ">"
	if after, ok := strings.CutPrefix(r, nk); ok {

		return fit(cell, lead+nickStyle(l.Nick).Render(nk)+text(after), n)
	}
	return fit(cell, lead+text(r), n)
}

// codeRows marks the rows inside a ``` block; the fence state restarts per line.
func codeRows(rows []string, of []int, shown []model.Line) []bool {
	out := make([]bool, len(rows))
	in := false
	for i, r := range rows {
		if i == 0 || of[i] != of[i-1] {
			in = false
		}
		r = strings.TrimPrefix(r[len(stampRE.FindString(r)):], "<"+shown[of[i]].Nick+">")
		if fenced(r) {
			in = !in
			continue
		}
		out[i] = in
	}
	return out
}

// View renders the box layout of DESIGN D:290-299: channels, lines and nicks
// panes with borders, then the modeline and the input line, in w columns and h
// rows. h < 3 or w < 1 yields "". off scrolls the lines pane back: it shows the
// lines ending off before the newest (clamped, D:304). hit is the index of the
// search hit in the current channel (-1 none); it is drawn reversed.
// Channel markers: ● current, ○ gone, else the unread count.
func View(m model.Model, ml model.Modeline, input string, w, h, off, hit int) string {
	cur := m.Current()
	var names []string
	for _, c := range m.Channels() {
		names = append(names, c.Name)
	}
	return render(m, ml, "> "+input, w, h, off, hit, sidePanes{chans: names, nicks: memberNicks(cur)}, cur.Lines.All(), wrapLine, nil, "")
}

// render is View with the nick pane given as nicks and the shown lines of the current channel.
func render(m model.Model, ml model.Modeline, input string, w, h, off, hit int, p sidePanes, shown []model.Line, wrap wrapFunc, sugg []string, hint string) string {
	if w < 1 || h < 3 {
		return ""
	}
	cur := m.Current()
	ir := append(sugg, inputRows(input, w, h)...) // the suggestion row sits right above the input
	n := paneRows(w, h, len(ir))
	lines, of := tailRows(shown, n, off, textWidth(w, h), wrap)
	code := codeRows(lines, of, shown)

	var rows []string
	if !wide(w, h) {
		rows = append(rows, fit(title, cur.Name, w))
		for i := 0; i < n; i++ {
			rows = append(rows, lineRow(lines, of, shown, hit, i, w, "", code))
		}
	} else {
		byName := map[string]model.Channel{}
		for _, c := range m.Channels() {
			byName[c.Name] = c
		}
		var chans []model.Channel
		for _, n := range p.chans {
			chans = append(chans, byName[n])
		}
		mid := w - chanW - nickW - 4 // four border columns
		// The focused pane's borders are drawn in focusBorder (vic 2026-10-23). Pane k
		// (0 channels, 1 chat, 2 nicks) owns vertical borders k and k+1 and segment k.
		pane := 1
		switch p.focus {
		case focusChans:
			pane = 0
		case focusNicks:
			pane = 2
		}
		edge := func(k int, s string) string { // vertical border k, or a corner on it
			if k == pane || k == pane+1 {
				return focusBorder.Render(s)
			}
			return border.Render(s)
		}
		line := func(k int) lipgloss.Style {
			if k == pane {
				return focusBorder
			}
			return border
		}
		rows = append(rows, edge(0, "┌")+seg(p.title("channels", focusChans), chanW, line(0))+edge(1, "┬")+
			seg(cur.Name, mid, line(1))+edge(2, "┬")+seg(p.title("nicks", focusNicks), nickW, line(2))+edge(3, "┐"))
		for i := 0; i < n; i++ {
			nk := at(p.nicks, i)
			if nk != "" { // an empty Render still emits escapes
				mark, name := "", nk
				for _, sm := range statusMarks {
					if rest, ok := strings.CutPrefix(nk, sm); ok {
						mark, name = sm, rest
					}
				}
				nk = mark + nickStyle(name).Render(name)
			}
			cc, nc := chanCell(chans, cur.Name, i), fit(cell, " "+nk, nickW)
			if p.focus == focusChans && i == p.sel && i < len(chans) {
				cc = fit(current, " "+chans[i].Name, chanW)
			}
			if p.focus == focusNicks && i == p.sel && i < len(p.nicks) {
				nc = fit(current, " "+p.nicks[i], nickW)
			}
			rows = append(rows, edge(0, "│")+cc+edge(1, "│")+lineRow(lines, of, shown, hit, i, mid, " ", code)+
				edge(2, "│")+nc+edge(3, "│"))
		}
		rows = append(rows, edge(0, "└")+line(0).Render(strings.Repeat("─", chanW))+edge(1, "┴")+line(1).Render(strings.Repeat("─", mid))+
			edge(2, "┴")+line(2).Render(strings.Repeat("─", nickW))+edge(3, "┘"))
	}

	left, lst := " ", cell
	if ml.Mode != "" {
		left = badge(ml.Mode, ml.Red) + " "
	}
	if ml.Red {
		lst = alert
	}
	left += lst.Render(ml.Left)
	if hint != "" { // keys for this mode, between the status and the clock (vic 2026-10-22)
		left += "   " + hint
	}
	right := dimmed.Render(ml.Right + " ")
	rows = append(rows, fit(cell, fit(cell, left, max(w-lipgloss.Width(right), 0))+right, w))
	for _, r := range ir {
		if after, ok := strings.CutPrefix(r, ">"); ok {
			r = prompt.Render(">") + after
		}
		rows = append(rows, fit(cell, r, w))
	}
	return strings.Join(rows, "\n")
}

func chanCell(chans []model.Channel, curName string, i int) string {
	if i >= len(chans) {
		return fit(cell, "", chanW)
	}
	c := chans[i]
	marker, markSt, nameSt := "", cell, nickStyle(strings.TrimPrefix(c.Name, "#")) // a session is its main agent nick
	if c.Kind == model.RoomChan {
		nameSt = roomName
	}
	switch {
	case c.Gone:
		marker, markSt, nameSt = "○", faint, faint
	case c.Name == curName:
		marker = "●"
	case c.Unread > 0:
		marker, markSt = strconv.Itoa(c.Unread), unread
	}
	if c.Name == curName { // plain text: inner colors would end the background early
		return fit(selected, " "+fit(cell, c.Name, 11)+fit(cell, marker, 3), chanW)
	}
	return fit(cell, " "+fit(nameSt, c.Name, 11)+fit(markSt, marker, 3), chanW)
}

// textWidth is the width of the scrollback text in a w x h screen.
func textWidth(w, h int) int {
	if wide(w, h) {
		return w - chanW - nickW - 4 - 1 // borders and the leading space
	}
	return w
}

// wrapLine word-wraps l to width columns: "<nick>" on the first row, the text
// aligned under itself on the rows after it.
func wrapLine(l model.Line, width int) []string {
	nk := "<" + l.Nick + ">"
	nk += strings.Repeat(" ", max(nickCol-lipgloss.Width(nk), 1))
	bw := max(width-lipgloss.Width(nk), 1)
	var out []string
	for i, r := range strings.Split(lipgloss.NewStyle().Width(bw).Render(l.Text), "\n") {
		r = strings.TrimRight(r, " ")
		if i == 0 {
			out = append(out, nk+r)
		} else {
			out = append(out, strings.Repeat(" ", lipgloss.Width(nk))+r)
		}
	}
	return out
}

// rowCount is how many rows c's scrollback takes at width.
func rowCount(ls []model.Line, width int, wrap wrapFunc) int {
	n := 0
	for _, l := range ls {
		n += len(wrap(l, width))
	}
	return n
}

// tailRows returns the n rows ending off rows before the newest and, per row,
// the index of the line it belongs to. It wraps lines newest-first and stops
// once off+n rows are collected, so cost follows the screen, not the ring.
func tailRows(all []model.Line, n, off, width int, wrap func(model.Line, int) []string) ([]string, []int) {
	off = max(off, 0)
	var rev []string // rows, newest first
	var revOf []int
	for i := len(all) - 1; i >= 0 && len(rev) < off+n; i-- {
		rs := wrap(all[i], width)
		for j := len(rs) - 1; j >= 0; j-- {
			rev, revOf = append(rev, rs[j]), append(revOf, i)
		}
	}
	rows := make([]string, 0, n)
	of := make([]int, 0, n)
	for k := min(off+n, len(rev)) - 1; k >= off; k-- {
		rows, of = append(rows, rev[k]), append(of, revOf[k])
	}
	return rows, of
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}

func memberNicks(c model.Channel) []string {
	out := make([]string, len(c.Members))
	for i, m := range c.Members {
		out[i] = m.Nick
	}
	return out
}

// chanNicks is the nick pane of c (D:217): user, then the session's main agent and
// its subagents
// (D:229 nick = channel name without "#") or the room's members.
func chanNicks(c model.Channel, user string) []string {
	if c.Kind == model.SessionChan {
		out := []string{user, strings.TrimPrefix(c.Name, "#")}
		for _, ag := range c.Agents {
			out = append(out, ag.Nick)
		}
		return out
	}
	return append([]string{user}, memberNicks(c)...)
}

// helpView is the help box centered in exactly w x h cells.
func helpView(w, h, off int) string {
	if w < 1 || h < 1 {
		return ""
	}
	box := strings.Split(lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1).Render(helpText()), "\n")
	if len(box) <= h {
		rows := strings.Split(lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, strings.Join(box, "\n")), "\n")[:h]
		for i, r := range rows {
			rows[i] = fit(cell, r, w)
		}
		return strings.Join(rows, "\n")
	}
	off = helpOff(len(box), h, off) // taller than the screen: a window that scrolls
	rows := box[off : off+h]
	for i, r := range rows {
		rows[i] = fit(cell, lipgloss.PlaceHorizontal(w, lipgloss.Center, r), w)
	}
	return strings.Join(rows, "\n")
}

// helpOff clamps a help scroll offset for a box of n rows in h rows.
func helpOff(n, h, off int) int { return min(max(off, 0), max(n-h, 0)) }

// helpRows is how many rows the help box has.
func helpRows() int { return strings.Count(helpText(), "\n") + 3 } // text rows + top and bottom border

// helpSections is the ? popup: a heading, then key and description pairs.
var helpSections = []struct {
	head string
	keys [][2]string
}{
	{"NORMAL", [][2]string{
		{"up down", "line cursor"},
		{"ctrl+n ctrl+p", "next / previous channel (also gt gT, {n}gt)"},
		{"tab", "focus channels, then nicks: type to filter, enter joins / mentions"},
		{"ctrl+t", "fuzzy picker: channels, and @nicks to mention"},
		{"wheel j k", "scroll"},
		{"J K", "next / previous speaker change"},
		{"S", "show only one speaker (fuzzy pick; (all) clears)"},
		{"y", "yank (copy) the cursor line"},
		{"shift+drag", "select text (the terminal does it; ircc takes plain drags)"},
		{"ctrl+d ctrl+u", "half page down / up"},
		{"gg G", "top / bottom"},
		{"/ n N", "search, next hit, previous hit"},
		{"i a o", "type a message"},
		{"A", "reply to the selected line (quoted), else type a message"},
		{"I", "invite someone to this room (fuzzy pick)"},
		{"O", "type a reply to the last @mentioned nick"},
		{":", "send a line as is to the session (e.g. /compact)"},
		{"t", "show / hide tool lines"},
		{"m", "mentions only (direct lines) / all lines"},
		{"s", "show / hide times (15:04)"},
	}},
	{"INSERT", [][2]string{
		{"enter", "send to the channel"},
		{"esc", "back to NORMAL (up down still switch channel)"},
		{"/cmd", "a line that starts with / is an ircc command"},
	}},
	{"COMMANDS", [][2]string{
		{"/join &room", "open or create a room"},
		{"/invite nick &room", "add a session; its nick is its channel without #"},
		{"/kick nick", "remove a member"},
		{"/part /rm", "hide / remove a room (/rm = /close)"},
		{"/topic /names /who", "room topic, members"},
		{"/msg nick text", "one line to one agent"},
		{"/help", "this help (same as ?)"},
		{"/quit", "quit (also ctrl+c)"},
	}},
}

// helpText renders helpSections with the key column aligned.
func helpText() string {
	rows := []string{title.Render("ircc") + dimmed.Render("  one # channel per Claude session, & rooms join them"), ""}
	for _, s := range helpSections {
		rows = append(rows, helpHead.Render(s.head))
		for _, k := range s.keys {
			rows = append(rows, "  "+shortcut.Width(20).Render(k[0])+k[1])
		}
		rows = append(rows, "")
	}
	return strings.Join(append(rows, keyHint("ctrl+c", "quit", "any key", "close help")), "\n")
}

// mentionRE is an @nick in text (D:258); teammate nicks carry @session.
var mentionRE = regexp.MustCompile(`@[A-Za-z0-9_.-]+(@[A-Za-z0-9_.-]+)?`)

// mentions renders s in st with each @nick in that nick's color, bold.
func mentions(s string, st lipgloss.Style) string {
	var b strings.Builder
	last := 0
	for _, m := range mentionRE.FindAllStringIndex(s, -1) {
		if m[0] > last {
			b.WriteString(st.Render(s[last:m[0]]))
		}
		b.WriteString(nickStyle(s[m[0]+1 : m[1]]).Bold(true).Render(s[m[0]:m[1]]))
		last = m[1]
	}
	if last < len(s) {
		b.WriteString(st.Render(s[last:]))
	}
	return b.String()
}

// wrapFunc wraps one line to rows of a width: wrapLine, or wrapStamped with s on.
type wrapFunc func(model.Line, int) []string

// stampRE is the "15:04 " that wrapStamped puts before a nick.
var stampRE = regexp.MustCompile(`^\d\d:\d\d `)

// wrapStamped is wrapLine after a short "15:04 " time; later rows are indented under it.
func wrapStamped(l model.Line, width int) []string {
	st := l.At.Format("15:04") + " "
	rs := wrapLine(l, max(width-len(st), 1))
	for i := range rs {
		if i == 0 {
			rs[i] = st + rs[i]
		} else {
			rs[i] = strings.Repeat(" ", len(st)) + rs[i]
		}
	}
	return rs
}

// sidePanes is what the channel and nick panes show: rows in order (filtered when
// focused), which pane has focus, its selected row and the query typed in it.
type sidePanes struct {
	chans, nicks []string
	focus        paneFocus
	sel          int
	q            string
}

// title is a side pane's title: "▸name query" while it has focus.
func (p sidePanes) title(name string, f paneFocus) string {
	if p.focus != f {
		return name
	}
	return strings.TrimSpace("▸" + name + " " + p.q)
}

// toastCards is how many toasts show at once (the newest).
const toastCards = 3

// overlayToasts draws the newest toasts as cards at the right edge of screen, the
// lowest card's bottom on row bottom (vic 2026-10-23, after jjui flash messages).
func overlayToasts(screen string, w, bottom int, ts []toast) string {
	if len(ts) == 0 || w < minWide {
		return screen
	}
	rows := strings.Split(screen, "\n")
	maxW := max(w/2, 20)
	for i := len(ts) - 1; i >= max(len(ts)-toastCards, 0) && bottom >= 0; i-- {
		color := lipgloss.Color("2")
		if ts[i].err {
			color = lipgloss.Color("9")
		}
		card := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(color).Padding(0, 1).
			Width(min(lipgloss.Width(ts[i].text)+4, maxW)).Render(ts[i].text)
		cl := strings.Split(card, "\n")
		cw := lipgloss.Width(cl[0])
		x := max(w-cw-1, 0) // one column from the right edge
		for j := len(cl) - 1; j >= 0 && bottom >= 0; j-- {
			if bottom < len(rows) {
				rows[bottom] = ansi.Cut(rows[bottom], 0, x) + cl[j] + ansi.Cut(rows[bottom], x+cw, w)
			}
			bottom--
		}
	}
	return strings.Join(rows, "\n")
}

// overlaySuggest draws the completion row in a small box over the screen, its
// bottom border on row bottom, from column 1: it floats above the prompt
// (vic 2026-10-23), so no layout row moves.
func overlaySuggest(screen string, w, bottom int, sugg []string) string {
	if len(sugg) == 0 || bottom < 2 {
		return screen
	}
	box := strings.Split(lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("5")).
		Padding(0, 1).Render(strings.Join(sugg, "\n")), "\n")
	rows := strings.Split(screen, "\n")
	bw := lipgloss.Width(box[0])
	for j := len(box) - 1; j >= 0 && bottom >= 0; j-- {
		if bottom < len(rows) {
			rows[bottom] = ansi.Cut(rows[bottom], 0, 1) + box[j] + ansi.Cut(rows[bottom], 1+bw, w)
		}
		bottom--
	}
	return strings.Join(rows, "\n")
}
