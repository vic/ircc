package e2e_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"ircc/internal/registry"
)

// writeSession writes one registry file under <claude>/sessions.
func writeSession(t *testing.T, claude string, pid int, procStart, name string, mode os.FileMode, file string) {
	t.Helper()
	sock := filepath.Join(claude, "s", fmt.Sprintf("%d.sock", pid))
	body := fmt.Sprintf(`{"pid":%d,"kind":"interactive","status":"idle","name":%q,"cwd":"/w/proj","messagingSocketPath":%q,"procStart":%q}`,
		pid, name, sock, procStart)
	if err := os.WriteFile(filepath.Join(claude, "sessions", file), []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// spawnChild starts a live `sleep` and returns its pid and real procStart.
func spawnChild(t *testing.T) (pid int, start string) {
	t.Helper()
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Process.Kill(); c.Wait() })
	start, err := registry.ProcStart("/proc", c.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return c.Process.Pid, start
}

// fixture builds <claude>/sessions with the live test process (real procStart)
// and returns the registry entry the Scan must report. procRoot is the real
// /proc so the live row can be verified. Rows that must be dropped: a dead
// pid, and a live child whose procStart is wrong ("1", E5).
func fixture(t *testing.T) (claude, procRoot string, live registry.Entry) {
	t.Helper()
	claude = t.TempDir()
	procRoot = "/proc"
	if err := os.MkdirAll(filepath.Join(claude, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	self := os.Getpid()
	start, err := registry.ProcStart(procRoot, self)
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, claude, self, start, "Review Bot", 0o600, fmt.Sprintf("%d.json", self))
	live = registry.Entry{PID: self, Name: "Review Bot", Cwd: "/w/proj", ProcStart: start,
		SocketPath: filepath.Join(claude, "s", fmt.Sprintf("%d.sock", self))}

	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	dead := c.Process.Pid // reaped by Run
	writeSession(t, claude, dead, "5", "dead", 0o600, fmt.Sprintf("%d.json", dead))

	child, _ := spawnChild(t)
	writeSession(t, claude, child, "1", "mismatch", 0o600, fmt.Sprintf("%d.json", child))
	// A mode 0000 file is unreadable to non-root only: as root the row proves nothing, so skip it.
	if os.Geteuid() != 0 {
		writeSession(t, claude, self, start, "key", 0o000, fmt.Sprintf("%d.x.key", self))
	}
	return claude, procRoot, live
}
