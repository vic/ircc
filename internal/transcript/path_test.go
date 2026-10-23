package transcript

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func mk(t *testing.T, root string, rel ...string) {
	t.Helper()
	for _, r := range rel {
		p := filepath.Join(root, r)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFindGlobCharsInClaudeDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "[x]")
	mk(t, root, "projects/-a/s1.jsonl")
	got, err := Find(root, "s1")
	if err != nil || got != filepath.Join(root, "projects/-a/s1.jsonl") {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestFind(t *testing.T) {
	tests := []struct {
		name    string
		files   []string
		id      string
		want    string // relative to root
		wantErr error
	}{
		{"one match", []string{"projects/-a/s1.jsonl"}, "s1", "projects/-a/s1.jsonl", nil},
		{"picks right id", []string{"projects/-a/s1.jsonl", "projects/-b/s2.jsonl"}, "s2", "projects/-b/s2.jsonl", nil},
		{"zero matches", []string{"projects/-a/s1.jsonl"}, "zz", "", ErrNotFound},
		{"two matches", []string{"projects/-a/s1.jsonl", "projects/-b/s1.jsonl"}, "s1", "", ErrAmbiguous},
		{"slash", []string{"projects/-a/s1.jsonl"}, "-a/s1", "", ErrBadSessionID},
		{"dotdot", []string{"projects/-a/s1.jsonl"}, "..", "", ErrBadSessionID},
		{"star", []string{"projects/-a/s1.jsonl"}, "*", "", ErrBadSessionID},
		{"question", []string{"projects/-a/s1.jsonl"}, "s?", "", ErrBadSessionID},
		{"bracket", []string{"projects/-a/s1.jsonl"}, "s[1]", "", ErrBadSessionID},
		{"backslash", []string{"projects/-a/s1.jsonl"}, `s\1`, "", ErrBadSessionID},
		{"empty", nil, "", "", ErrBadSessionID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			mk(t, root, tc.files...)
			got, err := Find(root, tc.id)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err %v want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && got != filepath.Join(root, tc.want) {
				t.Fatalf("got %q", got)
			}
		})
	}
}
