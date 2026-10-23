package registry

import (
	"os"
	"strconv"
	"testing"
)

const fakeRoot = "testdata/live"

func TestProcStart(t *testing.T) {
	tests := []struct {
		name    string
		pid     int
		want    string
		wantErr bool
	}{
		{"plain comm", 100, "12345", false},
		{"comm with spaces and parens", 200, "777", false},
		{"truncated stat", 300, "", true},
		{"missing pid", 999, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ProcStart(fakeRoot, tc.pid)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("got (%q, %v), want (%q, err=%v)", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestAlive(t *testing.T) {
	self := os.Getpid()
	// fake root lacks self; use real /proc for the live-pid rows.
	start, err := ProcStart("/proc", self)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		pid       int
		procStart string
		root      string
		want      bool
	}{
		{"live pid, matching procStart", self, start, "/proc", true},
		{"live pid, procStart mismatch", self, start + "0", "/proc", false},
		{"live pid, procStart \"1\" (E5)", self, "1", "/proc", false},
		{"pid 0 (process group)", 0, start, "/proc", false},
		{"pid -1 (all processes)", -1, start, "/proc", false},
		{"dead pid", 0x7ffffff0, "12345", fakeRoot, false},
		{"live pid, fake stat missing", self, start, fakeRoot, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Alive(Entry{PID: tc.pid, ProcStart: tc.procStart}, tc.root); got != tc.want {
				t.Fatalf("Alive(%d,%q)=%v want %v (pid %s)", tc.pid, tc.procStart, got, tc.want, strconv.Itoa(tc.pid))
			}
		})
	}
}
