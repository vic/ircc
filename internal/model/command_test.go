package model

import (
	"slices"
	"strings"
	"testing"
)

func TestParseCmd(t *testing.T) {
	tests := []struct {
		in      string
		name    CmdName
		args    []string
		wantErr bool
	}{
		{"/join &x", CmdJoin, []string{"&x"}, false},
		{":join &x", CmdJoin, []string{"&x"}, false},
		{"/kick bob", CmdKick, []string{"bob"}, false},
		{"/invite bob &x", CmdInvite, []string{"bob", "&x"}, false},
		{"/msg bob hi there", CmdMsg, []string{"bob", "hi there"}, false},
		{"/msg #a  two  spaces", CmdMsg, []string{"#a", "two  spaces"}, false},
		{"/msg\tbob hi", CmdMsg, []string{"bob", "hi"}, false},
		{"/topic a  b", CmdTopic, []string{"a  b"}, false},
		{"/topic", "", nil, true},
		{"/msg bob\thi", CmdMsg, []string{"bob", "hi"}, false},
		{"/whois bob", CmdWhois, []string{"bob"}, false},
		{"/quit", CmdQuit, nil, false},
		{":q", CmdQuit, nil, false},
		{"/rm &x", CmdClose, []string{"&x"}, false},
		{"/foo", "", nil, true},
		{"/mode x", "", nil, true}, // /mode dropped (vic 2026-10-22)
		{"/mode", "", nil, true},
		{"/thinking on", "", nil, true}, // dropped (vic 2026-10-22)
		{"/join", "", nil, true},
		{"/join &a &b", "", nil, true},
		{"/kick", "", nil, true},
		{"/kick a b", "", nil, true},
		{"/invite bob", "", nil, true},
		{"/invite a b c", "", nil, true},
		{"/msg bob", "", nil, true},
		{"/msg bob   ", "", nil, true},
		{"/", "", nil, true},
		{"", "", nil, true},
		{"hello", "", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseCmd(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if got.Name() != tc.name || !slices.Equal(got.Args(), tc.args) {
				t.Fatalf("got %q %q want %q %q", got.Name(), got.Args(), tc.name, tc.args)
			}
		})
	}
}

func TestCmdWordsAllParse(t *testing.T) {
	for _, w := range CmdWords {
		if _, err := ParseCmd("/" + w); err != nil && strings.Contains(err.Error(), "unknown command") {
			t.Errorf("CmdWords has %q, ParseCmd does not know it", w)
		}
	}
	if _, err := ParseCmd("/nosuch"); err == nil || !strings.Contains(err.Error(), "unknown command") { // control
		t.Fatalf("control: %v", err)
	}
}

func TestUsageOnBadArgs(t *testing.T) {
	for line, want := range map[string]string{"/join": "usage: /join &room", "/invite dave": "usage: /invite <nick> &room", "/msg": "usage: /msg <nick> <text>"} {
		if _, err := ParseCmd(line); err == nil || err.Error() != want {
			t.Errorf("%s: %v", line, err)
		}
	}
	for _, w := range CmdWords { // every command word has a usage
		c, _ := ParseCmd("/" + w)
		if n := CmdName(w); usage[n] == "" && usage[c.Name()] == "" {
			t.Errorf("no usage for %s", w)
		}
	}
}

// TestNewCmd: typed args (MCP, I4) follow ParseCmd's arity rules.
func TestNewCmd(t *testing.T) {
	tests := []struct {
		name    CmdName
		args    []string
		line    string
		wantErr bool
	}{
		{CmdJoin, []string{"&x"}, "/join &x", false},
		{CmdInvite, []string{"lib1", "&review"}, "/invite lib1 &review", false},
		{CmdMsg, []string{"#a", "two  words\nand a line"}, "/msg #a two  words\nand a line", false},
		{CmdTopic, []string{"ship it"}, "/topic ship it", false},
		{CmdClose, []string{"&x"}, "/close &x", false},
		{CmdPart, nil, "/part", false},
		{CmdJoin, nil, "", true},
		{CmdJoin, []string{""}, "", true},
		{CmdJoin, []string{"&a b"}, "", true}, // only a raw last arg may hold spaces
		{CmdInvite, []string{"a b", "&x"}, "", true},
		{CmdMsg, []string{"#a", ""}, "", true},
		{"nosuch", []string{"x"}, "", true},
	}
	for _, tc := range tests {
		got, err := NewCmd(tc.name, tc.args...)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s %q: err=%v wantErr=%v", tc.name, tc.args, err, tc.wantErr)
		}
		if err == nil && (got.Name() != tc.name || !slices.Equal(got.Args(), tc.args) || got.String() != tc.line) {
			t.Fatalf("%s %q: got %q %q %q", tc.name, tc.args, got.Name(), got.Args(), got.String())
		}
	}
}
