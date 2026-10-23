package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"ircc/internal/model"
	"ircc/internal/peer"
	"ircc/internal/registry"
	"ircc/internal/rooms"
	"ircc/internal/transcript"
	"ircc/internal/ui"
)

type sink struct {
	mu   sync.Mutex
	msgs []tea.Msg
}

func (s *sink) emit(m tea.Msg) { s.mu.Lock(); s.msgs = append(s.msgs, m); s.mu.Unlock() }

func (s *sink) take() []tea.Msg {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.msgs
	s.msgs = nil
	return m
}

// fixture: a fake claude dir with one live session (a child `sleep`), its
// transcript and a test listener standing in for the session socket.
type fix struct {
	claude, transcript string
	pid                int
	target             *peer.Listener
}

func newFix(t *testing.T) fix {
	t.Helper()
	claude := t.TempDir()
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Process.Kill(); c.Wait() })
	start, err := registry.ProcStart("/proc", c.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	socks, err := os.MkdirTemp("", "ir")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socks) })
	sock := filepath.Join(socks, fmt.Sprintf("%d.sock", c.Process.Pid))
	l, err := peer.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(5*time.Second, func() { l.Close() })
	t.Cleanup(func() { timer.Stop(); l.Close() })
	if err := os.MkdirAll(filepath.Join(claude, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	ej := fmt.Sprintf(`{"pid":%d,"sessionId":"sid1","cwd":"/w","name":"alpha","kind":"interactive","status":"busy","messagingSocketPath":%q,"procStart":%q,"pidDomain":"dom"}`,
		c.Process.Pid, sock, start)
	if err := os.WriteFile(filepath.Join(claude, "sessions", fmt.Sprintf("%d.json", c.Process.Pid)), []byte(ej), 0o600); err != nil {
		t.Fatal(err)
	}
	tdir := filepath.Join(claude, "projects", "p")
	if err := os.MkdirAll(tdir, 0o700); err != nil {
		t.Fatal(err)
	}
	tp := filepath.Join(tdir, "sid1.jsonl")
	if err := os.WriteFile(tp, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return fix{claude, tp, c.Process.Pid, l}
}

func appendLine(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestRunProducers(t *testing.T) {
	f := newFix(t)
	s := &sink{}
	r := newRunner(f.claude, "/proc", "vic", s.emit)
	defer r.close()

	es, errs := registry.Scan(filepath.Join(f.claude, "sessions"), "/proc")
	if len(es) != 1 || len(errs) != 0 {
		t.Fatalf("scan: %v %v", es, errs)
	}
	r.onSnapshot(registry.Snapshot{Entries: es})
	var sess ui.SessionsMsg
	for _, m := range s.take() {
		if sm, ok := m.(ui.SessionsMsg); ok {
			sess = sm
		}
	}
	if len(sess) != 1 || sess[0].PID != f.pid || sess[0].Name != "alpha" {
		t.Fatalf("SessionsMsg: %+v", sess)
	}

	// Fail closed: no permissionMode seen yet.
	if err := r.send("#alpha", "hi"); err == nil {
		t.Fatal("send before mode known: want error")
	}

	// A garbled line does not stop the tail; blocks render per DESIGN D:276-286.
	appendLine(t, f.transcript, "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"hello\"}]}}\n{bad\n{\"type\":\"user\",\"permissionMode\":\"default\"}\n")
	r.poll()
	var texts []string
	var info ui.InfoMsg
	for _, m := range s.take() {
		switch m := m.(type) {
		case ui.LineMsg:
			if m.Ch != "#alpha" {
				t.Errorf("line on %q", m.Ch)
			}
			texts = append(texts, m.L.Text)
		case ui.InfoMsg:
			info = m
		}
	}
	if len(texts) != 2 || texts[0] != "hello" || !strings.HasPrefix(texts[1], "! unparsable") {
		t.Fatalf("lines: %q", texts)
	}
	if info[f.pid].PermMode != "default" || info[f.pid].Status != "busy" {
		t.Fatalf("info: %+v", info)
	}

	// Send opens the user endpoint lazily in the target's dir and writes a frame.
	got := make(chan peer.Inbound, 1)
	go func() {
		c, err := f.target.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(time.Second))
		sc := c.Lines()
		if sc.Scan() {
			if in, err := peer.ParseFrame(append(sc.Bytes(), '\n')); err == nil {
				got <- in
			}
		}
	}()
	if err := r.send("#alpha", "hi"); err != nil {
		t.Fatal(err)
	}
	select {
	case in := <-got:
		if in.Body != "hi" || in.FromName != "vic" {
			t.Fatalf("frame: %+v", in)
		}
	case <-time.After(time.Second):
		t.Fatal("no frame at target")
	}
	if err := r.send("#nope", "hi"); err == nil {
		t.Fatal("unknown channel: want error")
	}
}

func TestClaudeDir(t *testing.T) {
	t.Setenv("IRCC_CLAUDE_DIR", "/x/y")
	if d, err := claudeDir(); err != nil || d != "/x/y" {
		t.Fatalf("got %q %v", d, err)
	}
}

func texts(msgs []tea.Msg) (lines []string, sessions int) {
	for _, m := range msgs {
		switch m := m.(type) {
		case ui.LineMsg:
			lines = append(lines, m.L.Text)
		case ui.SessionsMsg:
			sessions++
		}
	}
	return
}

func snapshot(t *testing.T, f fix) registry.Snapshot {
	t.Helper()
	es, errs := registry.Scan(filepath.Join(f.claude, "sessions"), "/proc")
	return registry.Snapshot{Entries: es, Errs: errs}
}

func TestSeedMode(t *testing.T) {
	f := newFix(t)
	// A mode event far back, more than seedBytes of other lines after the first
	// partial line, and the real last mode at the end.
	filler := "{\"type\":\"user\"}\n"
	var b []byte
	b = append(b, "{\"type\":\"user\",\"permissionMode\":\"plan\"}\n"...)
	for len(b) < seedBytes+1024 {
		b = append(b, filler...)
	}
	b = append(b, "{\"type\":\"user\",\"permissionMode\":\"default\"}\n"...)
	appendLine(t, f.transcript, string(b))

	s := &sink{}
	r := newRunner(f.claude, "/proc", "vic", s.emit)
	defer r.close()
	r.onSnapshot(snapshot(t, f))
	// No poll: the mode must come from the seed, and no history is replayed.
	if lines, _ := texts(s.take()); len(lines) != 0 {
		t.Fatalf("history replayed: %d lines", len(lines))
	}
	got := make(chan struct{}, 1)
	r.sendFrame = func(sock string, b []byte) error { got <- struct{}{}; return nil }
	if err := r.send("#alpha", "hi"); err != nil {
		t.Fatalf("send with seeded mode: %v", err)
	}
	<-got
}

func TestSnapshotNotes(t *testing.T) {
	f := newFix(t)
	s := &sink{}
	r := newRunner(f.claude, "/proc", "vic", s.emit)
	defer r.close()
	snap := snapshot(t, f)
	snap.Errs = []error{errors.New("registry entry \"9.json\": invalid json")}
	r.onSnapshot(snap)
	lines, _ := texts(s.take())
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "9.json") }) {
		t.Fatalf("scan error not shown: %q", lines)
	}
	// Missing transcript: one note per session, not one per snapshot.
	os.Remove(f.transcript)
	r2 := newRunner(f.claude, "/proc", "vic", s.emit)
	defer r2.close()
	r2.onSnapshot(snap)
	r2.poll()
	r2.poll()
	n := 0
	for _, l := range func() []string { l, _ := texts(s.take()); return l }() {
		if strings.Contains(l, "transcript") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want exactly 1 transcript note, got %d", n)
	}
}

