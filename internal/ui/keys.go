// Package ui holds the terminal UI; keys.go is the pure vim key table (DESIGN R6).
package ui

import (
	"strconv"
	"strings"
)

// EditMode is the vim mode.
type EditMode uint8

const (
	Normal EditMode = iota
	Insert
)

// ActKind names what a key press asks the UI to do.
type ActKind uint8

const (
	None ActKind = iota
	Scroll
	HalfPage
	Top
	Bottom
	NextChan
	PrevChan
	GotoChan
	CmdLine
	ShellLine
	Send
	Complete
	Help
	ToggleTools
	ToggleMentions
	FocusPane
	NextTurn
	PrevTurn
	LineUp
	LineDown
	ToggleStamps
	PickChan
	PickSpeaker
	Yank
	PickInvite
	ReplyMention
	ReplySelected
)

// maxCountDigits bounds the {n} in {n}gt.
const maxCountDigits = 4

// Action is a key result; N is the signed amount for Scroll/HalfPage or, for GotoChan,
// the 1-based vim {n}gt channel number (the model uses N-1).
type Action struct {
	Kind ActKind
	N    int
}

// Key maps key k (bubbletea KeyMsg.String() form) in mode m with the pending
// prefix ("g", "3", "3g") to an action, the next mode and the next pending prefix.
func Key(m EditMode, pending, k string) (Action, EditMode, string) {
	if m == Insert {
		switch k {
		case "esc":
			return Action{}, Normal, ""
		case "enter":
			return Action{Kind: Send}, Insert, ""
		case "ctrl+t":
			return Action{Kind: PickChan}, Insert, ""
		case "up":
			return Action{Kind: LineUp}, Insert, ""
		case "down":
			return Action{Kind: LineDown}, Insert, ""
		case "ctrl+p":
			return Action{Kind: PrevChan}, Insert, ""
		case "ctrl+n":
			return Action{Kind: NextChan}, Insert, ""
		case "tab":
			return Action{Kind: Complete}, Insert, ""
		}
		return Action{}, Insert, ""
	}
	if pending == "" {
		switch k {
		case "i", "a", "o":
			return Action{}, Insert, ""
		case "A":
			return Action{Kind: ReplySelected}, Insert, ""
		case "O":
			return Action{Kind: ReplyMention}, Insert, ""
		case "j":
			return Action{Scroll, 1}, Normal, ""
		case "k":
			return Action{Scroll, -1}, Normal, ""
		case "ctrl+d":
			return Action{HalfPage, 1}, Normal, ""
		case "ctrl+u":
			return Action{HalfPage, -1}, Normal, ""
		case "G":
			return Action{Kind: Bottom}, Normal, ""
		case "ctrl+t":
			return Action{Kind: PickChan}, Normal, ""
		case "S":
			return Action{Kind: PickSpeaker}, Normal, ""
		case "y":
			return Action{Kind: Yank}, Normal, ""
		case "I":
			return Action{Kind: PickInvite}, Normal, ""
		case "up":
			return Action{Kind: LineUp}, Normal, ""
		case "down":
			return Action{Kind: LineDown}, Normal, ""
		case "ctrl+n":
			return Action{Kind: NextChan}, Normal, ""
		case "ctrl+p":
			return Action{Kind: PrevChan}, Normal, ""
		case "J":
			return Action{Kind: NextTurn}, Normal, ""
		case "K":
			return Action{Kind: PrevTurn}, Normal, ""
		case "tab":
			return Action{Kind: FocusPane}, Normal, ""
		case "s":
			return Action{Kind: ToggleStamps}, Normal, ""
		case "t":
			return Action{Kind: ToggleTools}, Normal, ""
		case "m":
			return Action{Kind: ToggleMentions}, Normal, ""
		case "?":
			return Action{Kind: Help}, Normal, ""
		case ":":
			return Action{Kind: CmdLine}, Normal, ""
		case "$":
			return Action{Kind: ShellLine}, Normal, ""
		case "g":
			return Action{}, Normal, "g"
		}
	}
	digits, g := strings.CutSuffix(pending, "g")
	switch {
	case len(k) == 1 && k[0] >= '0' && k[0] <= '9' && !g:
		if len(digits) >= maxCountDigits {
			return Action{}, Normal, ""
		}
		if digits != "" || k != "0" {
			return Action{}, Normal, digits + k
		}
	case k == "g" && !g && digits != "":
		return Action{}, Normal, digits + "g"
	case g && digits == "" && k == "g":
		return Action{Kind: Top}, Normal, ""
	case g && digits == "" && k == "t":
		return Action{Kind: NextChan}, Normal, ""
	case g && digits == "" && k == "T":
		return Action{Kind: PrevChan}, Normal, ""
	case g && digits != "" && k == "t":
		if n, err := strconv.Atoi(digits); err == nil {
			return Action{GotoChan, n}, Normal, ""
		}
	}
	return Action{}, Normal, ""
}
