package transcript

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func appendTo(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestTail(t *testing.T) {
	tests := []struct {
		name    string
		initial string
		steps   []string // appended one by one; Lines() after each
		want    [][]string
	}{
		{"complete lines", "", []string{"a\nb\n"}, [][]string{{"a", "b"}}},
		{"partial held until newline", "", []string{"a\nb", "c\nd\n"}, [][]string{{"a"}, {"bc", "d"}}},
		{"partial only emits nothing", "", []string{"abc"}, [][]string{nil}},
		{"initial content read, no dup", "x\n", []string{"y\n", ""}, [][]string{{"x", "y"}, nil}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "s.jsonl")
			appendTo(t, p, tc.initial)
			tl, err := Open(p, 0)
			if err != nil {
				t.Fatal(err)
			}
			for i, s := range tc.steps {
				appendTo(t, p, s)
				got, err := tl.Lines()
				if err != nil {
					t.Fatal(err)
				}
				var gs []string
				for _, l := range got {
					gs = append(gs, string(l))
				}
				if !reflect.DeepEqual(gs, tc.want[i]) {
					t.Fatalf("step %d: got %q want %q", i, gs, tc.want[i])
				}
			}
		})
	}
}

func TestTailOffsetResume(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	appendTo(t, p, "a\nb\n")
	tl, err := Open(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tl.Lines(); err != nil {
		t.Fatal(err)
	}
	if tl.Offset() != 4 {
		t.Fatalf("offset %d", tl.Offset())
	}
	appendTo(t, p, "c\n")
	t2, err := Open(p, tl.Offset())
	if err != nil {
		t.Fatal(err)
	}
	got, err := t2.Lines()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || string(got[0]) != "c" {
		t.Fatalf("got %q", got)
	}
}

func TestTailOffsetExcludesPartial(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	appendTo(t, p, "a\nb")
	tl, err := Open(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tl.Lines()
	if err != nil || len(got) != 1 || string(got[0]) != "a" {
		t.Fatalf("got %q err %v", got, err)
	}
	if tl.Offset() != 2 {
		t.Fatalf("offset %d", tl.Offset())
	}
	t2, err := Open(p, tl.Offset())
	if err != nil {
		t.Fatal(err)
	}
	appendTo(t, p, "c\n")
	got, err = t2.Lines()
	if err != nil || len(got) != 1 || string(got[0]) != "bc" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestTailTruncated(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	appendTo(t, p, "aaaa\n")
	tl, err := Open(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tl.Lines(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.Lines(); !errors.Is(err, ErrTruncated) {
		t.Fatalf("got %v", err)
	}
}

func TestTailReplacedByRename(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	appendTo(t, p, "aa\n")
	tl, err := Open(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tl.Lines(); err != nil {
		t.Fatal(err)
	}
	n := filepath.Join(dir, "new")
	appendTo(t, n, "{\"b\":22222}\n{\"c\":3}\n")
	if err := os.Rename(n, p); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.Lines(); !errors.Is(err, ErrTruncated) {
		t.Fatalf("got %v", err)
	}
}

// Same inode truncated then regrown past the offset cannot be told apart by
// size; only the rename case is detected.

func TestTailMissing(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "none"), 0); err == nil {
		t.Fatal("want error")
	}
}
