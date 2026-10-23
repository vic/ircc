package registry

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func entryJSON(pid int, procStart string) string {
	return fmt.Sprintf(`{"pid":%d,"kind":"bg","status":"idle","messagingSocketPath":"/s/%d.sock","procStart":%q}`, pid, pid, procStart)
}

func TestScan(t *testing.T) {
	self := os.Getpid()
	start, err := ProcStart("/proc", self)
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	dead := c.Process.Pid // reaped by Run

	dir := t.TempDir()
	w := func(name, body string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	w(fmt.Sprintf("%d.json", self), entryJSON(self, start), 0o600)
	w(fmt.Sprintf("%d.json", dead), entryJSON(dead, "5"), 0o600)
	w("1.json", entryJSON(1, "1"), 0o600) // procStart mismatch (E5)
	w("ircc-probe.json", entryJSON(self, start), 0o600)
	w("7.json", `{`, 0o600) // bad JSON -> errs
	w(fmt.Sprintf("%d.abc.key", self), "secret", 0o000)

	got, errs := Scan(dir, "/proc")
	if len(got) != 1 || got[0].PID != self {
		t.Fatalf("want only self live, got %+v", got)
	}
	if len(errs) != 1 {
		t.Fatalf("want 1 error (bad JSON), got %v", errs)
	}
}

func TestScanMissingDir(t *testing.T) {
	got, errs := Scan(filepath.Join(t.TempDir(), "nope"), "/proc")
	if len(got) != 0 || len(errs) != 1 {
		t.Fatalf("got %v, %v", got, errs)
	}
}

func TestScanSkipsNonRegular(t *testing.T) {
	self := os.Getpid()
	start, err := ProcStart("/proc", self)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "real.json")
	if err := os.WriteFile(target, []byte(entryJSON(self, start)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, fmt.Sprintf("%d.json", self))); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "8.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	type res struct {
		got  []Entry
		errs []error
	}
	ch := make(chan res, 1)
	go func() { g, e := Scan(dir, "/proc"); ch <- res{g, e} }()
	select {
	case r := <-ch:
		if len(r.got) != 0 || len(r.errs) != 0 {
			t.Fatalf("got %+v, %v", r.got, r.errs)
		}
	case <-time.After(time.Second):
		t.Fatal("Scan blocked on FIFO")
	}
}

func TestScanNoLeakNoFollow(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.key")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "123.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "124.json"), []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errs := Scan(dir, "/proc")
	if len(errs) != 1 { // only the regular invalid file
		t.Fatalf("errs %v", errs)
	}
	if strings.Contains(errs[0].Error(), "S") {
		t.Fatalf("error leaks content: %v", errs[0])
	}
}

func TestReadEntryRejectsSymlinkAndFIFO(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "t")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ln := filepath.Join(dir, "ln")
	if err := os.Symlink(target, ln); err != nil {
		t.Fatal(err)
	}
	ff := filepath.Join(dir, "ff")
	if err := syscall.Mkfifo(ff, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{ln, ff} {
		if _, err := readEntry(p); !errors.Is(err, errNotRegular) {
			t.Errorf("%s: got %v", p, err)
		}
	}
}

func TestScanOversize(t *testing.T) {
	dir := t.TempDir()
	big := append([]byte(`{"pad":"`), bytes.Repeat([]byte("a"), maxEntry)...)
	if err := os.WriteFile(filepath.Join(dir, "125.json"), append(big, '"', '}'), 0o600); err != nil {
		t.Fatal(err)
	}
	got, errs := Scan(dir, "/proc")
	if len(got) != 0 || len(errs) != 1 || !strings.Contains(errs[0].Error(), "too large") {
		t.Fatalf("got %v, %v", got, errs)
	}
}

func domainJSON(pid int, procStart, domain string) string {
	return fmt.Sprintf(`{"pid":%d,"kind":"bg","status":"idle","messagingSocketPath":"/s/%d.sock","procStart":%q,"pidDomain":%q}`, pid, pid, procStart, domain)
}

func TestScanPIDDomain(t *testing.T) {
	self := os.Getpid()
	selfStart, err := ProcStart("/proc", self)
	if err != nil {
		t.Fatal(err)
	}
	var kids []int
	var starts []string
	for i := 0; i < 2; i++ {
		c := exec.Command("sleep", "30")
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Process.Kill(); c.Wait() })
		s, err := ProcStart("/proc", c.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		kids, starts = append(kids, c.Process.Pid), append(starts, s)
	}
	pids := []int{self, kids[0], kids[1]}
	ss := []string{selfStart, starts[0], starts[1]}
	for _, tc := range []struct {
		name    string
		domains [3]string
		keep    []int // indexes into pids
		errs    int
	}{
		{"same domain kept", [3]string{"a", "a", "a"}, []int{0, 1, 2}, 0},
		{"minority dropped with err", [3]string{"a", "a", "b"}, []int{0, 1}, 1},
		{"all empty kept", [3]string{"", "", ""}, []int{0, 1, 2}, 0},
		{"empty always kept", [3]string{"a", "a", ""}, []int{0, 1, 2}, 0},
		{"tie: keep all, one error", [3]string{"a", "b", ""}, []int{0, 1, 2}, 1},
		{"no strict majority of three: keep all, one error", [3]string{"a", "b", "c"}, []int{0, 1, 2}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for i, p := range pids {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", p)), []byte(domainJSON(p, ss[i], tc.domains[i])), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, errs := Scan(dir, "/proc")
			var want []int
			for _, i := range tc.keep {
				want = append(want, pids[i])
			}
			slices.Sort(want)
			var gotPIDs []int
			for _, e := range got {
				gotPIDs = append(gotPIDs, e.PID)
			}
			if !slices.Equal(gotPIDs, want) || len(errs) != tc.errs {
				t.Fatalf("kept %v errs %v, want %v and %d errs", gotPIDs, errs, want, tc.errs)
			}
			for _, e := range errs {
				if strings.Contains(e.Error(), `"a"`) || strings.Contains(e.Error(), `"b"`) {
					t.Errorf("error echoes domain value: %v", e)
				}
			}
		})
	}
}

func TestScanOwnEntriesDoNotVote(t *testing.T) {
	self := os.Getpid()
	selfStart, err := ProcStart("/proc", self)
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Process.Kill(); c.Wait() })
	kidStart, err := ProcStart("/proc", c.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// self is ircc's own endpoint (domain "own"), the child a session (domain "a"):
	// own does not vote, so "a" is a strict majority of one and nobody is dropped.
	own := fmt.Sprintf(`{"pid":%d,"kind":"bg","status":"idle","messagingSocketPath":"/s/ircc-u.sock","procStart":%q,"pidDomain":"own"}`, self, selfStart)
	kid := domainJSON(c.Process.Pid, kidStart, "a")
	for pid, body := range map[int]string{self: own, c.Process.Pid: kid} {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", pid)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, errs := Scan(dir, "/proc")
	if len(got) != 2 || len(errs) != 0 {
		t.Fatalf("got %d entries, errs %v; want both kept, no errors", len(got), errs)
	}
}
