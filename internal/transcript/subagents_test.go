package transcript

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const sid = "sess"

func writeMeta(t *testing.T, proj, file, body string) {
	t.Helper()
	d := filepath.Join(proj, sid, "subagents")
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, file), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSubagents(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		want    []Agent
		wantErr int
	}{
		{"missing dir", nil, nil, 0},
		{"named agent", map[string]string{
			"agent-areviewer-9f3c.meta.json": `{"agentType":"Explore","description":"d","spawnDepth":1,"model":"m"}`,
		}, []Agent{{ID: "areviewer-9f3c", Nick: "reviewer", Type: "Explore", Description: "d", Model: "m", Depth: 1}}, 0},
		{"unnamed agent nick is id", map[string]string{
			"agent-a1b2c3.meta.json": `{"agentType":"t"}`,
		}, []Agent{{ID: "a1b2c3", Nick: "a1b2c3", Type: "t"}}, 0},
		{"sorted by id, non-meta ignored", map[string]string{
			"agent-ab-1.meta.json": `{}`,
			"agent-aa-1.meta.json": `{}`,
			"agent-aa-1.jsonl":     `not json`,
			"other.meta.json":      `{nope`,
		}, []Agent{{ID: "aa-1", Nick: "a"}, {ID: "ab-1", Nick: "b"}}, 0},
		{"id order not file order", map[string]string{
			"agent-a-1.meta.json": `{}`,
			"agent-a.meta.json":   `{}`,
		}, []Agent{{ID: "a", Nick: "a"}, {ID: "a-1", Nick: "a-1"}}, 0},
		// NEG: corrupt meta does not abort the list
		{"bad meta json", map[string]string{
			"agent-ax-1.meta.json": `{nope`,
			"agent-ay-1.meta.json": `{}`,
		}, []Agent{{ID: "ay-1", Nick: "y"}}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proj := t.TempDir()
			for f, b := range tc.files {
				writeMeta(t, proj, f, b)
			}
			got, errs := Subagents(proj, sid)
			if len(errs) != tc.wantErr {
				t.Fatalf("errs %v", errs)
			}
			for i := range got {
				if want := filepath.Join(proj, sid, "subagents", "agent-"+got[i].ID+".jsonl"); got[i].Path != want {
					t.Fatalf("path %s want %s", got[i].Path, want)
				}
				got[i].Path = ""
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

func TestSubagentsBadSessionID(t *testing.T) {
	for _, id := range []string{"", "..", "a/b", "a*", "a?", "a[b", `a\b`} {
		got, errs := Subagents(t.TempDir(), id)
		if got != nil || len(errs) != 1 || !errors.Is(errs[0], ErrBadSessionID) {
			t.Errorf("id %q: %v %v", id, got, errs)
		}
	}
}

func TestSubagentsGlobCharsInProjectDir(t *testing.T) {
	proj := filepath.Join(t.TempDir(), "p[1]*")
	writeMeta(t, proj, "agent-aq-1.meta.json", `{}`)
	got, errs := Subagents(proj, sid)
	if len(errs) != 0 || len(got) != 1 {
		t.Fatalf("%v %v", got, errs)
	}
}

func TestBusy(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	idle := write("idle.jsonl", `{"type":"user","message":{"content":"go"}}`+"\n"+`{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}`+"\n"+`{"type":"attachment"}`+"\n")
	tool := write("tool.jsonl", `{"type":"assistant","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","name":"Bash","input":{}}]}}`+"\n")
	asked := write("asked.jsonl", `{"type":"assistant","message":{"stop_reason":"end_turn","content":[]}}`+"\n"+`{"type":"user","message":{"content":"next task"}}`+"\n")
	for _, c := range []struct {
		p           string
		busy, known bool
	}{{idle, false, true}, {tool, true, true}, {asked, true, true}, {filepath.Join(dir, "none"), false, false}} {
		if b, k := Busy(c.p); b != c.busy || k != c.known {
			t.Errorf("%s: busy %v known %v", filepath.Base(c.p), b, k)
		}
	}
}
