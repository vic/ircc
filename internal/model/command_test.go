package model

import (
	"slices"
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
		{"/mode bypass", CmdMode, []string{"bypass"}, false},
		{"/mode prompting", CmdMode, []string{"prompting"}, false},
		{"/thinking on", CmdThinking, []string{"on"}, false},
		{"/thinking off", CmdThinking, []string{"off"}, false},
		{"/foo", "", nil, true},
		{"/mode x", "", nil, true},
		{"/mode", "", nil, true},
		{"/thinking maybe", "", nil, true},
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
