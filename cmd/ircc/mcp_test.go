package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"ircc/internal/mcp"
	"ircc/internal/model"
	"ircc/internal/peer"
	"ircc/internal/registry"
	"ircc/internal/rooms"
	"ircc/internal/ui"
)

// mcpFix is newTR (room &r: alice, bob, scout@alice; carol live, not a member)
// with the MCP server listening on a real unix socket. The test process is the
// caller: a fake /proc says its parent is parent (a fake caller pid).
type mcpFix struct {
	*trFix
	sock string
	cs   *cmds
}

func newMCP(t *testing.T, parent int) *mcpFix {
	t.Helper()
	x := &mcpFix{trFix: newTR(t)}
	proc := t.TempDir()
	stat := filepath.Join(proc, strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(stat, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stat, "stat"), fmt.Appendf(nil, "%d (go test) S %d 1 1 0", os.Getpid(), parent), 0o600); err != nil {
		t.Fatal(err)
	}
	x.r.procRoot = proc
	addr, err := peer.ParseAddr("uds:/s/ircc-vic.sock")
	if err != nil {
		t.Fatal(err)
	}
	x.r.ep, x.r.epDir = &peer.Endpoint{Name: "vic", Addr: addr}, "/s" // user endpoint stand-in: # sends need it
	x.cs = &cmds{rt: x.r.rt, dir: x.f.dir, emit: x.r.emit, send: x.r.sendTo, inviteFn: rooms.Invite,
		ends: x.r.invitable, sendCh: x.r.sendAs, whois: x.r.whois, user: func() string { return x.r.user }}
	m, err := model.New(10)
	if err != nil {
		t.Fatal(err)
	}
	m = m.WithSessions([]model.Session{{PID: 101, Name: "alice"}, {PID: 102, Name: "bob"}}).WithRooms([]string{"&r"})
	now := time.Now()
	for _, l := range []model.Line{
		{At: now.Add(-time.Hour), Nick: "alice", Text: "old needle"},
		{At: now, Nick: "alice", Text: "* Bash: go test", Kind: model.Tool},
		{At: now, Nick: "vic", Text: "new needle"},
	} {
		if m, err = m.AddLine("#alice", l); err != nil {
			t.Fatal(err)
		}
	}
	snap := func() (ui.Snapshot, error) { return ui.Snapshot{Model: m, User: "vic", Typing: true}, nil }
	x.sock = filepath.Join(x.f.dir+"-mcp", "mcp.sock")
	l, err := listenMCP(x.sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); newMCPSrv(x.r, x.cs, snap).serve(ctx, l) }()
	t.Cleanup(func() { cancel(); <-done })
	return x
}

// client is one MCP session over the socket; every read has a deadline.
type client struct {
	t  *testing.T
	c  net.Conn
	sc *bufio.Scanner
	id int
}

