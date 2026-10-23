package peer

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"ircc/internal/registry"
)

func fakeKeeper(t *testing.T) *os.Process {
	t.Helper()
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); _, _ = c.Process.Wait() })
	return c.Process
}

func readEntry(t *testing.T, sessDir string, pid int) registry.Entry {
	t.Helper()
	name := strconv.Itoa(pid) + ".json"
	b, err := os.ReadFile(filepath.Join(sessDir, name))
	if err != nil {
		t.Fatal(err)
	}
	e, err := registry.ParseEntry(name, b)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestOpenUser(t *testing.T) {
	socks, sess := t.TempDir(), t.TempDir()
	ep, err := OpenUser(socks, sess, "/proc", "vic", "dom")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(socks, "ircc-vic.sock")
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket: %v %v", fi, err)
	}
	if ep.Addr.String() != "uds:"+sock || ep.Name != "vic" {
		t.Fatalf("endpoint %+v", ep)
	}
	e := readEntry(t, sess, os.Getpid())
	want, _ := registry.ProcStart("/proc", os.Getpid())
	if e.PIDDomain != "dom" || e.Name != "vic" || e.SocketPath != sock || e.ProcStart != want || e.Kind != registry.Bg || e.PeerProtocol != 1 {
		t.Fatalf("entry %+v", e)
	}
	if err := ep.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket left: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sess, strconv.Itoa(os.Getpid())+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("entry left: %v", err)
	}
}

func TestOpenRoomAndClose(t *testing.T) {
	socks, sess := t.TempDir(), t.TempDir()
	k := fakeKeeper(t)
	ep, err := OpenRoom(socks, sess, "/proc", "&review", k, "dom")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(socks, "ircc-%26review.sock")
	// Claude decodes uds: once (measured 2026-10-22: from=...%26 hit ENOENT on ircc-&review.sock), so "%" goes out as "%25".
	if ep.Addr.Path() != sock || ep.Addr.String() != "uds:"+strings.ReplaceAll(sock, "%", "%25") {
		t.Fatalf("addr %s path %s", ep.Addr, ep.Addr.Path())
	}
	e := readEntry(t, sess, k.Pid)
	want, _ := registry.ProcStart("/proc", k.Pid)
	if e.Name != "&review" || e.PID != k.Pid || e.ProcStart != want || e.PIDDomain != "dom" ||
		e.Kind != registry.Bg || e.Status != registry.Idle || e.PeerProtocol != 1 || len(e.PeerFeatures) != 0 {
		t.Fatalf("entry %+v", e)
	}
	if err := ep.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(k.Pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("keeper still alive/unreaped: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sess, strconv.Itoa(k.Pid)+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("entry left: %v", err)
	}
	if _, err := os.Stat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket left: %v", err)
	}
}

func TestOpenRejects(t *testing.T) {
	socks, sess := t.TempDir(), t.TempDir()
	k := fakeKeeper(t)
	for name, f := range map[string]func() error{
		"empty user":      func() error { _, err := OpenUser(socks, sess, "/proc", "", ""); return err },
		"user with &":     func() error { _, err := OpenUser(socks, sess, "/proc", "&x", ""); return err },
		"room without &":  func() error { _, err := OpenRoom(socks, sess, "/proc", "x", k, ""); return err },
		"room only &":     func() error { _, err := OpenRoom(socks, sess, "/proc", "&", k, ""); return err },
		"room nil keeper": func() error { _, err := OpenRoom(socks, sess, "/proc", "&x", nil, ""); return err },
		"user slash":      func() error { _, err := OpenUser(socks, sess, "/proc", "a/b", ""); return err },
		"dead procRoot":   func() error { _, err := OpenUser(socks, sess, t.TempDir(), "vic", ""); return err },
	} {
		if f() == nil {
			t.Errorf("%s: want error", name)
		}
	}
	// failed opens leave nothing behind
	if des, _ := os.ReadDir(socks); len(des) != 0 {
		t.Errorf("socks dir not clean: %v", des)
	}
	if des, _ := os.ReadDir(sess); len(des) != 0 {
		t.Errorf("session dir not clean: %v", des)
	}
}

const foreignEntry = `{"pid":%d,"kind":"bg","status":"idle","messagingSocketPath":"/run/cc-socks/9.sock","procStart":"1"}`

func TestCloseIdempotentAndSparesForeign(t *testing.T) {
	socks, sess := t.TempDir(), t.TempDir()
	k := fakeKeeper(t)
	ep, err := OpenRoom(socks, sess, "/proc", "&review", k, "dom")
	if err != nil {
		t.Fatal(err)
	}
	if err := ep.Close(); err != nil {
		t.Fatal(err)
	}
	// adversary: pid reused, a foreign entry appears; a second Close must not touch it
	path := filepath.Join(sess, strconv.Itoa(k.Pid)+".json")
	if err := os.WriteFile(path, []byte(fmt.Sprintf(foreignEntry, k.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ep.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("foreign entry removed by second Close: %v", err)
	}
}

func TestCloseSparesReplacedEntry(t *testing.T) {
	socks, sess := t.TempDir(), t.TempDir()
	k := fakeKeeper(t)
	ep, err := OpenRoom(socks, sess, "/proc", "&review", k, "dom")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sess, strconv.Itoa(k.Pid)+".json")
	if err := os.WriteFile(path, []byte(fmt.Sprintf(foreignEntry, k.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ep.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("replaced entry removed: %v", err)
	}
	if err := syscall.Kill(k.Pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("keeper alive: %v", err)
	}
}

func TestOpenRoomFailureKillsKeeper(t *testing.T) {
	socks, sess := t.TempDir(), t.TempDir()
	k := fakeKeeper(t)
	long := "&" + strings.Repeat("a", 200) // sun_path overflow
	if _, err := OpenRoom(socks, sess, "/proc", long, k, ""); err == nil {
		t.Fatal("want error")
	}
	if err := syscall.Kill(k.Pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("keeper survived failed open: %v", err)
	}
	k2 := fakeKeeper(t)
	if _, err := OpenRoom(socks, sess, "/proc", "noamp", k2, ""); err == nil {
		t.Fatal("want error")
	}
	if err := syscall.Kill(k2.Pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("keeper survived rejected name: %v", err)
	}
}

func TestOpenUserTwiceRefused(t *testing.T) {
	socks, sess := t.TempDir(), t.TempDir()
	first, err := OpenUser(socks, sess, "/proc", "vic", "")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := OpenUser(socks, sess, "/proc", "other", ""); err == nil {
		t.Fatal("second user endpoint accepted")
	}
	if _, err := os.Stat(filepath.Join(socks, "ircc-other.sock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second socket left: %v", err)
	}
	if e := readEntry(t, sess, os.Getpid()); e.Name != "vic" {
		t.Fatalf("first entry clobbered: %+v", e)
	}
}

func TestOpenUserOverStaleSocket(t *testing.T) {
	socks, sess := t.TempDir(), t.TempDir()
	sock := filepath.Join(socks, "ircc-vic.sock")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	l.Close() // leaves a stale socket file
	ep, err := OpenUser(socks, sess, "/proc", "vic", "")
	if err != nil {
		t.Fatalf("stale socket blocked open: %v", err)
	}
	ep.Close()
}
