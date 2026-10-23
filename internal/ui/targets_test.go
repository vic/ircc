package ui

import (
	"strings"
	"testing"
)

func TestClickTarget(t *testing.T) {
	row := "see `go test ./...` and edit some-file.nix, then ~/x/y.go:12."
	at := func(word string) int { // display column of word in row
		i := strings.Index(row, word)
		if i < 0 {
			t.Fatalf("%q not in row", word)
		}
		return i
	}
	cases := []struct {
		name      string
		col       int
		path, cmd string
		ok        bool
	}{
		{"code span is a command", at("test") + 1, "", "go test ./...", true},
		{"bare file", at("some-file") + 2, "some-file.nix", "", true},
		{"path with line and dot", at("~/x") + 1, "~/x/y.go", "", true},
		{"plain word", at("then"), "", "", false},
		{"sentence word", at("see"), "", "", false},
		{"past the row", len(row) + 5, "", "", false},
		{"border column", -1, "", "", false},
	}
	for _, c := range cases {
		path, cmd, ok := clickTarget(row, c.col)
		if path != c.path || cmd != c.cmd || ok != c.ok {
			t.Errorf("%s: got (%q, %q, %v), want (%q, %q, %v)", c.name, path, cmd, ok, c.path, c.cmd, c.ok)
		}
	}
}
