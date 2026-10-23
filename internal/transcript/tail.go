// Package transcript tails session transcript JSONL files.
package transcript

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrTruncated means the file is shorter than the bytes already consumed or was
// replaced by another file; the caller must reopen the file at offset 0.
// Truncate-and-regrow of the same inode past the offset is not detectable.
var ErrTruncated = errors.New("transcript: file shrank below read offset")

// Tailer reads complete lines appended to a file; off counts bytes consumed,
// part holds the trailing line without its newline yet.
type Tailer struct {
	path string
	info os.FileInfo // identity of the file at Open; a different file means replaced
	off  int64
	part []byte
}

// Open starts tailing path at byte offset off.
func Open(path string, off int64) (*Tailer, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("transcript: open %s: %w", path, err)
	}
	return &Tailer{path: path, info: info, off: off}, nil
}

// Lines returns the complete lines appended since the last call, without newlines.
// It reads from the offset to EOF in one read; bounding that is left for later.
func (t *Tailer) Lines() ([][]byte, error) {
	f, err := os.Open(t.path)
	if err != nil {
		return nil, fmt.Errorf("transcript: tail %s: %w", t.path, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("transcript: stat %s: %w", t.path, err)
	}
	if st.Size() < t.off || !os.SameFile(t.info, st) {
		return nil, ErrTruncated
	}
	if _, err := f.Seek(t.off, io.SeekStart); err != nil {
		return nil, fmt.Errorf("transcript: seek %s: %w", t.path, err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("transcript: read %s: %w", t.path, err)
	}
	t.off += int64(len(data))
	buf := append(t.part, data...)
	var out [][]byte
	for {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			break
		}
		out = append(out, buf[:i:i])
		buf = buf[i+1:]
	}
	t.part = append([]byte(nil), buf...)
	return out, nil
}

// Offset is the byte position after the last complete line, safe to resume from.
func (t *Tailer) Offset() int64 { return t.off - int64(len(t.part)) }
