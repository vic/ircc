package e2e_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ircc/internal/peer"
)

const hopChain = "ff91b42a87afde0d1a72bfe2" // E10

// maxSunPath is the longest bindable AF_UNIX path.
const maxSunPath = 107

// ccSocks is a short private dir standing in for cc-socks/ (sun_path limit).
func ccSocks(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "s3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

// listen binds EndpointPath(dir, name) and arms a kill timer so a stuck
// Accept cannot hang the test.
func listen(t *testing.T, dir, name string, kill time.Duration) (*peer.Listener, string, peer.Addr) {
	t.Helper()
	p, err := peer.EndpointPath(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	return listenAt(t, p, kill)
}

// listenAt binds path (any socket path) with the same kill timer.
func listenAt(t *testing.T, p string, kill time.Duration) (*peer.Listener, string, peer.Addr) {
	t.Helper()
	if len(p) > maxSunPath {
		t.Fatalf("socket path %d bytes exceeds %d", len(p), maxSunPath)
	}
	l, err := peer.Listen(p)
	if err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(kill, func() { l.Close() })
	t.Cleanup(func() { timer.Stop(); l.Close() })
	a, err := peer.ParseAddr("uds:" + p)
	if err != nil {
		t.Fatal(err)
	}
	return l, p, a
}

// readOne accepts one connection and returns its first line and peer pid.
// It reports failures as errors so it is safe off the test goroutine.
func readOne(l *peer.Listener) ([]byte, int, error) {
	c, err := l.Accept()
	if err != nil {
		return nil, 0, fmt.Errorf("accept: %w", err)
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(time.Second)); err != nil {
		return nil, 0, err
	}
	pid, err := c.PeerPID()
	if err != nil {
		return nil, 0, err
	}
	sc := c.Lines()
	if !sc.Scan() {
		return nil, 0, fmt.Errorf("no line: %w", errors.Join(sc.Err(), errors.New("eof")))
	}
	return append(bytes.Clone(sc.Bytes()), '\n'), pid, nil
}

// withHop puts hop-chain="hc" inside the wrapper, between from and from-name,
// as real replies do (D:55, E10). In the JSON line the wrapper quotes are escaped.
func withHop(t *testing.T, line []byte, hc string) []byte {
	t.Helper()
	const at = ` from-name=`
	if bytes.Count(line, []byte(at)) != 1 {
		t.Fatalf("want exactly one %q in %s", at, line)
	}
	return bytes.Replace(line, []byte(at), []byte(` hop-chain=\"`+hc+`\"`+at), 1)
}

// TestMain doubles as the sender child: with S3_SEND=<sock> it sends the frame
// in S3_FRAME to that socket and exits, so the receiver sees a foreign pid.
func TestMain(m *testing.M) {
	if sock := os.Getenv("S3_SEND"); sock != "" {
		if err := peer.Send(sock, []byte(os.Getenv("S3_FRAME")+"\n")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// sendFromChild re-execs the test binary to send f to sock and returns the child's pid.
func sendFromChild(t *testing.T, sock string, f peer.Frame) int {
	t.Helper()
	c := exec.Command(os.Args[0])
	// GORACE: skip the race runtime's 1s exit sleep so the child exits at once.
	c.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0", "S3_SEND="+sock, "S3_FRAME="+strings.TrimSuffix(string(f.Bytes()), "\n"))
	timer := time.AfterFunc(5*time.Second, func() { _ = c.Process.Kill() })
	defer timer.Stop()
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("child sender: %v: %s", err, out)
	}
	return c.Process.Pid
}

func TestS3SendAndReply(t *testing.T) {
	// Chain from S1: the target listens at the live registry entry's socket path.
	_, _, live := fixture(t)
	sockDir := filepath.Dir(live.SocketPath)
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target, targetPath, targetAddr := listenAt(t, live.SocketPath, 2*time.Second)
	userPath, err := peer.EndpointPath(sockDir, "u")
	if err != nil {
		t.Fatal(err)
	}
	user, _, userAddr := listenAt(t, userPath, 2*time.Second)

	class, err := peer.ClassOf("auto")
	if err != nil {
		t.Fatal(err)
	}
	f, err := peer.NewUserFrame(userAddr, "vic", class, "hi")
	if err != nil {
		t.Fatal(err)
	}

	// Target side: read, verify, reply to the From address with a hop-chain
	// inside the wrapper. Errors travel through the channel.
	type result struct {
		in  peer.Inbound
		pid int
		err error
	}
	done := make(chan result, 1)
	go func() {
		line, pid, err := readOne(target)
		var in peer.Inbound
		if err == nil {
			in, err = peer.ParseFrame(line)
		}
		if err == nil {
			var rf peer.Frame
			rf, err = peer.NewUserFrame(targetAddr, "target", peer.Prompting, "pong")
			if err == nil {
				b := bytes.Replace(rf.Bytes(), []byte(` from-name=`), []byte(` hop-chain=\"`+hopChain+`\" from-name=`), 1)
				err = peer.Send(in.From.Path(), b)
			}
		}
		done <- result{in, pid, err}
	}()

	childPID := sendFromChild(t, targetPath, f)
	line, userSidePID, err := readOne(user)
	if err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.in.Body != "hi" || r.in.From != userAddr || r.in.Mode != peer.Prompting {
		t.Fatalf("target got %+v", r.in)
	}
	// F1: SO_PEERCRED names the sending child, not this process.
	if r.pid != childPID || r.pid == os.Getpid() {
		t.Fatalf("target PeerPID=%d want child %d (self %d)", r.pid, childPID, os.Getpid())
	}
	if userSidePID != os.Getpid() {
		t.Fatalf("reply PeerPID=%d want self %d", userSidePID, os.Getpid())
	}
	if !bytes.Contains(line, []byte(`hop-chain=\"`+hopChain+`\" from-name=`)) {
		t.Fatalf("hop-chain not inside the wrapper: %s", line)
	}
	reply, err := peer.ParseFrame(line)
	if err != nil {
		t.Fatalf("reply with hop-chain: %v", err)
	}
	if reply.Body != "pong" || reply.From != targetAddr {
		t.Fatalf("reply %+v", reply)
	}

	// Control: same envelope with a bad hop-chain charset must be rejected,
	// so the accept above is not vacuous.
	bad := withHop(t, f.Bytes(), "a b")
	if _, err := peer.ParseFrame(bad); err == nil {
		t.Fatal("bad hop-chain charset accepted")
	}
	if _, err := peer.ParseFrame(withHop(t, f.Bytes(), hopChain)); err != nil {
		t.Fatalf("good hop-chain rejected: %v", err)
	}
}

func TestS3SendAfterClose(t *testing.T) {
	dir := ccSocks(t)
	user, userPath, userAddr := listen(t, dir, "vic", 2*time.Second)
	f, err := peer.NewUserFrame(userAddr, "vic", peer.Prompting, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if err := user.Close(); err != nil {
		t.Fatal(err)
	}
	if err := peer.Send(userPath, f.Bytes()); err == nil {
		t.Fatal("Send to closed endpoint: want error")
	}
}