func TestSlowSendDoesNotBlockSnapshots(t *testing.T) {
	f := newFix(t)
	s := &sink{}
	r := newRunner(f.claude, "/proc", "vic", s.emit)
	defer r.close()
	appendLine(t, f.transcript, "{\"type\":\"user\",\"permissionMode\":\"default\"}\n")
	r.onSnapshot(snapshot(t, f))
	entered, release := make(chan struct{}), make(chan struct{})
	r.sendFrame = func(string, []byte) error { close(entered); <-release; return nil }
	done := make(chan error, 1)
	go func() { done <- r.send("#alpha", "hi") }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("send did not reach sendFrame")
	}
	snapDone := make(chan struct{})
	go func() { r.onSnapshot(snapshot(t, f)); r.poll(); close(snapDone) }()
	select {
	case <-snapDone:
	case <-time.After(time.Second):
		t.Fatal("onSnapshot/poll blocked behind a slow send")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWatchLoopRetries(t *testing.T) {
	s := &sink{}
	r := newRunner(t.TempDir(), "/proc", "vic", s.emit)
	calls := 0
	watch := func(context.Context) (<-chan registry.Snapshot, error) {
		calls++
		ch := make(chan registry.Snapshot)
		close(ch) // closes at once while ctx is live
		return ch, nil
	}
	err := watchLoop(context.Background(), r, watch, time.Millisecond)
	if err == nil {
		t.Fatal("want error after bounded retries")
	}
	if calls != 1+watchRetries {
		t.Fatalf("watch called %d times, want %d", calls, 1+watchRetries)
	}
	// ctx cancelled: clean exit, no error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := watchLoop(ctx, r, watch, time.Millisecond); err != nil {
		t.Fatalf("cancelled ctx: %v", err)
	}
}

