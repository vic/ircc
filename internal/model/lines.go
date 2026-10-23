package model

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

var (
	// ErrUnknownChannel means no channel has the given name.
	ErrUnknownChannel = errors.New("model: unknown channel")
	// ErrBadIndex means a channel index outside the list.
	ErrBadIndex = errors.New("model: channel index out of range")
)

// AddLine appends l to channel ch, counting it unread unless ch is focused.
// The returned Model gives ch its own Ring; m is never modified.
// shortcut: clone is O(cap) per line, upgrade to persistent ring if cap/rate grows
func (m Model) AddLine(ch string, l Line) (Model, error) { return m.add(ch, l, true) }

// AddHistory appends l like AddLine but as history: no unread count, and the
// channel keeps its place.
func (m Model) AddHistory(ch string, l Line) (Model, error) { return m.add(ch, l, false) }

func (m Model) add(ch string, l Line, live bool) (Model, error) {
	for i, c := range m.chans {
		if c.Name != ch {
			continue
		}
		l.Nick, l.Text = clean(l.Nick), cleanText(l.Text)
		c.Lines = c.Lines.clone()
		c.Lines.Push(l)
		if !live {
			return m.withChan(i, c), nil
		}
		if i != m.cur {
			c.Unread++
		}
		return m.withChan(i, c).toFront(i), nil
	}
	return m, fmt.Errorf("%w: %s", ErrUnknownChannel, ch)
}

// Focus makes channel i current and clears its unread count.
func (m Model) Focus(i int) (Model, error) {
	if i < 0 || i >= len(m.chans) {
		return m, fmt.Errorf("%w: %d", ErrBadIndex, i)
	}
	c := m.chans[i]
	c.Unread = 0
	n := m.withChan(i, c)
	n.cur = i
	return n, nil
}

// Current returns the focused channel, or the zero Channel when there are none.
func (m Model) Current() Channel {
	if m.cur < 0 || m.cur >= len(m.chans) {
		return Channel{}
	}
	return m.chans[m.cur]
}

func (m Model) withChan(i int, c Channel) Model {
	m.chans = append([]Channel(nil), m.chans...)
	m.chans[i] = c
	return m
}

// clean makes untrusted text safe to draw: invalid UTF-8 becomes U+FFFD, tab and
// line breaks become spaces, and every other control (C0, DEL, C1: ESC, BEL, CSI)
// and format char (bidi override, zero width) is dropped. Applied at the write
// site so no view can render a terminal escape from a peer line.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			return ' '
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
}

// toFront moves channel i to index 0 (most recent first) and keeps focus on
// the same channel.
func (m Model) toFront(i int) Model {
	c := m.chans[i]
	m.chans = append([]Channel{c}, slices.Delete(slices.Clone(m.chans), i, i+1)...)
	switch {
	case m.cur == i:
		m.cur = 0
	case m.cur < i:
		m.cur++
	}
	return m
}

// cleanText is clean for message text: it keeps line breaks and indentation (tab =
// 4 spaces) so agent replies keep their layout; other control runes still go.
func cleanText(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\t", "    ")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = clean(l)
	}
	return strings.Join(lines, "\n")
}
