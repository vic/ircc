package ui

import (
	"testing"

	"ircc/internal/model"
)

func TestSearch(t *testing.T) {
	ls := []model.Line{
		{Nick: "ren", Text: "alpha"},
		{Nick: "bob", Text: "Beta needle"},
		{Nick: "ren", Text: "gamma"},
		{Nick: "needle", Text: "delta"},
		{Nick: "x", Text: "needle again"},
	}
	for _, c := range []struct {
		name  string
		lines []model.Line
		q     string
		from  int
		back  bool
		want  int
		ok    bool
	}{
		{"next skips current", ls, "needle", 1, false, 3, true},
		{"next wraps", ls, "needle", 4, false, 1, true},
		{"prev", ls, "needle", 3, true, 1, true},
		{"prev wraps", ls, "needle", 1, true, 4, true},
		{"case-insensitive", ls, "NEEDLE", 0, false, 1, true},
		{"only match is current", ls, "alpha", 0, false, 0, true},
		{"from -1 starts at top", ls, "alpha", -1, false, 0, true},
		{"from past end, prev", ls, "needle", 99, true, 4, true},
		{"fuzzy", ls, "ndl ag", 0, false, 4, true}, // n-d-l and a-g only in "needle again"
		{"no match", ls, "zzz", 0, false, 0, false},
		{"NEG empty query", ls, "", 0, false, 0, false},
		{"NEG no lines", nil, "a", 0, false, 0, false},
	} {
		got, ok := Search(c.lines, c.q, c.from, c.back)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got (%d,%v) want (%d,%v)", c.name, got, ok, c.want, c.ok)
		}
	}
}