func TestInfoDropsUnknownMode(t *testing.T) {
	f := newFix(t)
	s := &sink{}
	r := newRunner(f.claude, "/proc", "vic", s.emit)
	defer r.close()
	r.onSnapshot(snapshot(t, f))
	s.take()
	appendLine(t, f.transcript, "{\"type\":\"user\",\"permissionMode\":\"weird\"}\n")
	r.poll()
	for _, m := range s.take() {
		if in, ok := m.(ui.InfoMsg); ok && in[f.pid].PermMode != "" {
			t.Fatalf("unknown mode passed to UI: %q", in[f.pid].PermMode)
		}
	}
}

func TestSendFailsClosedOutsideEndpointDir(t *testing.T) {
	f := newFix(t)
	s := &sink{}
	r := newRunner(f.claude, "/proc", "vic", s.emit)
	defer r.close()
	appendLine(t, f.transcript, "{\"type\":\"user\",\"permissionMode\":\"default\"}\n")
	snap := snapshot(t, f)
	r.onSnapshot(snap)
	sent := 0
	r.sendFrame = func(string, []byte) error { sent++; return nil }
	if err := r.send("#alpha", "hi"); err != nil || sent != 1 {
		t.Fatalf("first send: %v sent=%d", err, sent)
	}
	// The same session now claims a socket in another dir: the endpoint cannot be its peer there.
	moved := snap.Entries[0]
	moved.SocketPath = filepath.Join(t.TempDir(), "x.sock")
	r.onSnapshot(registry.Snapshot{Entries: []registry.Entry{moved}})
	err := r.send("#alpha", "hi")
	if err == nil || !strings.Contains(err.Error(), "another socks dir") || sent != 1 {
		t.Fatalf("want fail-closed error, got %v sent=%d", err, sent)
	}
}

