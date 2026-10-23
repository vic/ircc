package model

import (
	"fmt"
	"time"

	"ircc/internal/peer"
)

// SessionInfo is the live registry data the modeline shows for one pid.
// PermMode is the single source of truth for bypass.
type SessionInfo struct {
	Status, PermMode string
}

// bypass reports whether the session is in bypass class; known is false when
// PermMode is not a mode peer.ClassOf accepts.
func (s SessionInfo) bypass() (bypass, known bool) {
	c, err := peer.ClassOf(s.PermMode)
	return err == nil && c == peer.Bypass, err == nil
}

// Modeline is the line above the input (D:323-327); Red when bypass is on or unknown.
type Modeline struct {
	Mode        string // NORMAL / INSERT, drawn as a badge
	Left, Right string
	Red         bool
}

// rateWindow is the span lines/min counts over (D:359).
const rateWindow = time.Minute

// Status builds the modeline for the focused channel. mode is the edit mode
// label. For a session channel info[PID] gives status and permission mode. For a
// room, members come from the channel and info[member.PID] gives each one's
// mode: bypass members are counted, members without info or with an unknown mode
// are counted as "?" and make the line Red (fail safe, D:349).
// lines/min counts only the lines still in the ring, so it undercounts above its cap.
// Unread totals every channel; now is injected so the function stays pure.
func Status(m Model, mode string, info map[int]SessionInfo, now time.Time) Modeline {
	unread := 0
	for _, c := range m.chans {
		unread += c.Unread
	}
	ml := Modeline{Mode: mode, Right: fmt.Sprintf("%d unread %s", unread, now.Format("15:04"))}
	c := m.Current()
	switch {
	case c.Name == "":
	case c.Kind == RoomChan:
		bypass, unknown := 0, 0
		seen := map[int]bool{} // teammates share the parent pid: count each pid once
		for _, mb := range c.Members {
			if seen[mb.PID] {
				continue
			}
			seen[mb.PID] = true
			s, ok := info[mb.PID]
			b, known := s.bypass()
			switch {
			case !ok || !known:
				unknown++
			case b:
				bypass++
			}
		}
		rate := 0
		for _, l := range c.Lines.All() {
			if d := now.Sub(l.At); d >= 0 && d < rateWindow {
				rate++
			}
		}
		ml.Left = fmt.Sprintf("%s %d members %d bypass %d ? %d/min", c.Name, len(c.Members), bypass, unknown, rate)
		ml.Red = bypass > 0 || unknown > 0
	default:
		st, pm := "?", "?" // untrusted registry text: only known modes print, the rest is cleaned
		s, ok := info[c.PID]
		if ok {
			st = clean(s.Status)
			if _, known := s.bypass(); known {
				pm = s.PermMode
			}
		}
		ml.Left = fmt.Sprintf("%s %s %s", c.Name, st, pm)
		b, known := s.bypass()
		ml.Red = b || !known // no info or unknown mode: fail safe
	}
	return ml
}
