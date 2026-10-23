package rooms

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func logName(t *testing.T) Name {
	t.Helper()
	n, err := ParseName("&review")
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func ll(i int) LogLine {
	return LogLine{Time: time.Unix(int64(1000+i), 0).UTC(), Nick: "alice", Text: "line " + strconv.Itoa(i)}
}

func TestLogAppendLoad(t *testing.T) {
	dir, n := t.TempDir(), logName(t)
	for i := 0; i < 5; i++ {
		if err := AppendLine(dir, n, ll(i)); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(dir, "%26review.log")
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("log file: %v %v", fi, err)
	}
	got, err := LoadLog(dir, n, 100)
	if err != nil || len(got) != 5 || got[0] != ll(0) || got[4] != ll(4) {
		t.Fatalf("got %+v %v", got, err)
	}
	got, err = LoadLog(dir, n, 2) // cap: the last two, in order
	if err != nil || len(got) != 2 || got[0] != ll(3) || got[1] != ll(4) {
		t.Fatalf("cap: %+v %v", got, err)
	}
}

func TestLogMissingIsEmpty(t *testing.T) {
	got, err := LoadLog(t.TempDir(), logName(t), 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestLogTextKeptVerbatim(t *testing.T) {
	dir, n := t.TempDir(), logName(t)
	l := LogLine{Time: ll(0).Time, Nick: "a", Text: "x\ny\x1b[31m<z>"}
	if err := AppendLine(dir, n, l); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLog(dir, n, 1)
	if err != nil || len(got) != 1 || got[0] != l {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestLogRejects(t *testing.T) {
	dir, n := t.TempDir(), logName(t)
	for _, nick := range []string{"", "a>b", "a\nb"} {
		if err := AppendLine(dir, n, LogLine{Nick: nick, Text: "x"}); err == nil {
			t.Errorf("nick %q: want error", nick)
		}
	}
	if err := AppendLine(dir, Name{}, ll(0)); err == nil {
		t.Error("zero Name: want error")
	}
	if _, err := LoadLog(dir, n, 0); err == nil {
		t.Error("max 0: want error")
	}
}

func TestLogSymlinkRejected(t *testing.T) {
	dir, n := t.TempDir(), logName(t)
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.Symlink(target, filepath.Join(dir, "%26review.log")); err != nil {
		t.Fatal(err)
	}
	if err := AppendLine(dir, n, ll(0)); err == nil {
		t.Fatal("append through symlink: want error")
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("symlink target was created")
	}
	if _, err := LoadLog(dir, n, 5); err == nil {
		t.Fatal("load through symlink: want error")
	}
}

func TestLogMalformedSkippedAndCounted(t *testing.T) {
	dir, n := t.TempDir(), logName(t)
	if err := AppendLine(dir, n, ll(0)); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "%26review.log")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("not json\n{\"time\":\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := AppendLine(dir, n, ll(1)); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLog(dir, n, 10)
	if len(got) != 2 || got[0] != ll(0) || got[1] != ll(1) {
		t.Fatalf("got %+v", got)
	}
	if err == nil || !strings.Contains(err.Error(), "2 malformed") {
		t.Fatalf("want error counting 2 malformed lines, got %v", err)
	}
}

func TestLogReadCappedFromEnd(t *testing.T) {
	dir, n := t.TempDir(), logName(t)
	p := filepath.Join(dir, "%26review.log")
	big := LogLine{Time: ll(0).Time, Nick: "a", Text: strings.Repeat("x", 1000)}
	for i := 0; i < 1200; i++ { // ~1.2 MiB > 1 MiB cap
		if err := AppendLine(dir, n, big); err != nil {
			t.Fatal(err)
		}
	}
	if err := AppendLine(dir, n, ll(7)); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Size() <= maxLogRead {
		t.Fatalf("fixture too small: %d", fi.Size())
	}
	got, err := LoadLog(dir, n, 100000)
	if err != nil { // the cut first line is skipped silently, not counted
		t.Fatalf("err %v", err)
	}
	if len(got) == 0 || got[len(got)-1] != ll(7) || len(got) >= 1201 {
		t.Fatalf("len %d last %+v", len(got), got[len(got)-1])
	}
}

func TestLogCompactsWhenOverMax(t *testing.T) {
	dir, n := t.TempDir(), logName(t)
	p := filepath.Join(dir, "%26review.log")
	big := strings.Repeat("x", 64<<10)
	for i := 0; i < 80; i++ { // ~5 MiB > maxLog
		if err := AppendLine(dir, n, LogLine{Time: ll(i).Time, Nick: "a", Text: big + strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Size() > maxLog+(65<<10) || fi.Mode().Perm() != 0o600 {
		t.Fatalf("size/mode after compaction: %v %v", fi, err)
	}
	got, err := LoadLog(dir, n, 1000)
	if err != nil || len(got) == 0 || len(got) >= 80 {
		t.Fatalf("len %d err %v", len(got), err)
	}
	if last := got[len(got)-1]; !strings.HasSuffix(last.Text, "79") {
		t.Fatalf("newest line lost: %q", last.Text[len(last.Text)-4:])
	}
	for i := 1; i < len(got); i++ { // contiguous tail, no gaps
		if !got[i].Time.After(got[i-1].Time) {
			t.Fatalf("order broken at %d", i)
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("temp file left: %v", ents)
	}
}

func TestRemoveLog(t *testing.T) {
	dir, n := t.TempDir(), logName(t)
	if err := RemoveLog(dir, n); err != nil { // missing is fine
		t.Fatal(err)
	}
	if err := AppendLine(dir, n, ll(0)); err != nil {
		t.Fatal(err)
	}
	if err := RemoveLog(dir, n); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadLog(dir, n, 5); err != nil || len(got) != 0 {
		t.Fatalf("after remove: %+v %v", got, err)
	}
	if err := RemoveLog(dir, Name{}); err == nil {
		t.Fatal("zero Name: want error")
	}
}
