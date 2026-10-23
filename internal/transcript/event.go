package transcript

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Event is the subset of a transcript line ircc reads (DESIGN D:102-106, D:120-121).
type Event struct {
	Type           string
	PermissionMode string // top-level only
	Sidechain      bool
	AgentID        string
	Held           bool
	HeldFrom       string
	HeldPID        int     // "[verified pid N]" (m14); Held implies HeldPID > 0
	HeldPreview    string  // text inside «...» (D:105); match the fanned line by prefix
	Blocks         []Block // assistant/user message content (measured shapes)
	Done           bool    // assistant turn ended (stop_reason end_turn): the agent is idle
	Notice         *Notice // hook errors and goal status: shown as a toast, not a line
}

type rawEvent struct {
	Type           string `json:"type"`
	Subtype        string `json:"subtype"`
	Level          string `json:"level"`
	Content        string `json:"content"`
	PermissionMode string `json:"permissionMode"`
	IsSidechain    bool   `json:"isSidechain"`
	AgentID        string `json:"agentId"`
	Message        *struct {
		Content    json.RawMessage `json:"content"`
		StopReason string          `json:"stop_reason"`
	} `json:"message"`
	Attachment *struct {
		Type          string `json:"type"`
		Met           bool   `json:"met"`
		Condition     string `json:"condition"`
		HookEvent     string `json:"hookEvent"`
		BlockingError struct {
			BlockingError string `json:"blockingError"`
		} `json:"blockingError"`
	} `json:"attachment"`
}

const (
	heldPrefix   = "Held peer message"
	fromMarker   = "— from "
	pidMarker    = "[verified pid "
	previewOpen  = "preview: «"
	previewClose = "»"
)

// ParseEvent decodes one transcript line; any error yields the zero Event.
func ParseEvent(line []byte) (Event, error) {
	var r rawEvent
	if err := json.Unmarshal(line, &r); err != nil {
		return Event{}, fmt.Errorf("transcript event: %w", err)
	}
	e := Event{Type: r.Type, PermissionMode: r.PermissionMode, Sidechain: r.IsSidechain, AgentID: r.AgentID}
	if r.Type == "assistant" && r.Message != nil {
		e.Done = r.Message.StopReason == "end_turn"
	}
	if r.Message != nil && len(r.Message.Content) > 0 && (r.Type == "assistant" || r.Type == "user") {
		bs, err := parseBlocks(r.Type, r.Message.Content)
		if err != nil {
			return Event{}, err
		}
		e.Blocks = bs
	}
	if n, only := r.notice(e.Blocks); n != nil || only {
		e.Notice = n
		if only {
			e.Blocks = nil
		}
	}
	if r.Type == "system" && r.Subtype == "informational" && r.Level == "warning" && strings.HasPrefix(r.Content, heldPrefix) {
		// Fail loud: a held line that cannot be attributed (from, pid, preview) is an error,
		// never silently "not held".
		_, rest, ok := strings.Cut(r.Content, fromMarker)
		from, _, _ := strings.Cut(rest, " ")
		if !ok || from == "" {
			return Event{}, errors.New("transcript event: held line without from")
		}
		_, rest, ok = strings.Cut(r.Content, pidMarker)
		num, _, closed := strings.Cut(rest, "]")
		pid, err := strconv.Atoi(num)
		if !ok || !closed || err != nil || pid <= 0 {
			return Event{}, errors.New("transcript event: held line without verified pid")
		}
		_, rest, ok = strings.Cut(r.Content, previewOpen)
		preview, _, closed := strings.Cut(rest, previewClose)
		if !ok || !closed {
			return Event{}, errors.New("transcript event: held line without preview")
		}
		e.Held, e.HeldFrom, e.HeldPID, e.HeldPreview = true, from, pid, preview
	}
	return e, nil
}

// LastMode returns the last non-empty top-level permissionMode, or "".
func LastMode(evs []Event) string {
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].PermissionMode != "" {
			return evs[i].PermissionMode
		}
	}
	return ""
}

// BlockKind is the closed set of message block kinds ircc reads.
type BlockKind uint8

const (
	TextBlock BlockKind = iota
	ToolUseBlock
	ThinkingBlock
	ToolResultBlock
)

