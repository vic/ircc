package rooms

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"ircc/internal/peer"
)

// maxLogRead bounds how much of a room log LoadLog reads, from the end.
const maxLogRead = 1 << 20

// maxLog is the size above which AppendLine rewrites the log keeping its last half.
const maxLog = 4 << 20

// LogLine is one room scrollback line. Text is stored as given; the UI
// sanitizes at display (model.clean).
type LogLine struct {
	Time time.Time `json:"time"`
	Nick string    `json:"nick"`
	Text string    `json:"text"`
}

func logPath(dir string, n Name) (string, error) {
	if n.s == "" {
		return "", errors.New("rooms: log: zero Name")
	}
	return filepath.Join(dir, peer.EncName(n.s)+".log"), nil
}

// compactLog rewrites the log at p to its last half when it exceeds maxLog
// (temp + rename, 0600, whole lines only). Single writer assumed: ircc owns the file.
func compactLog(dir, p string) error {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() <= maxLog {
		return err
	}
	if _, err := f.Seek(-maxLog/2, io.SeekEnd); err != nil {
		return err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 { // drop the cut first line
		data = data[i+1:]
	}
	tmp, err := os.CreateTemp(dir, ".log-*.tmp") // 0600
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if err := errors.Join(werr, tmp.Close()); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// RemoveLog deletes the room's log (for /close, D:195); a missing log is not an error.
func RemoveLog(dir string, n Name) error {
	p, err := logPath(dir, n)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("rooms: log %s: %w", n.s, err)
	}
	return nil
}

// AppendLine appends l as one JSON line to <dir>/<EncName>.log (0600). Text is
// stored verbatim; it is sanitized at display by model.clean. A symlink at that
// path is refused (O_NOFOLLOW). Above maxLog the log is first cut to its last half.
// dir must exist.
func AppendLine(dir string, n Name, l LogLine) error {
	p, err := logPath(dir, n)
	if err != nil {
		return err
	}
	if l.Nick == "" {
		return errors.New("rooms: log: empty nick")
	}
	if err := peer.ParseNick(l.Nick); err != nil {
		return fmt.Errorf("rooms: log %s: %w", n.s, err)
	}
	b, err := json.Marshal(l)
	if err != nil {
		return fmt.Errorf("rooms: log %s: %w", n.s, err)
	}
	if err := compactLog(dir, p); err != nil {
		return fmt.Errorf("rooms: log %s: compact: %w", n.s, err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("rooms: log %s: %w", n.s, err)
	}
	_, werr := f.Write(append(b, '\n')) // one write: O_APPEND keeps the line whole
	if err := errors.Join(werr, f.Close()); err != nil {
		return fmt.Errorf("rooms: log %s: %w", n.s, err)
	}
	return nil
}

// LoadLog returns the last limit lines, oldest first, reading at most maxLogRead
// bytes from the end (a cut first line is dropped). Malformed lines are skipped
// and counted in the returned error; the valid lines are still returned.
// A missing log is empty. A symlink is refused.
func LoadLog(dir string, n Name, limit int) ([]LogLine, error) {
	if limit < 1 {
		return nil, fmt.Errorf("rooms: log: limit %d must be >= 1", limit)
	}
	p, err := logPath(dir, n)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rooms: log %s: %w", n.s, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("rooms: log %s: %w", n.s, err)
	}
	cut := fi.Size() > maxLogRead
	if cut {
		if _, err := f.Seek(-maxLogRead, io.SeekEnd); err != nil {
			return nil, fmt.Errorf("rooms: log %s: %w", n.s, err)
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, maxLogRead))
	if err != nil {
		return nil, fmt.Errorf("rooms: log %s: %w", n.s, err)
	}
	if cut { // the first line is probably partial
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		} else {
			data = nil
		}
	}
	var out []LogLine
	bad := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, maxLogRead)
	for sc.Scan() {
		var l LogLine
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.Nick == "" {
			bad++
			continue
		}
		out = append(out, l)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("rooms: log %s: %w", n.s, err)
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	if bad > 0 {
		return out, fmt.Errorf("rooms: log %s: %d malformed lines skipped", n.s, bad)
	}
	return out, nil
}
