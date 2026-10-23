package transcript

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var (
	// ErrBadSessionID means the id is empty or holds a path or glob metacharacter.
	ErrBadSessionID = errors.New("transcript: invalid session id")
	// ErrNotFound means no transcript matches the session id.
	ErrNotFound = errors.New("transcript: session not found")
	// ErrAmbiguous means several projects hold a transcript for the session id.
	ErrAmbiguous = errors.New("transcript: session id matches several projects")
)

// Find locates <claudeDir>/projects/*/<sessionID>.jsonl; the cwd slug is never derived.
func Find(claudeDir, sessionID string) (string, error) {
	if sessionID == "" || strings.ContainsAny(sessionID, `/*?[\`) || strings.Contains(sessionID, "..") {
		return "", fmt.Errorf("%w: %q", ErrBadSessionID, sessionID)
	}
	root := filepath.Join(claudeDir, "projects")
	entries, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("transcript: read %s: %w", root, err)
	}
	var m []string
	for _, e := range entries {
		p := filepath.Join(root, e.Name(), sessionID+".jsonl")
		if _, err := os.Stat(p); err == nil {
			m = append(m, p)
		} else if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return "", fmt.Errorf("transcript: stat %s: %w", p, err)
		}
	}
	switch len(m) {
	case 0:
		return "", fmt.Errorf("%w: %s", ErrNotFound, sessionID)
	case 1:
		return m[0], nil
	}
	return "", fmt.Errorf("%w: %s (%d)", ErrAmbiguous, sessionID, len(m))
}