// Block is one message content block; fields not of its Kind are zero.
type Block struct {
	Kind    BlockKind
	Text    string // text / thinking
	Tool    string // tool_use name
	ToolTo  string // tool_use input.to when a string, else ""
	ToolArg string // first line of the first string input in toolArgKeys, else ""
	IsError bool   // tool_result
}

// parseBlocks reads message.content: an array of blocks, or (user only) a string.
// Unknown block types are skipped; any other shape is an error.
func parseBlocks(evType string, raw json.RawMessage) ([]Block, error) {
	var s string
	if evType == "user" && json.Unmarshal(raw, &s) == nil {
		return []Block{{Kind: TextBlock, Text: s}}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("transcript event: %s content: %w", evType, err)
	}
	var out []Block
	for _, it := range items {
		var h struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(it, &h); err != nil {
			return nil, fmt.Errorf("transcript event: block: %w", err)
		}
		var b Block
		var err error
		switch h.Type {
		case "text":
			var v struct{ Text string }
			err = json.Unmarshal(it, &v)
			b = Block{Kind: TextBlock, Text: v.Text}
		case "thinking":
			var v struct{ Thinking string }
			err = json.Unmarshal(it, &v)
			b = Block{Kind: ThinkingBlock, Text: v.Thinking}
		case "tool_use":
			var v struct {
				Name  string
				Input map[string]json.RawMessage `json:"input"`
			}
			err = json.Unmarshal(it, &v)
			b = Block{Kind: ToolUseBlock, Tool: v.Name}
			_ = json.Unmarshal(v.Input["to"], &b.ToolTo) // non-string to stays ""
			b.ToolArg = toolArg(v.Input)
		case "tool_result":
			var v struct {
				IsError bool `json:"is_error"`
			}
			err = json.Unmarshal(it, &v)
			b = Block{Kind: ToolResultBlock, IsError: v.IsError}
		default:
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("transcript event: %s block: %w", h.Type, err)
		}
		out = append(out, b)
	}
	return out, nil
}

// toolArgKeys are the tool inputs that say what a call does, most telling first.
var toolArgKeys = []string{"command", "file_path", "notebook_path", "path", "pattern", "url", "query", "skill", "to", "description", "prompt"}

func toolArg(in map[string]json.RawMessage) string {
	for _, k := range toolArgKeys {
		var s string
		if json.Unmarshal(in[k], &s) == nil && strings.TrimSpace(s) != "" {
			first, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
			return first
		}
	}
	return ""
}

// Notice is an event ircc shows as a toast (vic 2026-10-23). Measured 2026-10-23:
// attachments goal_status {met, condition} and hook_blocking_error {hookEvent,
// blockingError.blockingError}; user text "<Event>[:<Tool>] hook error: ...".
type Notice struct {
	Text string
	Err  bool
}

// hookErrRE is user text Claude records for a failed hook, e.g. "PreToolUse:Bash hook error: ...".
var hookErrRE = regexp.MustCompile(`^(PreToolUse|PostToolUse|Stop|SubagentStop|SessionStart|SessionEnd|UserPromptSubmit|Notification|PreCompact)(:[A-Za-z_]+)? hook error: `)

// stopFeedback opens the user text that repeats a hook_blocking_error attachment.
const stopFeedback = "Stop hook feedback:"

// notice reads the toast an event carries, if any, and reports whether the event's
// own blocks are only that notice (then they are not shown as lines).
func (r rawEvent) notice(blocks []Block) (*Notice, bool) {
	if a := r.Attachment; r.Type == "attachment" && a != nil {
		switch a.Type {
		case "goal_status":
			if a.Met {
				return &Notice{Text: "goal met: " + a.Condition}, false
			}
			return &Notice{Text: "goal not met yet: " + a.Condition}, false
		case "hook_blocking_error":
			return &Notice{Text: a.HookEvent + " hook: " + a.BlockingError.BlockingError, Err: true}, false
		}
	}
	if r.Type != "user" || len(blocks) != 1 || blocks[0].Kind != TextBlock {
		return nil, false
	}
	switch t := blocks[0].Text; {
	case hookErrRE.MatchString(t):
		return &Notice{Text: t, Err: true}, true
	case strings.HasPrefix(t, stopFeedback):
		return nil, true // the hook_blocking_error attachment already toasts it
	}
	return nil, false
}
