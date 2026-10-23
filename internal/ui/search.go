package ui

import (
	"ircc/internal/model"
)

// Search finds the next line after from (back false) or before it (back true)
// whose nick or text fuzzy-matches q (q a subsequence, ignoring case; vic
// 2026-10-22), wrapping around (D:312).
// A from outside lines starts at the matching edge. The match at from is
// returned only after a full wrap.
func Search(lines []model.Line, q string, from int, back bool) (int, bool) {
	n := len(lines)
	if q == "" || n == 0 {
		return 0, false
	}
	step := 1
	if back {
		step = -1
	}
	if from < 0 || from >= n {
		if back {
			from = 0
		} else {
			from = n - 1
		}
	}

	for k := 1; k <= n; k++ {
		i := ((from+step*k)%n + n) % n
		l := lines[i]
		if _, ok := fuzzy(q, l.Nick); ok {
			return i, true
		}
		if _, ok := fuzzy(q, l.Text); ok {
			return i, true
		}
	}
	return 0, false
}
