package ui

import (
	"strings"

	"ircc/internal/model"
)

// Search finds the next line after from (back false) or before it (back true)
// whose nick or text contains q, ignoring case, wrapping around (D:312).
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
	q = strings.ToLower(q)
	for k := 1; k <= n; k++ {
		i := ((from+step*k)%n + n) % n
		l := lines[i]
		if strings.Contains(strings.ToLower(l.Nick), q) || strings.Contains(strings.ToLower(l.Text), q) {
			return i, true
		}
	}
	return 0, false
}
