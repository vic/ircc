package ui

import "testing"

func TestKey(t *testing.T) {
	tests := []struct {
		name    string
		m       EditMode
		pending string
		k       string
		act     Action
		mode    EditMode
		pend    string
	}{
		{"i insert", Normal, "", "i", Action{}, Insert, ""},
		{"a insert", Normal, "", "a", Action{}, Insert, ""},
		{"j down", Normal, "", "j", Action{Scroll, 1}, Normal, ""},
		{"k up", Normal, "", "k", Action{Scroll, -1}, Normal, ""},
		{"ctrl+d", Normal, "", "ctrl+d", Action{HalfPage, 1}, Normal, ""},
		{"ctrl+u", Normal, "", "ctrl+u", Action{HalfPage, -1}, Normal, ""},
		{"g pends", Normal, "", "g", Action{}, Normal, "g"},
		{"gg top", Normal, "g", "g", Action{Top, 0}, Normal, ""},
		{"G bottom", Normal, "", "G", Action{Bottom, 0}, Normal, ""},
		{"gt next", Normal, "g", "t", Action{NextChan, 0}, Normal, ""},
		{"gT prev", Normal, "g", "T", Action{PrevChan, 0}, Normal, ""},
		{"3 pends", Normal, "", "3", Action{}, Normal, "3"},
		{"3g pends", Normal, "3", "g", Action{}, Normal, "3g"},
		{"3gt goto", Normal, "3g", "t", Action{GotoChan, 3}, Normal, ""},
		{"12gt goto", Normal, "12g", "t", Action{GotoChan, 12}, Normal, ""},
		{": cmdline", Normal, "", ":", Action{CmdLine, 0}, Normal, ""},
		{"insert esc", Insert, "", "esc", Action{}, Normal, ""},
		{"insert enter", Insert, "", "enter", Action{Send, 0}, Insert, ""},
		{"insert tab", Insert, "", "tab", Action{Complete, 0}, Insert, ""},
		{"insert j is text", Insert, "", "j", Action{}, Insert, ""},
		// NEG
		{"g then x aborts", Normal, "g", "x", Action{}, Normal, ""},
		{"x none", Normal, "", "x", Action{}, Normal, ""},
		{"0 not a count", Normal, "", "0", Action{}, Normal, ""},
		{"3g then x aborts", Normal, "3g", "x", Action{}, Normal, ""},
		{"digit then j aborts", Normal, "3", "j", Action{}, Normal, ""},
		{"esc in normal clears pending", Normal, "g", "esc", Action{}, Normal, ""},
		{"3gg aborts", Normal, "3g", "g", Action{}, Normal, ""},
		{"5th digit aborts", Normal, "9999", "9", Action{}, Normal, ""},
		{"4 digits ok", Normal, "999", "9", Action{}, Normal, "9999"},
		{"/ search out of scope", Normal, "", "/", Action{}, Normal, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, m, p := Key(tc.m, tc.pending, tc.k)
			if a != tc.act || m != tc.mode || p != tc.pend {
				t.Fatalf("got %v %v %q want %v %v %q", a, m, p, tc.act, tc.mode, tc.pend)
			}
		})
	}
}
