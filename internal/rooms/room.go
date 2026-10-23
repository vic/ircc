// Package rooms holds ircc's own room state (DESIGN D:232-262, D:365-366).
package rooms

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"ircc/internal/peer"
)

// maxEncName bounds the percent-encoded name (endpoint path must fit sun_path, 107).
const maxEncName = 64

// Name is a validated room name; only ParseName builds one.
type Name struct{ s string }

func (n Name) String() string { return n.s }

// ParseName wants `&` plus >=1 char, valid UTF-8, none of / \ " < > , space or control,
// and EncName(s) <= maxEncName so the endpoint path fits in sun_path.
func ParseName(s string) (Name, error) {
	if !strings.HasPrefix(s, "&") || len(s) == 1 || !utf8.ValidString(s) {
		return Name{}, fmt.Errorf("rooms: name %q: want &<name> in valid UTF-8", s)
	}
	for _, r := range s {
		if strings.ContainsRune(`/\"<>,`, r) || unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return Name{}, fmt.Errorf("rooms: name %q: forbidden char %q", s, r)
		}
	}
	if len(peer.EncName(s)) > maxEncName {
		return Name{}, fmt.Errorf("rooms: name %q: encoded longer than %d bytes", s, maxEncName)
	}
	return Name{s}, nil
}

type Member struct {
	Session string `json:"session"`
	Nick    string `json:"nick"`
}

type Room struct {
	Name    Name
	Topic   string
	Members []Member
}

type diskRoom struct {
	Name    string   `json:"name"`
	Topic   string   `json:"topic"`
	Members []Member `json:"members"`
}

// Save writes <dir>/<EncName>.json atomically (temp + rename, mode 0600). dir must exist.
func Save(dir string, r Room) error {
	if r.Name.s == "" {
		return errors.New("rooms: save: zero Name")
	}
	if err := checkNicks(r.Members); err != nil {
		return fmt.Errorf("rooms: save %s: %w", r.Name.s, err)
	}
	b, err := json.Marshal(diskRoom{r.Name.s, r.Topic, r.Members})
	if err != nil {
		return fmt.Errorf("rooms: save %s: %w", r.Name.s, err)
	}
	tmp, err := os.CreateTemp(dir, ".room-*.tmp") // CreateTemp is 0600
	if err != nil {
		return fmt.Errorf("rooms: save %s: %w", r.Name.s, err)
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("rooms: save %s: %w", r.Name.s, err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, peer.EncName(r.Name.s)+".json")); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("rooms: save %s: %w", r.Name.s, err)
	}
	return nil
}

// Load reads every *.json in dir; bad files go to errs. A missing dir is empty.
func Load(dir string) ([]Room, []error) {
	des, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []error{fmt.Errorf("rooms: load %s: %w", dir, err)}
	}
	var out []Room
	var errs []error
	for _, de := range des {
		if !de.Type().IsRegular() || filepath.Ext(de.Name()) != ".json" {
			continue
		}
		b, err := readCapped(filepath.Join(dir, de.Name()))
		if err != nil {
			errs = append(errs, fmt.Errorf("rooms: load %s: %w", de.Name(), err))
			continue
		}
		var d diskRoom
		if err := json.Unmarshal(b, &d); err != nil {
			errs = append(errs, fmt.Errorf("rooms: load %s: %w", de.Name(), err))
			continue
		}
		n, err := ParseName(d.Name)
		if err != nil {
			errs = append(errs, fmt.Errorf("rooms: load %s: %w", de.Name(), err))
			continue
		}
		if err := checkNicks(d.Members); err != nil {
			errs = append(errs, fmt.Errorf("rooms: load %s: %w", de.Name(), err))
			continue
		}
		if want := peer.EncName(n.s) + ".json"; de.Name() != want {
			errs = append(errs, fmt.Errorf("rooms: load %s: file name must be %s", de.Name(), want))
			continue
		}
		out = append(out, Room{n, d.Topic, d.Members})
	}
	return out, errs
}

const maxRoomFile = 64 << 10

func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxRoomFile+1)) // +1 detects oversize
	if err != nil {
		return nil, err
	}
	if len(b) > maxRoomFile {
		return nil, fmt.Errorf("larger than %d bytes", maxRoomFile)
	}
	return b, nil
}

func checkNicks(ms []Member) error {
	for _, m := range ms {
		if err := peer.ParseNick(m.Nick); err != nil {
			return err
		}
	}
	return nil
}
