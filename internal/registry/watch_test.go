package registry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func recvEntries(t *testing.T, ch <-chan Snapshot, d time.Duration) (Snapshot, bool) {
	t.Helper()
	select {
	case s, ok := <-ch:
		return s, ok
	case <-time.After(d):
		return Snapshot{}, false
	}
}

func TestWatch(t *testing.T) {
	self := os.Getpid()
	start, err := ProcStart("/proc", self)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := Watch(ctx, dir, "/proc")
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := recvEntries(t, ch, time.Second); !ok || len(s.Entries) != 0 {
		t.Fatalf("initial scan: got %v ok=%v, want empty", s, ok)
	}

	file := filepath.Join(dir, strconv.Itoa(self)+".json")
	// waitStatus drains events until the entry shows want, or fails at 1s.
	waitStatus := func(want Status) {
		t.Helper()
		deadline := time.After(time.Second)
		for {
			select {
			case s := <-ch:
				if len(s.Entries) == 1 && s.Entries[0].Status == want {
					return
				}
			case <-deadline:
				t.Fatalf("no event with status %v", want)
			}
		}
	}
	if err := os.WriteFile(file, []byte(entryJSON(self, start)), 0o600); err != nil {
		t.Fatal(err)
	}
	waitStatus(Idle)
	busy := strings.Replace(entryJSON(self, start), `"idle"`, `"busy"`, 1)
	if err := os.WriteFile(file, []byte(busy), 0o600); err != nil {
		t.Fatal(err)
	}
	waitStatus(Busy)

	// WriteFile fires several events (truncate, write); drain until quiet.
	for {
		if _, ok := recvEntries(t, ch, 100*time.Millisecond); !ok {
			break
		}
	}
	// NEG: non-.json write emits nothing.
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(self)+".x.key"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, ok := recvEntries(t, ch, 300*time.Millisecond); ok {
		t.Fatalf(".key write emitted %v", s)
	}

	cancel()
	deadline := time.After(time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("channel not closed after cancel")
		}
	}
}

func TestWatchMissingDir(t *testing.T) {
	if _, err := Watch(context.Background(), filepath.Join(t.TempDir(), "nope"), "/proc"); err == nil {
		t.Fatal("want error for missing dir")
	}
}

func TestWatchBurstCoalesces(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := Watch(ctx, dir, "/proc")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := recvEntries(t, ch, time.Second); !ok {
		t.Fatal("no initial snapshot")
	}
	for i := 0; i < 10; i++ {
		if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(1000+i)+".json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for {
		if _, ok := recvEntries(t, ch, 300*time.Millisecond); !ok {
			break
		}
		n++
	}
	if n < 1 || n > 2 {
		t.Fatalf("burst of 10 writes gave %d snapshots, want 1..2", n)
	}
}

func TestWatchDirRemoved(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ch, err := Watch(context.Background(), dir, "/proc")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := recvEntries(t, ch, time.Second); !ok {
		t.Fatal("no initial snapshot")
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	sawErr := false
	for {
		select {
		case s, ok := <-ch:
			if !ok {
				if !sawErr {
					t.Fatal("closed without a final snapshot carrying an error")
				}
				return
			}
			if len(s.Errs) > 0 {
				sawErr = true
			}
		case <-deadline:
			t.Fatal("channel not closed within 1s of dir removal")
		}
	}
}

func TestWatchLoopErrorsResync(t *testing.T) {
	dir := t.TempDir()
	events := make(chan fsnotify.Event)
	errs := make(chan error)
	out := make(chan Snapshot)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchLoop(ctx, dir, "/proc", events, errs, out)
	if _, ok := recvEntries(t, out, time.Second); !ok {
		t.Fatal("no initial snapshot")
	}
	overflow := errors.New("queue overflow")
	errs <- overflow
	s, ok := recvEntries(t, out, time.Second)
	if !ok || !slices.ContainsFunc(s.Errs, func(e error) bool { return errors.Is(e, overflow) }) {
		t.Fatalf("watcher error not resynced into snapshot: %+v ok=%v", s, ok)
	}
	events <- fsnotify.Event{Name: dir, Op: fsnotify.Rename}
	var last Snapshot
	for {
		s, ok := recvEntries(t, out, time.Second)
		if !ok {
			break
		}
		last = s
	}
	if len(last.Errs) == 0 {
		t.Fatal("rename of sessDir: no final error snapshot before close")
	}
}

func TestWatchMaxLatency(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := Watch(ctx, dir, "/proc")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := recvEntries(t, ch, time.Second); !ok {
		t.Fatal("no initial snapshot")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		end := time.After(500 * time.Millisecond)
		for i := 0; ; i++ {
			select {
			case <-tick.C:
				_ = os.WriteFile(filepath.Join(dir, "1000.json"), []byte(strconv.Itoa(i)), 0o600)
			case <-end:
				return
			}
		}
	}()
	n := 0
	for {
		select {
		case <-done:
			if n < 2 {
				t.Fatalf("got %d snapshots while writes were running, want >=2", n)
			}
			return
		case <-ch:
			n++
		}
	}
}
