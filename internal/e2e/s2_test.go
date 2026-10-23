package e2e_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ircc/internal/transcript"
)

const (
	// GAP-3b: shape unverified.
	userLine = `{"type":"user","permissionMode":"default"}` + "\n"
	// GAP-3b: the permissionMode line shape is unverified against a real transcript.
	modeLine   = `{"type":"user","permissionMode":"bypassPermissions"}` + "\n"
	sessionID  = "0b1c2d3e-aaaa-bbbb-cccc-000000000001"
	halfPrefix = `{"type":"user","permiss`
	halfRest   = `ionMode":"plan"}` + "\n"
)

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func transcriptPath(t *testing.T, claude string) string {
	t.Helper()
	dir := filepath.Join(claude, "projects", "x")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, sessionID+".jsonl")
}

func TestS2(t *testing.T) {
	claude, _, _ := fixture(t)
	path := transcriptPath(t, claude)
	heldLine := heldFixture(t)
	appendTo(t, path, userLine+modeLine+heldLine+halfPrefix)

	found, err := transcript.Find(claude, sessionID)
	if err != nil || found != path {
		t.Fatalf("Find = %q, %v", found, err)
	}
	tl, err := transcript.Open(found, 0)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := tl.Lines()
	if err != nil || len(lines) != 3 { // NEG: the partial line is not yielded
		t.Fatalf("first read: %d lines, %v", len(lines), err)
	}
	var evs []transcript.Event
	for _, l := range lines {
		e, err := transcript.ParseEvent(l)
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, e)
	}
	if got := transcript.LastMode(evs); got != "bypassPermissions" {
		t.Fatalf("LastMode = %q", got)
	}
	if !evs[2].Held || evs[2].HeldFrom != "uds:/tmp/x/ircc-%26review.sock" || evs[0].Held {
		t.Fatalf("held detection: %+v", evs)
	}

	appendTo(t, path, halfRest)
	lines, err = tl.Lines()
	if err != nil || len(lines) != 1 {
		t.Fatalf("second read: %d lines, %v", len(lines), err)
	}
	e, err := transcript.ParseEvent(lines[0])
	if err != nil || e.PermissionMode != "plan" {
		t.Fatalf("finished line: %+v, %v", e, err)
	}

	t.Run("assistant text", func(t *testing.T) {
		t.Skip("GAP-3 vic gate")
	})

	t.Run("rename-replaced file is ErrTruncated", func(t *testing.T) {
		repl := path + ".new"
		appendTo(t, repl, userLine+userLine+userLine+userLine+userLine)
		if err := os.Rename(repl, path); err != nil {
			t.Fatal(err)
		}
		if _, err := tl.Lines(); !errors.Is(err, transcript.ErrTruncated) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestS2FindBadID(t *testing.T) {
	claude, _, _ := fixture(t)
	if _, err := transcript.Find(claude, "../x"); !errors.Is(err, transcript.ErrBadSessionID) {
		t.Fatalf("got %v", err)
	}
}

// heldFixture returns line 1 of the shared held.jsonl fixture (with newline).
func heldFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../transcript/testdata/events/held.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(b), "\n")
	return first + "\n"
}

func TestS2FindNotFoundAndAmbiguous(t *testing.T) {
	claude, _, _ := fixture(t)
	if _, err := transcript.Find(claude, sessionID); !errors.Is(err, transcript.ErrNotFound) {
		t.Fatalf("not found: got %v", err)
	}
	for _, slug := range []string{"x", "y"} {
		dir := filepath.Join(claude, "projects", slug)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		appendTo(t, filepath.Join(dir, sessionID+".jsonl"), userLine)
	}
	if _, err := transcript.Find(claude, sessionID); !errors.Is(err, transcript.ErrAmbiguous) {
		t.Fatalf("ambiguous: got %v", err)
	}
}
