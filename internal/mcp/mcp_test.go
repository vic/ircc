package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// exchange feeds lines to Serve and returns its responses, one per line.
func exchange(t *testing.T, call CallFunc, lines ...string) []map[string]any {
	t.Helper()
	var out strings.Builder
	if err := Serve(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out, call); err != nil {
		t.Fatal(err)
	}
	return decode(t, out.String())
}

func decode(t *testing.T, s string) []map[string]any {
	t.Helper()
	var got []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("response %q: %v", l, err)
		}
		got = append(got, m)
	}
	return got
}

func echo(name string, args json.RawMessage) (string, error) {
	if name == "ircc_whois" {
		return "", errors.New("no session or teammate x")
	}
	return name + " " + string(args), nil
}

func TestServe(t *testing.T) {
	got := exchange(t, echo,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ircc_list","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"ircc_whois","arguments":{"nick":"x"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"ircc_quit"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"nope"}`,
		`not json`,
		`{"jsonrpc":"2.0","id":"p","method":"ping"}`,
	)
	if len(got) != 8 { // the notification gets no answer
		t.Fatalf("want 8 responses, got %d: %v", len(got), got)
	}
	init := got[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-03-26" || !strings.Contains(init["instructions"].(string), "&vic") {
		t.Fatalf("initialize %v", init)
	}
	if tools := got[1]["result"].(map[string]any)["tools"].([]any); len(tools) != len(Tools()) || len(tools) != 12 {
		t.Fatalf("tools/list %d tools", len(tools))
	}
	for i, tc := range []struct {
		want  string
		isErr bool
	}{{"ircc_list {}", false}, {"no session or teammate x", true}, {`unknown tool "ircc_quit"`, true}} {
		res := got[2+i]["result"].(map[string]any)
		text := res["content"].([]any)[0].(map[string]any)["text"]
		if text != tc.want || (res["isError"] == true) != tc.isErr {
			t.Errorf("call %d: %v", i, res)
		}
	}
	if code := got[5]["error"].(map[string]any)["code"]; code != float64(-32601) {
		t.Errorf("unknown method code %v", code)
	}
	if code := got[6]["error"].(map[string]any)["code"]; code != float64(-32700) {
		t.Errorf("parse error code %v", code)
	}
	if got[7]["id"] != "p" || got[7]["result"] == nil {
		t.Errorf("ping %v", got[7])
	}
}

// TestToolSchemas: every schema is an object schema whose required keys are properties.
func TestToolSchemas(t *testing.T) {
	for _, tl := range Tools() {
		var s struct {
			Type       string                    `json:"type"`
			Properties map[string]map[string]any `json:"properties"`
			Required   []string                  `json:"required"`
		}
		if err := json.Unmarshal(tl.InputSchema, &s); err != nil || s.Type != "object" {
			t.Fatalf("%s: schema %s: %v", tl.Name, tl.InputSchema, err)
		}
		for _, r := range s.Required {
			if _, ok := s.Properties[r]; !ok {
				t.Errorf("%s: required %q has no property", tl.Name, r)
			}
		}
	}
}

// TestBridgeNoTUI is negative control 4 (I2, R4): with no socket the bridge
// still answers initialize and tools/list, and every call is the I2 error.
func TestBridgeNoTUI(t *testing.T) {
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ircc_list","arguments":{}}}`,
	}, "\n") + "\n"
	var out strings.Builder
	if err := Bridge(strings.NewReader(in), &out, filepath.Join(t.TempDir(), "mcp.sock")); err != nil {
		t.Fatal(err)
	}
	got := decode(t, out.String())
	if len(got) != 3 || got[0]["result"] == nil || got[1]["result"] == nil {
		t.Fatalf("responses %v", got)
	}
	res := got[2]["result"].(map[string]any)
	if res["isError"] != true || res["content"].([]any)[0].(map[string]any)["text"] != NotRunning {
		t.Fatalf("call with no TUI: %v", res)
	}
	if NotRunning != "ircc is not running: start it (herdr session vix-agents, pane ircc)" {
		t.Fatalf("I2 text changed: %q", NotRunning)
	}
}

// TestBridgeCopies: with a listener the bridge is a plain pipe both ways.
func TestBridgeCopies(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "mcp.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(time.Second))
		sc := bufio.NewScanner(c)
		for sc.Scan() {
			if _, err := io.WriteString(c, "got "+sc.Text()+"\n"); err != nil {
				return
			}
		}
	}()
	var out strings.Builder
	if err := Bridge(strings.NewReader("a\nb\n"), &out, sock); err != nil {
		t.Fatal(err)
	}
	if out.String() != "got a\ngot b\n" {
		t.Fatalf("bridge out %q", out.String())
	}
}
