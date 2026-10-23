package ui

import (
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
)

// Simple markdown coloring (vic 2026-10-22). Markers stay visible but dimmed, so
// widths never change: wrapping and scroll counts stay the plain-text ones.
var (
	codeSt   = fg("6")
	inlineRE = regexp.MustCompile("`[^`]+`|\\*\\*[^*]+\\*\\*|\\*[^*\\s][^*]*\\*|\\b_[^_\\s][^_]*_\\b")
	bulletRE = regexp.MustCompile(`^(\s*)([-*+]|\d+\.) `)
	headRE   = regexp.MustCompile(`^\s*#{1,6} `)
)

// fenced reports whether a row of text opens or closes a ``` code block.
func fenced(text string) bool { return strings.HasPrefix(strings.TrimSpace(text), "```") }

// markdown renders one row of message text in st. inCode: the row is inside a
// ``` block. Plain parts keep @mention colors.
func markdown(text string, st lipgloss.Style, inCode bool) string {
	switch {
	case fenced(text):
		return dimmed.Render(text)
	case inCode:
		return codeSt.Render(text)
	case headRE.MatchString(text):
		return title.Render(text)
	}
	var b strings.Builder
	if m := bulletRE.FindStringSubmatchIndex(text); m != nil {
		b.WriteString(st.Render(text[:m[4]]) + shortcut.Render(text[m[4]:m[5]]))
		text = text[m[5]:]
	}
	last := 0
	for _, m := range inlineRE.FindAllStringIndex(text, -1) {
		b.WriteString(mentions(text[last:m[0]], st))
		tok := text[m[0]:m[1]]
		n := 1 // marker width: ` * _
		inner := st
		switch {
		case tok[0] == '`':
			inner = codeSt
		case strings.HasPrefix(tok, "**"):
			n, inner = 2, st.Bold(true)
		default:
			inner = st.Italic(true)
		}
		b.WriteString(dimmed.Render(tok[:n]) + inner.Render(tok[n:len(tok)-n]) + dimmed.Render(tok[len(tok)-n:]))
		last = m[1]
	}
	b.WriteString(mentions(text[last:], st))
	return b.String()
}
