// Package model holds pure UI-independent state.
package model

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// Session is the registry data a channel name derives from.
type Session struct {
	PID       int
	Name, Cwd string
}

// ChanNames maps each pid to its channel name (D:223-227): lowercase, each
// run of space/comma/control -> "-"; empty name -> cwd basename; sessions
// whose names collide all become #<name>~<pid>.
func ChanNames(ss []Session) map[int]string {
	base := make(map[int]string, len(ss))
	count := map[string]int{}
	for _, s := range ss {
		n := slug(s.Name)
		if n == "" {
			n = slug(filepath.Base(s.Cwd))
		}
		if n == "" || n == "." || n == "/" {
			n = strconv.Itoa(s.PID)
		}
		base[s.PID] = n
		count[n]++
	}
	out := make(map[int]string, len(ss))
	for pid, n := range base {
		if count[n] > 1 {
			out[pid] = fmt.Sprintf("#%s~%d", n, pid)
		} else {
			out[pid] = "#" + n
		}
	}
	return out
}

// slug lowercases s, collapses separator runs and trims "-" from the ends.
func slug(s string) string {
	var b strings.Builder
	sep := false
	for _, r := range strings.ToLower(s) {
		if r == ' ' || r == ',' || unicode.IsControl(r) {
			if !sep {
				b.WriteByte('-')
			}
			sep = true
			continue
		}
		sep = false
		b.WriteRune(r)
	}
	return strings.Trim(b.String(), "-")
}
