package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func deadPID(t *testing.T) int {
	t.Helper()
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	return c.Process.Pid // reaped by Run
}

func selfEntry(t *testing.T, sock string) Entry {
	t.Helper()
	self := os.Getpid()
	start, err := ProcStart("/proc", self)
	if err != nil {
		t.Fatal(err)
	}
	return Entry{PID: self, Kind: Bg, Status: Idle, SocketPath: sock, ProcStart: start}
}

func exists(dir string, pid int) bool {
	_, err := os.Lstat(filepath.Join(dir, entryFile(pid)))
	return err == nil
}

func TestWriteRemoveOwn(t *testing.T) {
	dir := t.TempDir()
	e := selfEntry(t, "/s/ircc-x.sock")
	if err := WriteOwn(dir, e); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, entryFile(e.PID)))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v err %v", fi, err)
	}
	got, errs := Scan(dir, "/proc")
	if len(errs) != 0 || len(got) != 1 || got[0].SocketPath != e.SocketPath {
		t.Fatalf("scan %+v %v", got, errs)
	}
	if err := RemoveOwn(dir, e.PID); err != nil || exists(dir, e.PID) {
		t.Fatalf("remove: %v", err)
	}
	if err := RemoveOwn(dir, e.PID); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}

func TestWriteOwnRejectsInvalid(t *testing.T) {
	for _, e := range []Entry{
		{PID: 0, SocketPath: "/s/ircc-a.sock", ProcStart: "5"},
		{PID: 5},
		{PID: 5, SocketPath: "/s/ircc-a.sock"}, // empty ProcStart
		{PID: 5, SocketPath: "/s/5.sock", ProcStart: "5"}, // foreign socket name
	} {
		if err := WriteOwn(t.TempDir(), e); err == nil {
			t.Errorf("want error for %+v", e)
		}
	}
}

func TestRemoveStale(t *testing.T) {
	dir := t.TempDir()
	dead := deadPID(t)
	put := func(e Entry) {
		t.Helper()
		if err := WriteOwn(dir, e); err != nil {
			t.Fatal(err)
		}
	}
	live := selfEntry(t, "/s/ircc-live.sock")
	put(live) // live own: kept
	put(Entry{PID: dead, Kind: Bg, SocketPath: "/s/ircc-dead.sock", ProcStart: "5"})
	liveStart, err := ProcStart("/proc", 1)
	if err != nil {
		t.Fatal(err)
	}
	putRaw(t, dir, 1, liveStart, "/s/1.sock") // live foreign
	foreignDead := deadPID(t)
	putRaw(t, dir, foreignDead, "5", "/s/123.sock")
	if err := os.WriteFile(filepath.Join(dir, "7.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "t")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "8.json")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveStale(dir, "/proc"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		pid  int
		want bool
	}{
		{"live own kept", live.PID, true},
		{"dead own removed", dead, false},
		{"live foreign kept", 1, true},
		{"dead foreign kept", foreignDead, true},
		{"unparsable kept", 7, true},
		{"symlink kept", 8, true},
	} {
		if exists(dir, c.pid) != c.want {
			t.Errorf("%s: exists=%v want %v", c.name, !c.want, c.want)
		}
	}
}

func putRaw(t *testing.T, dir string, pid int, procStart, sock string) {
	t.Helper()
	body := fmt.Sprintf(`{"pid":%d,"kind":"bg","status":"idle","messagingSocketPath":%q,"procStart":%q}`, pid, sock, procStart)
	if err := os.WriteFile(filepath.Join(dir, entryFile(pid)), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWriteOwnLeavesNoTmp(t *testing.T) {
	dir := t.TempDir()
	if err := WriteOwn(dir, selfEntry(t, "/s/ircc-x.sock")); err != nil {
		t.Fatal(err)
	}
	des, err := os.ReadDir(dir)
	if err != nil || len(des) != 1 {
		t.Fatalf("dir %v %v", des, err)
	}
}

func TestRemoveStaleTmp(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{".5.json.abc.tmp", ".x.tmp", "other.tmp"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveStale(dir, "/proc"); err != nil {
		t.Fatal(err)
	}
	for n, want := range map[string]bool{".5.json.abc.tmp": false, ".x.tmp": true, "other.tmp": true} {
		_, err := os.Lstat(filepath.Join(dir, n))
		if (err == nil) != want {
			t.Errorf("%s exists=%v want %v", n, err == nil, want)
		}
	}
}

func TestRemoveOwnIf(t *testing.T) {
	dir := t.TempDir()
	e := selfEntry(t, "/s/ircc-x.sock")
	if err := WriteOwn(dir, e); err != nil {
		t.Fatal(err)
	}
	// foreign entry for the same pid (socket differs): survives
	other := e
	other.SocketPath = "/s/ircc-y.sock"
	if err := RemoveOwnIf(dir, other); err != nil || !exists(dir, e.PID) {
		t.Fatalf("different socket removed: %v", err)
	}
	// procStart differs (pid reuse): survives
	other = e
	other.ProcStart += "0"
	if err := RemoveOwnIf(dir, other); err != nil || !exists(dir, e.PID) {
		t.Fatalf("different procStart removed: %v", err)
	}
	if err := RemoveOwnIf(dir, e); err != nil || exists(dir, e.PID) {
		t.Fatalf("exact match not removed: %v", err)
	}
	if err := RemoveOwnIf(dir, e); err != nil {
		t.Fatalf("already gone: %v", err)
	}
	// unparsable file: left alone, no error
	if err := os.WriteFile(filepath.Join(dir, entryFile(e.PID)), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveOwnIf(dir, e); err != nil || !exists(dir, e.PID) {
		t.Fatalf("unparsable removed: %v", err)
	}
}

func TestHasOwn(t *testing.T) {
	dir := t.TempDir()
	e := selfEntry(t, "/s/ircc-x.sock")
	if HasOwn(dir, e.PID) {
		t.Fatal("empty dir")
	}
	if err := WriteOwn(dir, e); err != nil {
		t.Fatal(err)
	}
	if !HasOwn(dir, e.PID) {
		t.Fatal("ircc entry not seen")
	}
	foreign := `{"pid":` + fmt.Sprint(e.PID+1) + `,"kind":"bg","status":"idle","messagingSocketPath":"/run/cc-socks/1.sock","procStart":"1"}`
	if err := os.WriteFile(filepath.Join(dir, entryFile(e.PID+1)), []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if HasOwn(dir, e.PID+1) {
		t.Fatal("foreign entry counted as ircc's")
	}
}

func TestWriteOwnStartedAt(t *testing.T) {
	dir := t.TempDir()
	before := time.Now().UnixMilli()
	e := selfEntry(t, "/s/ircc-x.sock")
	if err := WriteOwn(dir, e); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, entryFile(e.PID)))
	var got struct {
		StartedAt int64 `json:"startedAt"`
	}
	if err != nil || json.Unmarshal(b, &got) != nil || got.StartedAt < before || got.StartedAt > time.Now().UnixMilli() {
		t.Fatalf("startedAt %d (before %d) err %v: %s", got.StartedAt, before, err, b)
	}
}
