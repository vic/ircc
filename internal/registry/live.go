package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ProcStart returns field 22 of procRoot/<pid>/stat.
func ProcStart(procRoot string, pid int) (string, error) { return statField(procRoot, pid, 22) }

// ParentPID returns field 4 (ppid) of procRoot/<pid>/stat (the I3 parent walk).
func ParentPID(procRoot string, pid int) (int, error) {
	f, err := statField(procRoot, pid, 4)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(f)
}

// statField returns field n (n >= 3) of procRoot/<pid>/stat. comm (field 2)
// may hold spaces and parens, so fields are split after the last ')'.
func statField(procRoot string, pid, n int) (string, error) {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", fmt.Errorf("read stat of pid %d: %w", pid, err)
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return "", fmt.Errorf("stat of pid %d: no comm terminator", pid)
	}
	f := strings.Fields(s[i+1:]) // f[0] is field 3
	idx := n - 3
	if len(f) <= idx {
		return "", fmt.Errorf("stat of pid %d: %d fields after comm, need %d", pid, len(f), idx+1)
	}
	return f[idx], nil
}

// Alive reports whether e.PID exists and its start time equals e.ProcStart,
// guarding against pid reuse. An unreadable or missing stat reads as dead
// (fails closed); EPERM from kill counts as not ours, so also dead. pid <= 0
// is never a process (kill would signal a group).
func Alive(e Entry, procRoot string) bool {
	pid := e.PID
	if pid <= 0 {
		return false
	}
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	got, err := ProcStart(procRoot, pid)
	return err == nil && got == e.ProcStart
}
