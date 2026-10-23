package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Agent is one subagent of a session, from its agent-<id>.meta.json.
type Agent struct {
	ID, Nick, Type, Description, Model string
	Path                               string // the agent's own transcript (.jsonl)
	Depth                              int
}

type agentMeta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
	SpawnDepth  int    `json:"spawnDepth"`
	Model       string `json:"model"`
}

const (
	agentPrefix = "agent-"
	metaSuffix  = ".meta.json"
)

// Subagents lists the agents under <projectDir>/<sessionID>/subagents, ordered by ID.
// A missing directory yields nil, nil. A corrupt meta file adds an error and
// the rest of the list is still returned.
func Subagents(projectDir, sessionID string) ([]Agent, []error) {
	if sessionID == "" || strings.ContainsAny(sessionID, `/*?[\`) || strings.Contains(sessionID, "..") {
		return nil, []error{fmt.Errorf("%w: %q", ErrBadSessionID, sessionID)}
	}
	dir := filepath.Join(projectDir, sessionID, "subagents")
	des, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []error{fmt.Errorf("transcript: read %s: %w", dir, err)}
	}
	var out []Agent
	var errs []error
	for _, de := range des {
		name := de.Name()
		if !de.Type().IsRegular() || !strings.HasPrefix(name, agentPrefix) || !strings.HasSuffix(name, metaSuffix) {
			continue
		}
		p := filepath.Join(dir, name)
		b, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("transcript: read %s: %w", p, err))
			continue
		}
		var m agentMeta
		if err := json.Unmarshal(b, &m); err != nil {
			errs = append(errs, fmt.Errorf("transcript: parse %s: %w", p, err))
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, agentPrefix), metaSuffix)
		out = append(out, Agent{ID: id, Nick: agentNick(id), Type: m.AgentType, Path: filepath.Join(dir, agentPrefix+id+".jsonl"),
			Description: m.Description, Model: m.Model, Depth: m.SpawnDepth})
	}
	slices.SortFunc(out, func(a, b Agent) int { return strings.Compare(a.ID, b.ID) })
	return out, errs
}

// agentNick extracts <name> from a<name>-<hash>; other ids are their own nick.
func agentNick(id string) string {
	if rest, ok := strings.CutPrefix(id, "a"); ok {
		if i := strings.LastIndexByte(rest, '-'); i > 0 {
			return rest[:i]
		}
	}
	return id
}

// busyTail is how much of an agent transcript Busy reads.
const busyTail = 64 << 10

// Busy reports whether the agent with transcript path is working: its last user or
// assistant event is not an assistant turn that ended (end_turn). Measured
// 2026-10-22: an idle teammate ends on assistant stop_reason end_turn, then
// attachments. known is false when no such event is in the last busyTail bytes.
func Busy(path string) (busy, known bool) {
	f, err := os.Open(path)
	if err != nil {
		return false, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return false, false
	}
	off := max(fi.Size()-busyTail, 0)
	buf := make([]byte, fi.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
		return false, false
	}
	lines := bytes.Split(buf, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if i == 0 && off > 0 {
			break // cut line
		}
		ev, err := ParseEvent(lines[i])
		if err != nil || (ev.Type != "user" && ev.Type != "assistant") {
			continue
		}
		return !ev.Done, true
	}
	return false, false
}
