// Package registry reads Claude session registry entries (DESIGN D:47-49, D:68-70).
package registry

import (
	"encoding/json"
	"fmt"
	"strconv"
)

type Kind uint8

const (
	Interactive Kind = iota
	Bg
)

type Status uint8

const (
	Idle Status = iota
	Busy
)

type Entry struct {
	PID          int
	SessionID    string
	Cwd, Name    string
	Kind         Kind
	Status       Status
	SocketPath   string
	PeerProtocol int
	PeerFeatures []string
	ProcStart    string
	PIDDomain    string
}

type rawEntry struct {
	PID          int      `json:"pid"`
	SessionID    string   `json:"sessionId"`
	Cwd          string   `json:"cwd"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Status       string   `json:"status"`
	SocketPath   string   `json:"messagingSocketPath"`
	PeerProtocol int      `json:"peerProtocol"`
	PeerFeatures []string `json:"peerFeatures"`
	ProcStart    string   `json:"procStart"`
	PIDDomain    string   `json:"pidDomain"`
	StartedAt    int64    `json:"startedAt,omitempty"` // ms; Claude lists "started N ago" from it
}

// ParseEntry accepts only a file named <pid>.json whose pid matches the body (E1 D:77).
func ParseEntry(fileName string, b []byte) (Entry, error) {
	var r rawEntry
	if err := json.Unmarshal(b, &r); err != nil {
		return Entry{}, fmt.Errorf("registry entry %q: %w", fileName, err)
	}
	if r.PID <= 0 {
		return Entry{}, fmt.Errorf("registry entry %q: pid %d not positive", fileName, r.PID)
	}
	if want := strconv.Itoa(r.PID) + ".json"; fileName != want {
		return Entry{}, fmt.Errorf("registry entry %q: want file name %q", fileName, want)
	}
	if r.SocketPath == "" {
		return Entry{}, fmt.Errorf("registry entry %q: empty messagingSocketPath", fileName)
	}
	e := Entry{PID: r.PID, SessionID: r.SessionID, Cwd: r.Cwd, Name: r.Name, SocketPath: r.SocketPath,
		PeerProtocol: r.PeerProtocol, PeerFeatures: r.PeerFeatures, ProcStart: r.ProcStart, PIDDomain: r.PIDDomain}
	switch r.Kind {
	case "interactive":
		e.Kind = Interactive
	case "bg":
		e.Kind = Bg
	default:
		return Entry{}, fmt.Errorf("registry entry %q: unknown kind", fileName)
	}
	switch r.Status {
	case "idle":
		e.Status = Idle
	case "busy":
		e.Status = Busy
	default:
		return Entry{}, fmt.Errorf("registry entry %q: unknown status", fileName)
	}
	return e, nil
}
