package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ownPrefix marks socket basenames ircc created (D:183).
const ownPrefix = "ircc-"

var ownTmp = regexp.MustCompile(`^\.\d+\.json\.[^/]*\.tmp$`)

func entryFile(pid int) string { return strconv.Itoa(pid) + ".json" }

func kindName(k Kind) string {
	if k == Bg {
		return "bg"
	}
	return "interactive"
}

func statusName(s Status) string {
	if s == Busy {
		return "busy"
	}
	return "idle"
}

// WriteOwn writes e as <pid>.json (tmp + rename, 0600). The bytes must parse
// back through ParseEntry, so an invalid entry is never written.
func WriteOwn(sessDir string, e Entry) error {
	name := entryFile(e.PID)
	b, err := json.Marshal(rawEntry{PID: e.PID, SessionID: e.SessionID, Cwd: e.Cwd, Name: e.Name,
		Kind: kindName(e.Kind), Status: statusName(e.Status), SocketPath: e.SocketPath,
		PeerProtocol: e.PeerProtocol, PeerFeatures: e.PeerFeatures, ProcStart: e.ProcStart, PIDDomain: e.PIDDomain,
		StartedAt: time.Now().UnixMilli()})
	if err != nil {
		return fmt.Errorf("marshal %s: %w", name, err)
	}
	if e.ProcStart == "" {
		return fmt.Errorf("registry entry %q: empty procStart", name)
	}
	if !strings.HasPrefix(filepath.Base(e.SocketPath), ownPrefix) {
		return fmt.Errorf("registry entry %q: socket %q is not an ircc- socket", name, e.SocketPath)
	}
	if _, err := ParseEntry(name, b); err != nil {
		return err
	}
	f, err := os.CreateTemp(sessDir, "."+name+".*.tmp") // O_EXCL, 0600; Scan ignores dotfiles
	if err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := os.Rename(tmp, filepath.Join(sessDir, name)); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// RemoveOwn deletes <pid>.json; already gone is not an error.
func RemoveOwn(sessDir string, pid int) error {
	if err := os.Remove(filepath.Join(sessDir, entryFile(pid))); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", entryFile(pid), err)
	}
	return nil
}

// RemoveStale also deletes WriteOwn tmp leftovers (.<pid>.json.*.tmp). It deletes regular <pid>.json files whose socket basename starts
// with "ircc-" and whose pid is not Alive. Foreign, live, unparsable and
// non-regular files are never touched.
func RemoveStale(sessDir, procRoot string) error {
	des, err := os.ReadDir(sessDir)
	if err != nil {
		return fmt.Errorf("remove stale %s: %w", sessDir, err)
	}
	var errs []error
	for _, de := range des {
		name := de.Name()
		if de.Type().IsRegular() && ownTmp.MatchString(name) { // WriteOwn leftover
			if err := os.Remove(filepath.Join(sessDir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("remove stale %s: %w", name, err))
			}
			continue
		}
		if !de.Type().IsRegular() || !entryName.MatchString(name) {
			continue
		}
		path := filepath.Join(sessDir, name)
		b, err := readEntry(path)
		if err != nil {
			continue
		}
		e, err := ParseEntry(name, b)
		if err != nil || !strings.HasPrefix(filepath.Base(e.SocketPath), ownPrefix) || Alive(e, procRoot) {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove stale %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// RemoveOwnIf deletes <e.PID>.json only if it still names e's socket and procStart,
// so an entry a foreign process wrote for a reused pid survives. Unreadable or
// unparsable files are left alone.
func RemoveOwnIf(sessDir string, e Entry) error {
	path := filepath.Join(sessDir, entryFile(e.PID))
	b, err := readEntry(path)
	if err != nil {
		return nil
	}
	cur, err := ParseEntry(entryFile(e.PID), b)
	if err != nil || cur.SocketPath != e.SocketPath || cur.ProcStart != e.ProcStart {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", entryFile(e.PID), err)
	}
	return nil
}

// HasOwn reports whether <pid>.json exists and names an ircc- socket.
func HasOwn(sessDir string, pid int) bool {
	b, err := readEntry(filepath.Join(sessDir, entryFile(pid)))
	if err != nil {
		return false
	}
	e, err := ParseEntry(entryFile(pid), b)
	return err == nil && strings.HasPrefix(filepath.Base(e.SocketPath), ownPrefix)
}

// IsOwn reports whether sock (a socket path or basename) is one ircc created (D:183).
func IsOwn(sock string) bool { return strings.HasPrefix(filepath.Base(sock), ownPrefix) }
