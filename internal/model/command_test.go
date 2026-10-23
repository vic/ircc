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
		{"/who", CmdWho, nil, false},
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
