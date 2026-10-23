package e2e_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"ircc/internal/model"
	"ircc/internal/registry"
)

func TestS1(t *testing.T) {
	claude, procRoot, live := fixture(t)
	got, errs := registry.Scan(filepath.Join(claude, "sessions"), procRoot)
	if len(errs) != 0 {
		t.Fatalf("errs %v", errs)
	}
	if len(got) != 1 || got[0].PID != live.PID || got[0].ProcStart != live.ProcStart || got[0].SocketPath != live.SocketPath {
		t.Fatalf("want only live %+v, got %+v", live, got)
	}
	names := model.ChanNames([]model.Session{{PID: got[0].PID, Name: got[0].Name, Cwd: got[0].Cwd}})
	if names[live.PID] != "#review-bot" {
		t.Fatalf("chan name %q", names[live.PID])
	}
}

// Control: a live child with its real procStart is listed, so the mismatch
// row above is dropped for its procStart and not for any other reason.
func TestS1ChildControl(t *testing.T) {
	claude, procRoot, live := fixture(t)
	pid, start := spawnChild(t)
	writeSession(t, claude, pid, start, "child", 0o600, fmt.Sprintf("%d.json", pid))
	got, errs := registry.Scan(filepath.Join(claude, "sessions"), procRoot)
	if len(errs) != 0 || len(got) != 2 {
		t.Fatalf("got %+v errs %v", got, errs)
	}
	for _, e := range got {
		if e.PID != live.PID && e.PID != pid {
			t.Fatalf("unexpected %+v", e)
		}
	}
}
