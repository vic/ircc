package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ircc/internal/registry"
)

// screen collects the frames the program renders, as plain text. The v2 renderer
// writes only changed cells, so the frames come from the model, not the byte stream.
type screen struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *screen) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }

// frames is a tea.WithFilter hook. App is a value: Update on a copy gives the frame
// the program shows after msg, with no side effect (returned Cmds are dropped).
func (s *screen) frames(m tea.Model, msg tea.Msg) tea.Msg {
	if next, _ := m.Update(msg); next != nil {
		s.Write([]byte(ansi.Strip(next.View().Content) + "\n"))
	}
	return msg
}

// waitFor fails unless want shows up in output written after mark, within 1s.
func (s *screen) waitFor(t *testing.T, mark int, want string) int {
	t.Helper()
	for end := time.Now().Add(time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		s.mu.Lock()
		out := s.b.String()
		s.mu.Unlock()
		if i := strings.Index(out[mark:], want); i >= 0 {
			return mark + i + len(want)
		}
	}
	s.mu.Lock()
	t.Logf("screen:\n%s", s.b.String())
	s.mu.Unlock()
	t.Fatalf("no %q on screen after byte %d", want, mark)
	return 0
}

// fakeSession registers a live sleep process named name in claude/sessions.
func fakeSession(t *testing.T, claude, socks, name string) {
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
	ej := fmt.Sprintf(`{"pid":%d,"sessionId":"sid-%s","cwd":"/w","name":%q,"kind":"interactive","status":"idle","messagingSocketPath":%q,"procStart":%q}`,
		c.Process.Pid, name, name, filepath.Join(socks, fmt.Sprintf("%d.sock", c.Process.Pid)), start)
	if err := os.WriteFile(filepath.Join(claude, "sessions", fmt.Sprintf("%d.json", c.Process.Pid)), []byte(ej), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestTUIKeys drives the whole program (runner, registry watch, ui) with Claude mocked
// by session files: keys go in through a pipe, frames come out as a string.
func TestTUIKeys(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "tui")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	claude, socks := filepath.Join(root, "c"), filepath.Join(root, "s")
	for _, d := range []string{filepath.Join(claude, "sessions"), socks} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fakeSession(t, claude, socks, "alpha")
	fakeSession(t, claude, socks, "beta")
	t.Setenv("IRCC_CLAUDE_DIR", claude)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("USER", "vic")

	in, keys := io.Pipe()
	out := &screen{}
	done := make(chan int, 1)
	go func() {
		done <- runTUI("", tea.WithInput(in), tea.WithOutput(io.Discard), tea.WithoutSignals(), tea.WithWindowSize(100, 30), tea.WithFilter(out.frames))
	}()
	type_ := func(s string) {
		if _, err := keys.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}

	at := out.waitFor(t, 0, "NORMAL  #alpha")
	type_("gt")
	at = out.waitFor(t, at, "NORMAL  #beta")
	type_("gT")
	at = out.waitFor(t, at, "NORMAL  #alpha")
	type_("i")
	at = out.waitFor(t, at, "INSERT  #alpha")
	// real terminal bytes: ctrl+n, ctrl+p, arrows, esc, then the : prompt
	for _, step := range []struct{ keys, want string }{
		{"\x0e", "INSERT  #beta"}, {"\x10", "INSERT  #alpha"}, {"\x1b", "NORMAL  #alpha"},
		{"\x0e", "NORMAL  #beta"}, {"\x10", "NORMAL  #alpha"},
		{"\x1b[A", "NORMAL  #alpha"}, // up selects a line (vic 2026-10-23), the channel stays
		{":", "send as is"}, {"\x1b", "NORMAL  #alpha"}, {"i/join &x\r", "INSERT  &x"},
	} {
		type_(step.keys)
		at = out.waitFor(t, at, step.want)
	}
	type_("\x03")
	select {
	case rc := <-done:
		if rc != 0 {
			t.Fatalf("rc %d", rc)
		}
	case <-time.After(time.Second):
		t.Fatal("ctrl+c did not quit")
	}
	if b, err := os.ReadFile(filepath.Join(root, "state", "ircc", "last")); err != nil || string(b) != "&x\n" { // the channel it quit on
		t.Fatalf("last channel %q %v", b, err)
	}
}
