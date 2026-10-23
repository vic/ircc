package model

import (
	"fmt"
	"slices"
	"strings"
)

// CmdName is the closed set of command names (DESIGN D:318-319).
type CmdName string

const (
	CmdJoin     CmdName = "join"
	CmdPart     CmdName = "part"
	CmdClose    CmdName = "close"
	CmdInvite   CmdName = "invite"
	CmdKick     CmdName = "kick"
	CmdTopic    CmdName = "topic"
	CmdWho      CmdName = "who"
	CmdNames    CmdName = "names"
	CmdMsg      CmdName = "msg"
	CmdMode     CmdName = "mode"
	CmdThinking CmdName = "thinking"
	CmdQuit     CmdName = "quit"
)

// Cmd can only be built by ParseCmd.
type Cmd struct {
	name CmdName
	args []string
}

func (c Cmd) Name() CmdName  { return c.name }
func (c Cmd) Args() []string { return c.args }

// arity gives the arg count per name; raw means the last arg is the rest of
// the line verbatim. ok is false for an unknown name; n < 0 is free-form.
func arity(n CmdName) (count int, raw, ok bool) {
	switch n {
	case CmdJoin, CmdKick, CmdMode, CmdThinking:
		return 1, false, true
	case CmdInvite:
		return 2, false, true
	case CmdTopic:
		return 1, true, true
	case CmdMsg:
		return 2, true, true
	case CmdPart, CmdClose, CmdWho, CmdNames, CmdQuit:
		return -1, false, true
	}
	return 0, false, false
}

// ParseCmd parses a line starting with `/` or `:`. `:q` is `quit`. A raw
// last arg (/msg body, /topic text) keeps its inner whitespace.
func ParseCmd(line string) (Cmd, error) {
	if !strings.HasPrefix(line, "/") && !strings.HasPrefix(line, ":") {
		return Cmd{}, fmt.Errorf("not a command: %q", line)
	}
	word, rest := line[1:], ""
	if i := strings.IndexAny(word, " \t"); i >= 0 {
		word, rest = word[:i], word[i+1:]
	}
	switch word { // aliases
	case "q":
		word = string(CmdQuit)
	case "rm":
		word = string(CmdClose) // remove a & room: endpoint, saved file and log
	}
	name := CmdName(word)
	n, raw, ok := arity(name)
	if !ok {
		return Cmd{}, fmt.Errorf("unknown command %q", word)
	}
	var args []string
	if raw {
		for len(args) < n-1 {
			var a string
			rest = strings.TrimLeft(rest, " \t")
			i := strings.IndexAny(rest, " \t")
			if i < 0 {
				i = len(rest)
			}
			a, rest = rest[:i], rest[i:]
			args = append(args, a)
		}
		if rest = strings.TrimLeft(rest, " \t"); strings.TrimSpace(rest) != "" {
			args = append(args, rest)
		}
	} else {
		args = strings.Fields(rest)
	}
	if n >= 0 && (len(args) != n || slices.Contains(args, "")) {
		return Cmd{}, fmt.Errorf("/%s wants %d args, got %d", word, n, len(args))
	}
	if len(args) == 1 {
		switch {
		case name == CmdMode && args[0] != "bypass" && args[0] != "prompting":
			return Cmd{}, fmt.Errorf("/mode wants bypass|prompting, got %q", args[0])
		case name == CmdThinking && args[0] != "on" && args[0] != "off":
			return Cmd{}, fmt.Errorf("/thinking wants on|off, got %q", args[0])
		}
	}
	return Cmd{name: name, args: args}, nil
}
