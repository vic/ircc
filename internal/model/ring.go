package model

import (
	"errors"
	"fmt"
)

// ErrBadCap means a ring capacity below 1.
var ErrBadCap = errors.New("model: ring capacity must be >= 1")

// Ring is a fixed-capacity buffer that drops the oldest item when full.
type Ring[T any] struct {
	buf      []T
	start, n int
}

// Cap is a validated ring capacity (>= 1 when made by NewCap).
type Cap int

// NewCap validates n as a ring capacity.
func NewCap(n int) (Cap, error) {
	if n < 1 {
		return 0, fmt.Errorf("%w: %d", ErrBadCap, n)
	}
	return Cap(n), nil
}

// RingOf returns an empty ring of capacity c; it cannot fail.
func RingOf[T any](c Cap) Ring[T] { return Ring[T]{buf: make([]T, int(c))} }

// NewRing returns an empty ring holding at most n items. Always build rings
// with NewRing or RingOf: the zero Ring has no storage and Push on it is a no-op.
func NewRing[T any](n int) (Ring[T], error) {
	c, err := NewCap(n)
	if err != nil {
		return Ring[T]{}, err
	}
	return RingOf[T](c), nil
}

// Push appends v, evicting the oldest item when full.
func (r *Ring[T]) Push(v T) {
	if len(r.buf) == 0 {
		return
	}
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = v
		r.n++
		return
	}
	r.buf[r.start] = v
	r.start = (r.start + 1) % len(r.buf)
}

// Len is the number of items held.
func (r *Ring[T]) Len() int { return r.n }

// All returns a copy of the items, oldest first.
func (r *Ring[T]) All() []T {
	out := make([]T, r.n)
	for i := range out {
		out[i] = r.buf[(r.start+i)%len(r.buf)]
	}
	return out
}

// clone returns a Ring with its own storage.
func (r Ring[T]) clone() Ring[T] {
	r.buf = append([]T(nil), r.buf...)
	return r
}
