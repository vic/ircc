package registry

import (
	"slices"
	"strings"
	"testing"
)

const good = `{"pid":42,"sessionId":"s","cwd":"/w","name":"&a","kind":"interactive","status":"idle","messagingSocketPath":"/run/x/42.sock","peerProtocol":1,"peerFeatures":["notify_idle"],"procStart":"123","pidDomain":"d"}`

func TestParseEntry(t *testing.T) {
	rows := []struct {
		name, file, json string
		wantErr          bool
		want             Entry
	}{
		{name: "ok", file: "42.json", json: good, want: Entry{PID: 42, SessionID: "s", Cwd: "/w", Name: "&a",
			Kind: Interactive, Status: Idle, SocketPath: "/run/x/42.sock", PeerProtocol: 1,
			PeerFeatures: []string{"notify_idle"}, ProcStart: "123", PIDDomain: "d"}},
		{name: "bg busy", file: "42.json", json: `{"pid":42,"kind":"bg","status":"busy","messagingSocketPath":"/s","procStart":"1"}`,
			want: Entry{PID: 42, Kind: Bg, Status: Busy, SocketPath: "/s", ProcStart: "1"}},
		{name: "ircc-probe name", file: "ircc-probe.json", json: good, wantErr: true},
		{name: "pid mismatch", file: "43.json", json: good, wantErr: true},
		{name: "no .json", file: "42", json: good, wantErr: true},
		{name: "empty socket", file: "42.json", json: `{"pid":42,"kind":"bg","status":"idle","messagingSocketPath":""}`, wantErr: true},
		{name: "unknown kind", file: "42.json", json: `{"pid":42,"kind":"x","status":"idle","messagingSocketPath":"/s"}`, wantErr: true},
		{name: "unknown status", file: "42.json", json: `{"pid":42,"kind":"bg","status":"x","messagingSocketPath":"/s"}`, wantErr: true},
		{name: "negative pid", file: "-5.json", json: `{"pid":-5,"kind":"bg","status":"idle","messagingSocketPath":"/s"}`, wantErr: true},
		{name: "zero pid", file: "0.json", json: `{"pid":0,"kind":"bg","status":"idle","messagingSocketPath":"/s"}`, wantErr: true},
		{name: "bad json", file: "42.json", json: `{`, wantErr: true},
		{name: "procStart number", file: "42.json", wantErr: true, json: `{"pid":42,"kind":"bg","status":"idle","messagingSocketPath":"/s","procStart":1}`},
		{name: "pidDomain type", file: "42.json", wantErr: true, json: `{"pid":42,"kind":"bg","status":"idle","messagingSocketPath":"/s","pidDomain":{}}`},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			got, err := ParseEntry(r.file, []byte(r.json))
			if (err != nil) != r.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, r.wantErr)
			}
			if err != nil {
				return
			}
			if got.PID != r.want.PID || got.SessionID != r.want.SessionID || got.Cwd != r.want.Cwd ||
				got.Name != r.want.Name || got.Kind != r.want.Kind || got.Status != r.want.Status ||
				got.SocketPath != r.want.SocketPath || got.PeerProtocol != r.want.PeerProtocol ||
				got.ProcStart != r.want.ProcStart || got.PIDDomain != r.want.PIDDomain ||
				!slices.Equal(got.PeerFeatures, r.want.PeerFeatures) {
				t.Fatalf("got %+v want %+v", got, r.want)
			}
		})
	}
}

func TestParseEntryErrNoEcho(t *testing.T) {
	long := strings.Repeat("A", 1000)
	for _, field := range []string{"kind", "status"} {
		j := `{"pid":42,"kind":"bg","status":"idle","messagingSocketPath":"/s"}`
		j = strings.Replace(j, `"`+field+`":"`, `"`+field+`":"`+long, 1)
		if field == "kind" {
			j = strings.Replace(j, long+`bg`, long, 1)
		} else {
			j = strings.Replace(j, long+`idle`, long, 1)
		}
		_, err := ParseEntry("42.json", []byte(j))
		if err == nil || len(err.Error()) >= 100 {
			t.Errorf("%s: err=%v, want error shorter than 100", field, err)
		}
	}
}
