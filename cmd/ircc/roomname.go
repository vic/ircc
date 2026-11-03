package main

import "ircc/internal/rooms"

// isRoom reports whether name is an open room. A SendMessage target is a room or a
// teammate; only an open room is hidden as a tool line.
func (r *runner) isRoom(name string) bool {
	if r.rt == nil {
		return false
	}
	n, err := rooms.ParseName(name)
	if err != nil {
		return false
	}
	_, ok := r.rt.room(n)
	return ok
}
