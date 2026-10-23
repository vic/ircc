package transcript

import (
	"bufio"
	"os"
	"slices"
	"strings"
	"testing"
)

func load(t *testing.T, name string) []Event {
	t.Helper()
	f, err := os.Open("testdata/events/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var evs []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		e, err := ParseEvent(sc.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, e)
	}
	return evs
}

func TestParseEventTypes(t *testing.T) {
	evs := load(t, "types.jsonl")
	want := []string{"assistant", "user", "system", "attachment", "queue-operation", "permission-mode", "agent-name", "ai-title", "assistant"}
	if len(evs) != len(want) {
		t.Fatalf("got %d events", len(evs))
	}
	for i, w := range want {
		if evs[i].Type != w {
			t.Errorf("%d: type %q want %q", i, evs[i].Type, w)
		}
	}
	if last := evs[8]; !last.Sidechain || last.AgentID != "a1" {
		t.Errorf("sidechain: %+v", last)
	}
	if evs[0].Sidechain {
		t.Error("plain event marked sidechain")
	}
}

func TestParseEventHeld(t *testing.T) {
	evs := load(t, "held.jsonl")
	if !evs[0].Held || evs[0].HeldFrom != "uds:/tmp/x/ircc-%26review.sock" || evs[0].HeldPID != 548628 || evs[0].HeldPreview != "..." {
		t.Errorf("warning line: %+v", evs[0])
	}
	if evs[1].Held || evs[1].HeldFrom != "" {
		t.Errorf("level info must not be held: %+v", evs[1])
	}
}

func TestLastMode(t *testing.T) {
	if got := LastMode(load(t, "mode.jsonl")); got != "bypassPermissions" {
		t.Errorf("got %q", got)
	}
	if got := LastMode(load(t, "types.jsonl")); got != "" {
		t.Errorf("no mode: got %q", got)
	}
}

func TestParseEventMalformed(t *testing.T) {
	for _, in := range []string{``, `{`, `{"type":1}`} {
		if e, err := ParseEvent([]byte(in)); err == nil || e.Type != "" || e.Blocks != nil || e.Held {
			t.Errorf("%q: want error and zero Event, got %+v, %v", in, e, err)
		}
	}
}

func TestParseEventAssistantBlocks(t *testing.T) {
	want := [][]Block{
		{{Kind: TextBlock, Text: "hi"}},
		{{Kind: ToolUseBlock, Tool: "Bash"}},
		{{Kind: ThinkingBlock, Text: "hmm"}, {Kind: TextBlock, Text: "done"}}, // unknown type skipped
		nil,
		{{Kind: ToolUseBlock, Tool: "SendMessage", ToolTo: "uds:/run/x/ircc-vic.sock", ToolArg: "uds:/run/x/ircc-vic.sock"}, {Kind: ToolUseBlock, Tool: "Bash"}}, // non-string input.to ignored
	}
	checkBlocks(t, load(t, "assistant.jsonl"), want)
}

func TestParseEventUserBlocks(t *testing.T) {
	want := [][]Block{
		{{Kind: TextBlock, Text: "plain string"}},
		{{Kind: ToolResultBlock, IsError: true}},
		{{Kind: ToolResultBlock}, {Kind: TextBlock, Text: "typed"}}, // non-string result content ok
		nil,
	}
	checkBlocks(t, load(t, "user.jsonl"), want)
}

func checkBlocks(t *testing.T, evs []Event, want [][]Block) {
	t.Helper()
	if len(evs) != len(want) {
		t.Fatalf("got %d events", len(evs))
	}
	for i, w := range want {
		if !slices.Equal(evs[i].Blocks, w) {
			t.Errorf("event %d: got %+v want %+v", i, evs[i].Blocks, w)
		}
	}
}

func TestParseEventBadContent(t *testing.T) { // NEG
	for _, in := range []string{
		`{"type":"assistant","message":{"content":5}}`,
		`{"type":"assistant","message":{"content":"str"}}`, // assistant content is an array
		`{"type":"user","message":{"content":{"a":1}}}`,
		`{"type":"assistant","message":{"content":[5]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":5}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":5}]}}`,
		`{"type":"assistant","message":5}`,
	} {
		if e, err := ParseEvent([]byte(in)); err == nil || e.Type != "" || e.Blocks != nil {
			t.Errorf("%s: want error and zero Event, got %+v, %v", in, e, err)
		}
	}
}