func (x *mcpFix) dial(t *testing.T) *client {
	t.Helper()
	c, err := net.Dial("unix", x.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(c)
	sc.Buffer(nil, 1<<20)
	return &client{t: t, c: c, sc: sc}
}

// rpc sends one request; ok is false when the server closed the connection.
func (c *client) rpc(method string, params any) (map[string]any, bool) {
	c.t.Helper()
	c.id++
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := c.c.Write(append(b, '\n')); err != nil {
		return nil, false
	}
	if !c.sc.Scan() {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(c.sc.Bytes(), &m); err != nil {
		c.t.Fatalf("response %s: %v", c.sc.Bytes(), err)
	}
	return m, true
}

// call runs a tool; it returns the text and whether it was a tool error.
func (c *client) call(name string, args map[string]any) (string, bool) {
	c.t.Helper()
	m, ok := c.rpc("tools/call", map[string]any{"name": name, "arguments": args})
	if !ok {
		c.t.Fatalf("%s: connection closed", name)
	}
	res, _ := m["result"].(map[string]any)
	content, _ := res["content"].([]any)
	if len(content) != 1 {
		c.t.Fatalf("%s: response %v", name, m)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	return text, res["isError"] == true
}

func (c *client) ok(name string, args map[string]any) string {
	c.t.Helper()
	text, isErr := c.call(name, args)
	if isErr {
		c.t.Fatalf("%s %v: tool error %s", name, args, text)
	}
	return text
}

func (x *mcpFix) members(t *testing.T, room string) []string {
	t.Helper()
	r, ok := x.r.rt.room(mustName(t, room))
	if !ok {
		return nil
	}
	var out []string
	for _, m := range r.Members {
		out = append(out, m.Nick)
	}
	return out
}

func (x *mcpFix) framesTo(sock string) []peer.Inbound {
	x.mu.Lock()
	defer x.mu.Unlock()
	var out []peer.Inbound
	for _, s := range x.sent {
		if s.sock == sock {
			out = append(out, s.in)
		}
	}
	return out
}

// TestMCPEveryTool calls every I4 tool once over a real socket as alice (pid 101,
// reached by the /proc parent walk from the test pid).
func TestMCPEveryTool(t *testing.T) {
	x := newMCP(t, 101)
	c := x.dial(t)
	init, ok := c.rpc("initialize", map[string]any{"protocolVersion": "2025-06-18"})
	if !ok || !strings.Contains(fmt.Sprint(init["result"]), "&vic") {
		t.Fatalf("initialize %v", init)
	}
	if l, _ := c.rpc("tools/list", nil); len(l["result"].(map[string]any)["tools"].([]any)) != len(mcp.Tools()) {
		t.Fatalf("tools/list %v", l)
	}

	list := c.ok("ircc_list", nil)
	if !slices.Equal(x.members(t, "&vic"), []string{"alice"}) { // I6: the first call joins &vic
		t.Fatalf("&vic members %v", x.members(t, "&vic"))
	}
	for _, want := range []string{`"name":"#alice"`, `"name":"&r"`, `"kind":"&"`, `"unread":`, `"nick":"vic","status":"typing"`, `"nick":"bob","status":"idle"`} {
		if !strings.Contains(list, want) {
			t.Errorf("ircc_list lacks %s: %s", want, list)
		}
	}

	if got := c.ok("ircc_join", map[string]any{"room": "&new"}); !strings.Contains(got, "&new") || !slices.Equal(x.members(t, "&new"), []string{"alice"}) {
		t.Fatalf("join: %s members %v", got, x.members(t, "&new"))
	}
	if got := c.ok("ircc_invite", map[string]any{"nick": "bob", "room": "&new"}); !strings.Contains(got, "bob") || len(x.framesTo("/s/bob.sock")) != 1 {
		t.Fatalf("invite: %s frames %v", got, x.framesTo("/s/bob.sock"))
	}
	c.ok("ircc_topic", map[string]any{"room": "&new", "text": "ship it"})
	if r, _ := x.r.rt.room(mustName(t, "&new")); r.Topic != "ship it" {
		t.Fatalf("topic %q", r.Topic)
	}
	if got := c.ok("ircc_names", map[string]any{"room": "&new"}); !strings.Contains(got, `"nick":"bob","status":"idle"`) {
		t.Fatalf("names: %s", got)
	}
	if got := c.ok("ircc_kick", map[string]any{"room": "&new", "nick": "bob"}); strings.Contains(got, "bob") || !slices.Equal(x.members(t, "&new"), []string{"alice"}) {
		t.Fatalf("kick: %s", got)
	}
	if got := c.ok("ircc_msg", map[string]any{"to": "&r", "text": "hello room"}); got != "delivered: yes" {
		t.Fatalf("msg &r: %s", got)
	}
	if fs := x.framesTo("/s/bob.sock"); len(fs) != 2 || !strings.Contains(fs[1].Body, "<alice> hello room") {
		t.Fatalf("room frame to bob %+v", fs)
	}
	if len(x.framesTo("/s/alice.sock")) != 0 { // never to the writer's own socket
		t.Fatalf("frame to alice herself")
	}
	if got := c.ok("ircc_msg", map[string]any{"to": "#bob", "text": "hi bob"}); got != "delivered: yes" {
		t.Fatalf("msg #bob: %s", got)
	}
	if got := c.ok("ircc_whois", map[string]any{"nick": "bob"}); !strings.Contains(got, "pid 102") {
		t.Fatalf("whois: %s", got)
	}
	hist := c.ok("ircc_history", map[string]any{"channel": "#alice", "limit": 5, "since": time.Now().Add(-time.Minute).Format(time.RFC3339)})
	if strings.Contains(hist, "old needle") || strings.Contains(hist, "Bash") || !strings.Contains(hist, "new needle") {
		t.Fatalf("history: %s", hist)
	}
	if hist := c.ok("ircc_history", map[string]any{"channel": "#alice", "tools": true}); !strings.Contains(hist, "Bash") || !strings.Contains(hist, "old needle") {
		t.Fatalf("history tools: %s", hist)
	}
	if got := c.ok("ircc_grep", map[string]any{"channel": "all", "text": "NEEDLE"}); strings.Count(got, "needle") != 2 || !strings.Contains(got, `"channel":"#alice"`) {
		t.Fatalf("grep: %s", got)
	}
	c.ok("ircc_part", map[string]any{"room": "&new"})
	if len(x.members(t, "&new")) != 0 {
		t.Fatalf("part: members %v", x.members(t, "&new"))
	}
	c.ok("ircc_close", map[string]any{"room": "&new"})
	if _, open := x.r.rt.room(mustName(t, "&new")); open {
		t.Fatal("close: room still open")
	}
	if text, isErr := c.call("ircc_topic", map[string]any{"room": "&new", "text": "x"}); !isErr || !strings.Contains(text, "no room") {
		t.Fatalf("topic on a closed room: %q %v", text, isErr)
	}

	// I5: each mutating call shows a dim line; errors are tool errors, never notes.
	if !slices.Contains(x.lines("&new"), "|alice via mcp: /invite bob &new") || !slices.Contains(x.lines("&vic"), "|alice via mcp: /join &vic") {
		t.Fatalf("via lines &new %q &vic %q", x.lines("&new"), x.lines("&vic"))
	}
	if n := x.notes(); len(n) != 0 {
		t.Fatalf("notes %q", n)
	}
}

// TestMCPUnknownCallerRefused (NEG, I3): no live session up the /proc chain,
// so the connection is closed before any answer.
func TestMCPUnknownCallerRefused(t *testing.T) {
	x := newMCP(t, 4242) // not live, and no stat for it: the walk ends
	c := x.dial(t)
	if m, ok := c.rpc("initialize", map[string]any{}); ok {
		t.Fatalf("unknown caller got an answer: %v", m)
	}
}

// TestMCPMsgNeverAsTUINick (NEG, I3): ircc_msg sends as the caller nick; a
// caller whose session nick is the TUI user's is refused, nothing goes out.
func TestMCPMsgNeverAsTUINick(t *testing.T) {
	x := newMCP(t, 101)
	c := x.dial(t)
	c.ok("ircc_msg", map[string]any{"to": "#bob", "text": "hi"})
	fs := x.framesTo("/s/bob.sock")
	if len(fs) != 1 || fs[0].FromName != "alice" || fs[0].FromName == x.r.user {
		t.Fatalf("frames to bob %+v", fs)
	}

	y := newMCP(t, 104)
	y.r.mu.Lock() // the MCP server already serves: guard the maps it reads
	y.r.live[104] = registry.Entry{PID: 104, SessionID: "s-vic", SocketPath: "/s/vic.sock", Name: "vic"}
	y.r.names[104], y.r.tails[104] = "#vic", &tail{mode: "default"}
	y.r.mu.Unlock()
	d := y.dial(t)
	if text, isErr := d.call("ircc_msg", map[string]any{"to": "#bob", "text": "I am vic"}); !isErr || !strings.Contains(text, "TUI user") {
		t.Fatalf("caller named vic: %q %v", text, isErr)
	}
	if fs := y.framesTo("/s/bob.sock"); len(fs) != 0 {
		t.Fatalf("sent as vic: %+v", fs)
	}
}

// TestMCPAuthorNeverVic (NEG, R1): cmds.do refuses every mutating command from
// an MCP author whose nick is the TUI user's (any case), and an MCP author is
// never the TUI author.
func TestMCPAuthorNeverVic(t *testing.T) {
	x := newMCP(t, 101)
	for _, line := range []string{"/join &z", "/part &r", "/close &r", "/invite bob &r", "/kick bob", "/topic t", "/msg #bob hi", "/msg &r hi"} {
		cm, err := model.ParseCmd(line)
		if err != nil {
			t.Fatal(err)
		}
		for _, nick := range []string{"vic", "VIC", ""} {
			if err := x.cs.do(author{nick: nick, pid: 101}, "&r", cm); err == nil {
				t.Errorf("%s as MCP %q: want refused", line, nick)
			}
		}
	}
	if len(x.socks()) != 0 || !slices.Equal(x.members(t, "&r"), []string{"alice", "bob", "scout@alice"}) {
		t.Fatalf("a refused command acted: frames %v members %v", x.socks(), x.members(t, "&r"))
	}
	if a, ok := x.r.callerOf(os.Getpid()); !ok || a.vic || a.nick != "alice" {
		t.Fatalf("callerOf %+v %v", a, ok)
	}
	for _, l := range x.lines("&r") {
		if strings.HasPrefix(l, "vic|") {
			t.Fatalf("a vic line in &r: %q", l)
		}
	}
}
