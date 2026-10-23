package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"ircc/internal/rooms"
	"syscall"
)

// The test binary doubles as the keeper child: the real run(["keep"]) path.
func TestMain(m *testing.M) {
	if ready := os.Getenv("IRCC_KEEP_READY"); ready != "" {
		os.Exit(run([]string{"keep"}, func() { os.WriteFile(ready, nil, 0o600) }))
	}
	os.Exit(m.Run())
}

func TestKeeperSigterm(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv("IRCC_KEEP_READY", ready)
	t.Setenv("GORACE", "atexit_sleep_ms=0") // race runtime otherwise sleeps ~1s at exit; child inherits env
	p, err := rooms.SpawnKeeper(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *os.ProcessState, 1)
	go func() { st, _ := p.Wait(); done <- st }()
	deadline := time.After(time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case <-tick.C:
		case <-deadline:
			p.Kill()
			t.Fatal("keeper never became ready")
		}
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("keeper not alive: %v", err)
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case st := <-done:
		if st == nil || st.ExitCode() != 0 {
			t.Fatalf("want exit 0, got %v", st)
		}
	case <-time.After(time.Second):
		p.Kill()
		t.Fatal("keeper ignored SIGTERM")
	}
}
