package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"ircc/internal/peer"
	"ircc/internal/registry"
	"ircc/internal/rooms"
)

type rtFix struct {
	rt               *roomRT
	dir, socks, sess string
	spawned          *int
}

// newRT builds a roomRT over short /tmp dirs; keepers are real `keep` re-execs of the test binary.
func newRT(t *testing.T) rtFix {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "rt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	f := rtFix{dir: filepath.Join(root, "rooms"), socks: filepath.Join(root, "s"), sess: filepath.Join(root, "sess")}
	for _, d := range []string{f.socks, f.sess} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("IRCC_KEEP_READY", filepath.Join(root, "ready")) // child re-exec runs `keep`
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	f.rt = newRoomRT(f.dir, f.socks, f.sess, "/proc", os.Args[0])
	n := 0
	f.spawned = &n
	spawn := f.rt.spawnKeeper
	f.rt.spawnKeeper = func(self string) (*os.Process, error) { n++; return spawn(self) }
	t.Cleanup(func() { _ = f.rt.shutdown() })
	return f
}

func mustName(t *testing.T, s string) rooms.Name {
	t.Helper()
	n, err := rooms.ParseName(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (f rtFix) entries(t *testing.T) []registry.Entry {
	t.Helper()
	es, errs := registry.Scan(f.sess, "/proc")
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	return es
}

func TestRoomRTOpenClose(t *testing.T) {
	f := newRT(t)
	name := mustName(t, "&review")
	if err := f.rt.open(name, ""); err != nil {
		t.Fatal(err)
	}
	es := f.entries(t)
	sock, _ := peer.EndpointPath(f.socks, "&review")
	if len(es) != 1 || es[0].Name != "&review" || es[0].SocketPath != sock {
		t.Fatalf("entries %+v", es)
	}
	keeper := es[0].PID
	if keeper == os.Getpid() {
		t.Fatal("room entry carries ircc's pid, not a keeper's")
	}
	if _, err := os.Stat(filepath.Join(f.dir, peer.EncName("&review")+".json")); err != nil {
		t.Fatalf("room file: %v", err)
	}
	if !reflect.DeepEqual(f.rt.names(), []string{"&review"}) {
		t.Fatalf("names %v", f.rt.names())
	}
	if err := f.rt.open(name, ""); err != nil || *f.spawned != 1 { // idempotent: no second keeper
		t.Fatalf("reopen: %v spawned %d", err, *f.spawned)
	}

	if err := rooms.AppendLine(f.dir, name, rooms.LogLine{Time: time.Now(), Nick: "vic", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(f.dir, peer.EncName("&review")+".log")
	if _, err := os.Stat(logFile); err != nil { // control: the log exists before close
		t.Fatal(err)
	}

	if err := f.rt.closeRoom(name); err != nil {
		t.Fatal(err)
	}
	if es := f.entries(t); len(es) != 0 {
		t.Fatalf("entry left: %+v", es)
	}
	for _, p := range []string{sock, filepath.Join(f.dir, peer.EncName("&review")+".json"), logFile} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s still exists", p)
		}
	}
	if err := syscall.Kill(keeper, 0); !errors.Is(err, syscall.ESRCH) { // reaped, not a zombie
		t.Fatalf("keeper %d: kill 0 = %v", keeper, err)
	}
	if len(f.rt.names()) != 0 {
		t.Fatalf("names %v", f.rt.names())
	}
}

func TestRoomRTInvalidNameSpawnsNoKeeper(t *testing.T) {
	f := newRT(t)
	if _, err := rooms.ParseName(`&a"b`); err == nil {
		t.Fatal("ParseName accepted a quote")
	}
	if err := f.rt.open(rooms.Name{}, ""); err == nil { // the only way past ParseName is the zero value
		t.Fatal("zero Name opened")
	}
	if *f.spawned != 0 {
		t.Fatalf("spawned %d keepers", *f.spawned)
	}
	if err := f.rt.closeRoom(mustName(t, "&nope")); err == nil {
		t.Fatal("closing an unopened room succeeded")
	}
}

func TestRoomRTLoadOpensSavedRooms(t *testing.T) {
	f := newRT(t)
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"&a", "&b"} {
		if err := rooms.Save(f.dir, rooms.Room{Name: mustName(t, n), Topic: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.dir, "bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	errs := f.rt.load()
	if len(errs) != 1 { // the bad file; the good rooms still open
		t.Fatalf("errs %v", errs)
	}
	if !reflect.DeepEqual(f.rt.names(), []string{"&a", "&b"}) || len(f.entries(t)) != 2 {
		t.Fatalf("names %v entries %d", f.rt.names(), len(f.entries(t)))
	}
	r, ok := f.rt.room(mustName(t, "&a"))
	if !ok || r.Topic != "t" {
		t.Fatalf("room %+v %v", r, ok)
	}
}

func TestRoomRTSpawnRunsOnOneLockedThread(t *testing.T) {
	f := newRT(t)
	// both keepers come through the single spawner goroutine; shutdown stops it
	for _, n := range []string{"&a", "&b"} {
		if err := f.rt.open(mustName(t, n), ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.rt.shutdown(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.rt.stopped:
	case <-time.After(time.Second):
		t.Fatal("spawner goroutine did not stop")
	}
	if len(f.entries(t)) != 0 { // shutdown closed every room endpoint
		t.Fatal("entries left after shutdown")
	}
	if err := f.rt.open(mustName(t, "&c"), ""); !errors.Is(err, errShutdown) { // no spawner: fails, does not hang
		t.Fatalf("open after shutdown: %v", err)
	}
}

func TestRoomRTSetRoom(t *testing.T) {
	f := newRT(t)
	name := mustName(t, "&s")
	if err := f.rt.open(name, ""); err != nil {
		t.Fatal(err)
	}
	r := rooms.Room{Name: name, Topic: "new", Members: []rooms.Member{{Session: "s1", Nick: "a"}}}
	if err := f.rt.setRoom(r); err != nil {
		t.Fatal(err)
	}
	got, _ := f.rt.room(name)
	saved, errs := rooms.Load(f.dir)
	if len(errs) != 0 || got.Topic != "new" || len(saved) != 1 || saved[0].Topic != "new" || len(saved[0].Members) != 1 {
		t.Fatalf("mem %+v disk %+v %v", got, saved, errs)
	}
	// NEG: a failing Save leaves memory unchanged
	if err := os.RemoveAll(f.dir); err != nil {
		t.Fatal(err)
	}
	r.Topic = "lost"
	if err := f.rt.setRoom(r); err == nil {
		t.Fatal("save into a missing dir succeeded")
	}
	if got, _ := f.rt.room(name); got.Topic != "new" {
		t.Fatalf("memory changed on failed save: %+v", got)
	}
	// NEG: a room that is not open is not resurrected
	if err := f.rt.setRoom(rooms.Room{Name: mustName(t, "&ghost")}); err == nil {
		t.Fatal("setRoom of an unopened room succeeded")
	}
}
