package model

// WithRooms makes the room (&) channels exactly names. Kept channels keep their
// place (most recent first, vic 2026-10-22, was D:327 # before &), new rooms go last.
// Rooms that stay keep scrollback and members; duplicates collapse; focus stays
// on the same channel by name, else falls back to the first channel.
func (m Model) WithRooms(names []string) Model {
	want := map[string]bool{}
	for _, raw := range names {
		want[clean(raw)] = true
	}
	focus := m.Current().Name
	var out []Channel // existing order kept (most recent first); new rooms go last
	for _, c := range m.chans {
		if c.Kind == SessionChan || want[c.Name] {
			out = append(out, c)
			delete(want, c.Name)
		}
	}
	for _, raw := range names {
		if n := clean(raw); want[n] {
			out = append(out, Channel{Name: n, Kind: RoomChan, Lines: RingOf[Line](m.cap)})
			delete(want, n)
		}
	}
	n := m
	n.chans, n.cur = out, 0
	for i, c := range out {
		if c.Name == focus {
			n.cur = i
		}
	}
	return n
}
