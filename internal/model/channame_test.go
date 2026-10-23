package model

import (
	"reflect"
	"testing"
)

func TestChanNames(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []Session
		want map[int]string
	}{
		{"spaces", []Session{{1, "Replace Z with Natives", "/x"}}, map[int]string{1: "#replace-z-with-natives"}},
		{"run of space comma ctrl", []Session{{1, "A, \x07B", "/x"}}, map[int]string{1: "#a-b"}},
		{"dup after mapping", []Session{{1, "A B", "/x"}, {2, "a-b", "/y"}, {3, "c", "/z"}},
			map[int]string{1: "#a-b~1", 2: "#a-b~2", 3: "#c"}},
		{"empty -> cwd basename", []Session{{1, "", "/home/v/proj"}}, map[int]string{1: "#proj"}},
		{"NEG only spaces -> basename", []Session{{1, "  ", "/home/v/proj"}}, map[int]string{1: "#proj"}},
		{"basename collides with name", []Session{{1, "", "/a/proj"}, {2, "proj", "/b"}},
			map[int]string{1: "#proj~1", 2: "#proj~2"}},
		{"cwd empty", []Session{{7, "", ""}}, map[int]string{7: "#7"}},
		{"cwd root", []Session{{8, "", "/"}}, map[int]string{8: "#8"}},
		{"only commas", []Session{{9, ",,,", "/"}}, map[int]string{9: "#9"}},
		{"only BEL", []Session{{10, "\x07", "/h/p"}}, map[int]string{10: "#p"}},
		{"BEL no cwd", []Session{{11, "\x07", ""}}, map[int]string{11: "#11"}},
		{"trim ends", []Session{{12, ",a b,", "/x"}}, map[int]string{12: "#a-b"}},
		{"none", nil, map[int]string{}},
	} {
		if got := ChanNames(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