func TestParseEventHeldFailsLoud(t *testing.T) {
	const pre = `{"type":"system","subtype":"informational","level":"warning","content":"Held peer message — `
	const ok = `from uds:/a.sock [verified pid 7] (peer claims name: x); preview: «hi» — not delivered`
	e, err := ParseEvent([]byte(pre + ok + `"}`))
	if err != nil || !e.Held || e.HeldPID != 7 || e.HeldPreview != "hi" || e.HeldFrom != "uds:/a.sock" {
		t.Fatalf("baseline: %+v %v", e, err)
	}
	for _, c := range []struct{ name, tail string }{
		{"no pid", `from uds:/a.sock (peer claims name: x); preview: «hi»"}`},
		{"garbled pid", `from uds:/a.sock [verified pid abc] preview: «hi»"}`},
		{"unterminated pid", `from uds:/a.sock [verified pid 12 preview: «hi»"}`},
		{"zero pid", `from uds:/a.sock [verified pid 0] preview: «hi»"}`},
		{"negative pid", `from uds:/a.sock [verified pid -3] preview: «hi»"}`},
		{"no from", `[verified pid 7] preview: «hi»"}`},
		{"empty from", `from  [verified pid 7] preview: «hi»"}`},
		{"no preview", `from uds:/a.sock [verified pid 7] (peer claims name: x)"}`},
		{"unclosed preview", `from uds:/a.sock [verified pid 7] preview: «hi"}`},
	} {
		if e, err := ParseEvent([]byte(pre + c.tail)); err == nil {
			t.Errorf("%s: want error, got %+v", c.name, e)
		}
	}
}

func TestToolArg(t *testing.T) {
	ev, err := ParseEvent([]byte(`{"type":"assistant","message":{"content":[` +
		`{"type":"tool_use","name":"Bash","input":{"description":"run","command":"go test ./...\nmore"}},` +
		`{"type":"tool_use","name":"Edit","input":{"file_path":"/a/b.go","old_string":"x"}},` +
		`{"type":"tool_use","name":"X","input":{"n":3}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, b := range ev.Blocks {
		got = append(got, b.ToolArg)
	}
	if strings.Join(got, "|") != "go test ./...|/a/b.go|" { // command beats description; first line only; NEG: no string input
		t.Fatalf("args %q", got)
	}
}

func TestNotices(t *testing.T) {
	for _, c := range []struct {
		line, text  string
		err, blocks bool
	}{
		{`{"type":"attachment","attachment":{"type":"goal_status","met":true,"condition":"ship it"}}`, "goal met: ship it", false, false},
		{`{"type":"attachment","attachment":{"type":"hook_blocking_error","hookEvent":"Stop","blockingError":{"blockingError":"update HANDOFF"}}}`, "Stop hook: update HANDOFF", true, false},
		{`{"type":"user","message":{"content":"PreToolUse:Bash hook error: refused"}}`, "PreToolUse:Bash hook error: refused", true, false},
		{`{"type":"user","message":{"content":"Stop hook feedback:\nupdate HANDOFF"}}`, "", false, false}, // shown by the attachment
		{`{"type":"user","message":{"content":"a hook error: is just words"}}`, "", false, true},          // NEG: plain text
		{`{"type":"attachment","attachment":{"type":"hook_success"}}`, "", false, false},                  // NEG: too frequent to toast
	} {
		ev, err := ParseEvent([]byte(c.line))
		if err != nil {
			t.Fatal(err)
		}
		text, isErr := "", false
		if ev.Notice != nil {
			text, isErr = ev.Notice.Text, ev.Notice.Err
		}
		if text != c.text || isErr != c.err || (len(ev.Blocks) > 0) != c.blocks {
			t.Errorf("%s: notice %q err %v blocks %d", c.line, text, isErr, len(ev.Blocks))
		}
	}
}

func TestCtxPct(t *testing.T) {
	for line, want := range map[string]float64{
		`{"type":"attachment","attachment":{"type":"hook_additional_context","content":["BUDGET PreToolUse · ctx 91.5% · 5h 11.0%"]}}`: 91.5,
		`{"type":"attachment","attachment":{"type":"hook_additional_context","content":["no budget here"]}}`:                           0, // NEG
		`{"type":"attachment","attachment":{"type":"hook_success","content":["ctx 99%"]}}`:                                             0, // NEG: other kind
	} {
		ev, err := ParseEvent([]byte(line))
		if err != nil || ev.CtxPct != want {
			t.Errorf("%s: %v %v", line, ev.CtxPct, err)
		}
	}
}