func TestInboundWired(t *testing.T) {
	f := newFix(t)
	s := &sink{}
	r := newRunner(f.claude, "/proc", "vic", s.emit)
	defer r.close()
	appendLine(t, f.transcript, "{\"type\":\"user\",\"permissionMode\":\"default\"}\n")
	snap := snapshot(t, f)
	// This process is the "session" that answers: PeerPID of its connection is os.Getpid().
	self := snap.Entries[0]
	self.PID, self.Name = os.Getpid(), "self"
	r.onSnapshot(registry.Snapshot{Entries: []registry.Entry{snap.Entries[0], self}})
	r.sendFrame = func(string, []byte) error { return nil }
	if err := r.send("#alpha", "hi"); err != nil { // opens the endpoint and starts serveInbound
		t.Fatal(err)
	}
	s.take()
	fr, err := peer.NewUserFrame(r.ep.Addr, "self", peer.Prompting, "ping from self")
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Send(r.ep.Addr.Path(), fr.Bytes()); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		for _, m := range s.take() {
			if lm, ok := m.(ui.LineMsg); ok && lm.Ch == "#self" && strings.Contains(lm.L.Text, "ping from self") {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatal("inbound frame did not land in #self")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestEndpointOpenedOnFirstSnapshot(t *testing.T) {
	f := newFix(t)
	s := &sink{}
	r := newRunner(f.claude, "/proc", "vic", s.emit)
	defer r.close()
	r.onSnapshot(registry.Snapshot{}) // no live entry: nothing to open yet
	if r.ep != nil {
		t.Fatal("endpoint opened with no live session")
	}
	r.onSnapshot(snapshot(t, f))
	r.epMu.Lock()
	open := r.ep != nil
	r.epMu.Unlock()
	if !open {
		t.Fatal("endpoint not opened on the first snapshot with a live entry")
	}
}

func TestBlockLines(t *testing.T) {
	type row struct {
		nick, text string
		dim        bool
	}
	for _, tc := range []struct {
		name string
		ev   transcript.Event
		want []row
	}{
		{"assistant text", transcript.Event{Type: "assistant", Blocks: []transcript.Block{{Kind: transcript.TextBlock, Text: "hi"}}}, []row{{"alpha", "hi", false}}},
		{"tool_use dim", transcript.Event{Type: "assistant", Blocks: []transcript.Block{{Kind: transcript.ToolUseBlock, Tool: "Bash"}}}, []row{{"alpha", "* Bash", true}}},
		{"thinking not shown (vic 2026-10-22: a busy mark instead)", transcript.Event{Type: "assistant", Blocks: []transcript.Block{{Kind: transcript.ThinkingBlock, Text: "hmm"}}}, nil},
		{"user text", transcript.Event{Type: "user", Blocks: []transcript.Block{{Kind: transcript.TextBlock, Text: "typed"}}}, []row{{"vic", "typed", false}}},
		{"tool_result ok hidden", transcript.Event{Type: "user", Blocks: []transcript.Block{{Kind: transcript.ToolResultBlock}}}, nil},
		{"tool_result error", transcript.Event{Type: "user", Blocks: []transcript.Block{{Kind: transcript.ToolResultBlock, IsError: true}}}, []row{{"alpha", "! tool error", true}}},
		{"sidechain nick", transcript.Event{Type: "assistant", Sidechain: true, AgentID: "a1", Blocks: []transcript.Block{{Kind: transcript.TextBlock, Text: "sub"}}}, []row{{"a1@alpha", "sub", false}}},
		{"own-from hidden", transcript.Event{Type: "user", Blocks: []transcript.Block{{Kind: transcript.TextBlock, Text: wrap("uds:/run/x/ircc-vic.sock", "vic", "mine")}}}, nil},
		{"other-from shown dim with from-name", transcript.Event{Type: "user", Blocks: []transcript.Block{{Kind: transcript.TextBlock, Text: wrap("uds:/run/x/cc-socks/9.sock", "beta", "hello\nthere")}}}, []row{{"beta", "hello\nthere", true}}},
		{"malformed wrapper dim raw", transcript.Event{Type: "user", Blocks: []transcript.Block{{Kind: transcript.TextBlock, Text: wrapperPrefix + " junk"}}}, []row{{"vic", wrapperPrefix + " junk", true}}},
		{"user system-reminder dim", transcript.Event{Type: "user", Blocks: []transcript.Block{{Kind: transcript.TextBlock, Text: "<system-reminder>x</system-reminder>"}}}, []row{{"vic", "<system-reminder>x</system-reminder>", true}}},
		{"own wrapper behind header hidden", transcript.Event{Type: "user", Blocks: []transcript.Block{{Kind: transcript.TextBlock, Text: "Another Claude session sent a message:\n" + wrapperPrefix + " from=\"uds:/run/x/ircc-vic.sock\" from-name=\"vic\" from-mode=\"prompting\">\nhi\n</cross-session-message>\n\nThis came from another Claude session."}}}, nil},
		{"peer wrapper behind header shows body", transcript.Event{Type: "user", Blocks: []transcript.Block{{Kind: transcript.TextBlock, Text: "Another Claude session sent a message:\n" + wrapperPrefix + " from=\"uds:/run/x/cc-socks/9.sock\" from-name=\"ren\" from-mode=\"prompting\">\nyo\n</cross-session-message>\n\nThis came from another Claude session."}}}, []row{{"ren", "yo", true}}},
		{"teammate blocks become lines, idle JSON dropped", transcript.Event{Type: "user", Blocks: []transcript.Block{{Kind: transcript.TextBlock, Text: "Another Claude session sent a message:\n" + teammatePrefix + "teammate_id=\"driver-1\" color=\"purple\" summary=\"s\">\nREADY driver-1\n</teammate-message>\n\n" + teammatePrefix + "teammate_id=\"driver-1\" color=\"purple\">\n{\"type\":\"idle_notification\",\"from\":\"driver-1\"}\n</teammate-message>\n\nThis came from another Claude session."}}}, []row{{"driver-1", "READY driver-1", false}}},
		{"send to ircc hidden", transcript.Event{Type: "assistant", Blocks: []transcript.Block{{Kind: transcript.ToolUseBlock, Tool: "SendMessage", ToolTo: "uds:/run/x/ircc-vic.sock"}}}, nil},
		{"send to other shown", transcript.Event{Type: "assistant", Blocks: []transcript.Block{{Kind: transcript.ToolUseBlock, Tool: "SendMessage", ToolTo: "uds:/run/x/cc-socks/9.sock"}}}, []row{{"alpha", "* SendMessage", true}}},
		{"send by name shown", transcript.Event{Type: "assistant", Blocks: []transcript.Block{{Kind: transcript.ToolUseBlock, Tool: "SendMessage", ToolTo: "ircc-vic"}}}, []row{{"alpha", "* SendMessage", true}}},
		{"send to user by name hidden", transcript.Event{Type: "assistant", Blocks: []transcript.Block{{Kind: transcript.ToolUseBlock, Tool: "SendMessage", ToolTo: "vic"}}}, nil},
		{"send to room by name hidden", transcript.Event{Type: "assistant", Blocks: []transcript.Block{{Kind: transcript.ToolUseBlock, Tool: "SendMessage", ToolTo: "&test"}}}, nil},
		{"tool arg shown", transcript.Event{Type: "assistant", Blocks: []transcript.Block{{Kind: transcript.ToolUseBlock, Tool: "Bash", ToolArg: "go test ./..."}}}, []row{{"alpha", "* Bash: go test ./...", true}}},
		{"no blocks", transcript.Event{Type: "system"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []row
			for _, l := range blockLines(tc.ev, "alpha", "vic") {
				got = append(got, row{l.Nick, l.Text, l.Dim})
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

func wrap(from, name, body string) string {
	return wrapperPrefix + ` from="` + from + `" from-name="` + name + `" from-mode="prompting">` + "\n" + body + "\n</cross-session-message>"
}

func TestSendRoomRoutesToSay(t *testing.T) {
	r := newRunner(t.TempDir(), "/proc", "vic", (&sink{}).emit)
	defer r.close()
	sent := 0
	r.sendFrame = func(string, []byte) error { sent++; return nil }
	if err := r.send("&r", "hi"); !errors.Is(err, errNoRooms) || sent != 0 { // say path, not a session lookup
		t.Fatalf("& send: %v sent=%d", err, sent)
	}
	if err := r.send("#r", "hi"); err == nil || errors.Is(err, errNoRooms) { // control: # is a session
		t.Fatalf("# send: %v", err)
	}
}

func TestInvitableListsNonMembers(t *testing.T) {
	f := newFix(t)
	r := newRunner(f.claude, "/proc", "vic", (&sink{}).emit)
	defer r.close()
	appendLine(t, f.transcript, "{\"type\":\"user\",\"permissionMode\":\"default\"}\n")
	r.onSnapshot(snapshot(t, f))
	room := rooms.Room{Name: mustName(t, "&r")} // no members yet
	got := r.invitable(room)
	if len(got) != 1 || got[0].Member != (rooms.Member{Session: "sid1", Nick: "alpha"}) || got[0].Class != peer.Prompting {
		t.Fatalf("invitable %+v", got)
	}
	if es, _ := r.endsFor(room); len(es) != 0 { // control: fan-out still members only
		t.Fatalf("endsFor %+v", es)
	}
}

func TestSendLiteralHasNoWrapper(t *testing.T) {
	f := newFix(t)
	r := newRunner(f.claude, "/proc", "vic", (&sink{}).emit)
	defer r.close()
	appendLine(t, f.transcript, "{\"type\":\"user\",\"permissionMode\":\"default\"}\n")
	r.onSnapshot(snapshot(t, f))
	var got []byte
	r.sendFrame = func(_ string, b []byte) error { got = b; return nil }
	if err := r.sendLiteral("#alpha", "/compact keep the API notes"); err != nil {
		t.Fatal(err)
	}
	var fr struct {
		Message struct{ Content string }
		From    string
	}
	if err := json.Unmarshal(got, &fr); err != nil || fr.Message.Content != "/compact keep the API notes" || !strings.HasPrefix(fr.From, "uds:") {
		t.Fatalf("frame %s err %v", got, err)
	}
	if err := r.sendLiteral("&room", "x"); err == nil { // NEG: a room has no Claude socket
		t.Fatal("literal to a room: want error")
	}
}

func TestHistoryReplayedOld(t *testing.T) {
	f := newFix(t)
	appendLine(t, f.transcript, "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"from before\"}]}}\n")
	s := &sink{}
	r := newRunner(f.claude, "/proc", "vic", s.emit)
	defer r.close()
	r.onSnapshot(snapshot(t, f))
	r.poll()
	r.poll() // NEG: history comes once
	appendLine(t, f.transcript, "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"live now\"}]}}\n")
	r.poll()
	var got []string
	for _, m := range s.take() {
		if lm, ok := m.(ui.LineMsg); ok {
			got = append(got, fmt.Sprintf("%s old=%v", lm.L.Text, lm.Old))
		}
	}
	if strings.Join(got, "|") != "from before old=true|live now old=false" {
		t.Fatalf("lines %q", got)
	}
}

func TestRepliesToOurLinesAreChat(t *testing.T) {
	f := newFix(t)
	s := &sink{}
	r := newRunner(f.claude, "/proc", "vic", s.emit)
	defer r.close()
	r.onSnapshot(snapshot(t, f))
	r.poll()
	ours := `{"type":"user","message":{"content":"Another Claude session sent a message:\n<cross-session-message from=\"uds:/run/x/ircc-vic.sock\" from-name=\"vic\" from-mode=\"prompting\">\nstatus?\n</cross-session-message>"}}`
	say := func(txt string) string {
		return `{"type":"assistant","message":{"content":[{"type":"text","text":"` + txt + `"}]}}`
	}
	appendLine(t, f.transcript, ours+"\n"+`{"type":"user","message":{"content":"<system-reminder>x</system-reminder>"}}`+"\n"+say("all green")+"\n"+
		`{"type":"user","message":{"content":"typed in claude"}}`+"\n"+say("ok doing it")+"\n")
	r.poll()
	var got []string
	for _, m := range s.take() {
		if lm, ok := m.(ui.LineMsg); ok && lm.L.Nick == "alpha" {
			got = append(got, fmt.Sprintf("%s=%v", lm.L.Text, lm.L.Kind == model.Chat))
		}
	}
	if strings.Join(got, "|") != "all green=true|ok doing it=false" { // NEG: a prompt typed in Claude ends the reply
		t.Fatalf("got %q", got)
	}
}

func TestAgentsMsg(t *testing.T) {
	f := newFix(t)
	sub := filepath.Join(filepath.Dir(f.transcript), "sid1", "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "agent-adriver-1-ab12.meta.json"), []byte(`{"agentType":"driver-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	agentLog := filepath.Join(sub, "agent-adriver-1-ab12.jsonl")
	if err := os.WriteFile(agentLog, []byte(`{"type":"assistant","message":{"stop_reason":"tool_use","content":[]}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &sink{}
	r := newRunner(f.claude, "/proc", "vic", s.emit)
	defer r.close()
	r.onSnapshot(snapshot(t, f))
	agents := func() []string {
		var out []string
		for _, m := range s.take() {
			if am, ok := m.(ui.AgentsMsg); ok {
				out = append(out, fmt.Sprint(am.Ch, am.Agents))
			}
		}
		return out
	}
	r.poll()
	if got := agents(); len(got) != 1 || got[0] != "#alpha[{driver-1 true}]" {
		t.Fatalf("busy: %q", got)
	}
	appendLine(t, agentLog, `{"type":"assistant","message":{"stop_reason":"end_turn","content":[]}}`+"\n")
	r.poll()
	if got := agents(); len(got) != 1 || got[0] != "#alpha[{driver-1 false}]" {
		t.Fatalf("idle: %q", got)
	}
	r.poll()
	if got := agents(); len(got) != 0 { // NEG: unchanged list is not re-sent
		t.Fatalf("resent: %q", got)
	}
}
