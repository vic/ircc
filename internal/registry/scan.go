package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"syscall"
)

const maxEntry = 64 << 10

var entryName = regexp.MustCompile(`^\d+\.json$`)

// errNotRegular marks a path that is a symlink, FIFO, dir or other non-file.
var errNotRegular = errors.New("not a regular file")

// errTooLarge marks an entry file over maxEntry bytes.
var errTooLarge = errors.New("entry too large")

// readEntry reads a regular file without following symlinks or blocking on
// FIFOs; a file over maxEntry bytes is errTooLarge.
func readEntry(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, errNotRegular
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errNotRegular
	}
	b, err := io.ReadAll(io.LimitReader(f, maxEntry+1)) // +1 detects oversize
	if err == nil && len(b) > maxEntry {
		return nil, fmt.Errorf("%w: over %d bytes", errTooLarge, maxEntry)
	}
	return b, err
}

// Scan returns the live entries in sessDir sorted by PID. Only <pid>.json
// names are opened, so *.key files are never read. Files that vanish mid-scan
// and non-regular files (symlink, FIFO, dir) are skipped; other read and
// parse errors land in errs. Errors never carry file content.
func Scan(sessDir, procRoot string) ([]Entry, []error) {
	des, err := os.ReadDir(sessDir)
	if err != nil {
		return nil, []error{fmt.Errorf("scan %s: %w", sessDir, err)}
	}
	var out []Entry
	var errs []error
	for _, de := range des {
		name := de.Name()
		if !de.Type().IsRegular() || !entryName.MatchString(name) {
			continue
		}
		b, err := readEntry(filepath.Join(sessDir, name))
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errNotRegular) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("scan %s: %w", name, err))
			continue
		}
		if !json.Valid(b) { // json errors quote input bytes
			errs = append(errs, fmt.Errorf("registry entry %q: invalid json", name))
			continue
		}
		e, err := ParseEntry(name, b)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if Alive(e, procRoot) {
			out = append(out, e)
		}
	}
	out, derrs := sameDomain(out)
	errs = append(errs, derrs...)
	slices.SortFunc(out, func(a, b Entry) int { return a.PID - b.PID })
	return out, errs
}

// sameDomain is a second layer over Alive (a procStart match already proves the
// same pid namespace, D:69): session entries vote with their non-empty pidDomain,
// and only a STRICT majority domain drops the session entries of other domains.
// Without a strict majority nothing is dropped and one error says so. Entries
// with an empty pidDomain and ircc's own entries (IsOwn) never vote and are
// always kept, so this never empties the UI. Errors never carry the domain value.
func sameDomain(live []Entry) ([]Entry, []error) {
	count := map[string]int{}
	voters := 0
	for _, e := range live {
		if e.PIDDomain != "" && !IsOwn(e.SocketPath) {
			count[e.PIDDomain]++
			voters++
		}
	}
	ref := ""
	for d, n := range count {
		if 2*n > voters {
			ref = d
		}
	}
	if ref == "" {
		if len(count) > 1 {
			return live, []error{errors.New("registry: no majority pidDomain among live entries, none dropped")}
		}
		return live, nil
	}
	var out []Entry
	var errs []error
	for _, e := range live {
		if e.PIDDomain == "" || e.PIDDomain == ref || IsOwn(e.SocketPath) {
			out = append(out, e)
			continue
		}
		errs = append(errs, fmt.Errorf("registry entry %d.json: pidDomain differs from the majority, dropped", e.PID))
	}
	return out, errs
}
