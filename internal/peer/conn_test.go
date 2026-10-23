package peer

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSendMissing(t *testing.T) {
	if err := Send(filepath.Join(t.TempDir(), "none.sock"), []byte("x\n")); err == nil {
		t.Fatal("want error for missing socket")
	}
}

func TestListenTwoLinesPidMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.sock")
	l, err := Listen(p)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v err %v", fi, err)
	}
	done := make(chan error, 1)
	stop := time.AfterFunc(time.Second, func() { l.Close() })
	defer stop.Stop()
	go func() {
		err := Send(p, []byte("one\ntwo\n"))
		if err != nil {
			l.Close()
		}
		done <- err
	}()
	c, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	pid, err := c.PeerPID()
	if err != nil || pid != os.Getpid() {
		t.Fatalf("pid %d err %v", pid, err)
	}
	sc := c.Lines()
	var got []string
	for sc.Scan() {
		got = append(got, sc.Text())
	}
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("lines %q err %v", got, sc.Err())
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("socket not removed")
	}
}

func TestLinesLimit(t *testing.T) {
	for _, c := range []struct {
		name string
		n    int
		ok   bool
	}{
		{"above 64KiB below max", 100 << 10, true},
		{"above max", maxFrame + 1, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "a.sock")
			l, err := Listen(p)
			if err != nil {
				t.Fatal(err)
			}
			stop := time.AfterFunc(time.Second, func() { l.Close() })
			defer stop.Stop()
			defer l.Close()
			go func() {
				if Send(p, append(bytes.Repeat([]byte("x"), c.n), '\n')) != nil {
					l.Close()
				}
			}()
			conn, err := l.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			sc := conn.Lines()
			got := sc.Scan()
			if got != c.ok || (c.ok && len(sc.Text()) != c.n) || (!c.ok && !errors.Is(sc.Err(), bufio.ErrTooLong)) {
				t.Fatalf("scan=%v len=%d err=%v", got, len(sc.Text()), sc.Err())
			}
		})
	}
}

func TestListenStale(t *testing.T) {
	t.Run("stale socket replaced", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "a.sock")
		s, err := net.ListenUnix("unix", &net.UnixAddr{Name: p, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		s.SetUnlinkOnClose(false)
		s.Close()
		l, err := Listen(p)
		if err != nil {
			t.Fatal(err)
		}
		l.Close()
	})
	t.Run("live socket refused", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "a.sock")
		l, err := Listen(p)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		if l2, err := Listen(p); err == nil {
			l2.Close()
			t.Fatal("want in-use error")
		}
	})
	t.Run("regular file kept", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "a.sock")
		if err := os.WriteFile(p, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if l, err := Listen(p); err == nil {
			l.Close()
			t.Fatal("want error")
		}
		if b, err := os.ReadFile(p); err != nil || string(b) != "keep" {
			t.Fatalf("file lost: %q %v", b, err)
		}
	})
}

func TestListenerCloseIdempotent(t *testing.T) {
	dir, err := os.MkdirTemp("", "lc")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	l, err := Listen(filepath.Join(dir, "a.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close: %v, want nil", err)
	}
	if _, err := l.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after Close: %v, want net.ErrClosed", err)
	}
}
