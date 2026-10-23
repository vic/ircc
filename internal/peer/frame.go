// Package peer holds the peer-socket protocol (frame.go) and sockets (conn.go).
package peer

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	// m17: receiver silently drops a `from` outside this class.
	fromRE = regexp.MustCompile(`^[A-Za-z0-9%:_/.-]+$`)
	// m27: chars outside this class are %XX-encoded per UTF-8 byte.
	plainRE = regexp.MustCompile(`[A-Za-z0-9:_/.\-]`)
)

const udsPrefix = "uds:"

// EncName percent-encodes s per m27.
func EncName(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if plainRE.MatchString(s[i : i+1]) {
			b.WriteByte(s[i])
		} else {
			fmt.Fprintf(&b, "%%%02X", s[i])
		}
	}
	return b.String()
}

// Addr is a validated socket path; String is its `uds:` wire form. Only
// ParseAddr and PathAddr build one.
type Addr struct{ path string }

// ParseAddr validates a `uds:` wire address against m17 and decodes its %XX
// once, as Claude does: `uds:.../ircc-%2526r.sock` is the file `ircc-%26r.sock`.
func ParseAddr(s string) (Addr, error) {
	if !strings.HasPrefix(s, udsPrefix) || len(s) == len(udsPrefix) {
		return Addr{}, fmt.Errorf("peer: address %q: want uds:<path>", s)
	}
	if !fromRE.MatchString(s) {
		return Addr{}, fmt.Errorf("peer: address %q: chars outside [A-Za-z0-9%%:_/.-]", s)
	}
	p, err := url.PathUnescape(strings.TrimPrefix(s, udsPrefix))
	if err != nil {
		return Addr{}, fmt.Errorf("peer: address %q: %w", s, err)
	}
	return PathAddr(p)
}

// PathAddr is the address of socket file p. Strictness (absolute, clean, .sock)
// follows m26 (DESIGN.md:146-148): replies go only to a real socket path.
func PathAddr(p string) (Addr, error) {
	if !utf8.ValidString(p) || !filepath.IsAbs(p) || filepath.Clean(p) != p || !strings.HasSuffix(p, ".sock") {
		return Addr{}, fmt.Errorf("peer: socket path %q: want clean absolute path ending .sock", p)
	}
	return Addr{p}, nil
}

// Path is the socket file.
func (a Addr) Path() string { return a.path }

// String is the wire form: the path encoded per m27, so "%" goes out as "%25".
func (a Addr) String() string { return udsPrefix + EncName(a.path) }

// maxSunPath is the longest bindable AF_UNIX path (sun_path is 108 with NUL).
const maxSunPath = 107

// EndpointPath is <socksDir>/ircc-<enc>.sock (D:181-183).
func EndpointPath(socksDir, name string) (string, error) {
	if socksDir == "" || name == "" {
		return "", errors.New("peer: empty socks dir or name")
	}
	if strings.Contains(name, "/") {
		return "", fmt.Errorf("peer: name %q contains '/'", name)
	}
	// Names with `\` never exist: the m27 JS class keeps `\` plain, so it would not round-trip (adversary F3).
	if !utf8.ValidString(name) || strings.Contains(name, `\`) {
		return "", fmt.Errorf("peer: name %q is not valid UTF-8 or contains '\\'", name)
	}
	p := filepath.Join(socksDir, "ircc-"+EncName(name)+".sock")
	if len(p) > maxSunPath {
		return "", fmt.Errorf("peer: socket path %d bytes exceeds sun_path limit %d", len(p), maxSunPath)
	}
	return p, nil
}

// Class is the sender permission class shown in from-mode (m24).
type Class uint8

const (
	Prompting Class = iota
	Bypass
)

func (c Class) String() string {
	if c == Bypass {
		return "bypass"
	}
	return "prompting"
}

// ClassOf maps a permissionMode to its class. plan -> Prompting: lead ruling
// GAP-2, m24; fails safe (the frame is held at a bypass receiver).
func ClassOf(mode string) (Class, error) {
	switch mode {
	case "bypassPermissions":
		return Bypass, nil
	case "plan", "auto", "acceptEdits", "default":
		return Prompting, nil
	}
	return 0, fmt.Errorf("peer: unknown permissionMode %q", mode)
}

const (
	closeTag = "</cross-session-message>"
	// closePrefix is matched case-insensitively: the receiver regex may accept variants.
	closePrefix = "</cross-session-message"
)

// ParseNick rejects names that could break out of the wrapper attribute (D:362-364).
func ParseNick(n string) error {
	if !utf8.ValidString(n) || strings.ContainsAny(n, "<>\"\\") || strings.IndexFunc(n, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return fmt.Errorf("peer: name %q has a forbidden character", n)
	}
	return nil
}

// Frame is one validated, newline-terminated JSON line ready to write.
type Frame struct{ b []byte }

// Bytes returns the frame line including the trailing newline.
func (f Frame) Bytes() []byte { return f.b }

func uuid4() (string, error) {
	var u [16]byte
	if _, err := rand.Read(u[:]); err != nil {
		return "", fmt.Errorf("peer: uuid: %w", err)
	}
	u[6] = u[6]&0x0f | 0x40
	u[8] = u[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:]), nil
}

// NewUserFrame builds a first-hop user frame (D:93-97); rejects what the
// receiver's wrapper regex cannot parse (D:362-364).
func NewUserFrame(from Addr, fromName string, c Class, body string) (Frame, error) {
	// zero Addr (no path) is not an address.
	if from.path == "" {

		return Frame{}, errors.New("peer: invalid from address")
	}
	if err := ParseNick(fromName); err != nil {
		return Frame{}, err
	}
	if strings.Contains(strings.ToLower(fromName), closePrefix) {
		return Frame{}, errors.New("peer: name contains " + closeTag)
	}
	if strings.Contains(strings.ToLower(body), closePrefix) {
		return Frame{}, errors.New("peer: body contains " + closeTag)
	}
	return frame(from, `<cross-session-message from="`+from.String()+`" from-name="`+fromName+
		`" from-mode="`+c.String()+"\">\n"+body+"\n"+closeTag)
}

// NewLiteralFrame is a frame whose content is body as is, with no wrapper: the
// session sees body as the whole message (measured 2026-10-22, LITERAL-PROBE-1).
// With no from-mode, a bypass receiver holds it and a prompting one accepts (m25).
func NewLiteralFrame(from Addr, body string) (Frame, error) {
	if from.path == "" {
		return Frame{}, errors.New("peer: invalid from address")
	}
	if strings.TrimSpace(body) == "" {
		return Frame{}, errors.New("peer: empty literal")
	}
	return frame(from, body)
}

// frame marshals one user frame line carrying content.
func frame(from Addr, content string) (Frame, error) {
	id, err := uuid4()
	if err != nil {
		return Frame{}, err
	}
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	b, err := json.Marshal(struct {
		MsgV     int    `json:"msgV"`
		MsgID    string `json:"msg_id"`
		Type     string `json:"type"`
		Message  msg    `json:"message"`
		Priority string `json:"priority"`
		From     string `json:"from"`
	}{1, id, "user", msg{"user", content}, "next", from.String()})
	if err != nil {
		return Frame{}, fmt.Errorf("peer: marshal: %w", err)
	}
	return Frame{append(b, '\n')}, nil
}

// Inbound is a received user frame after the guards ran.
type Inbound struct {
	From                      Addr
	Mode                      Class
	FromName, Body, AgentNick string
}

var (
	msgIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	wrapRE  = regexp.MustCompile(`(?s)^<cross-session-message from="([^"]*)"(?: hop-chain="([^"]*)")? from-name="([^"]*)" from-mode="([^"]*)">\n(.*)\n</cross-session-message>$`)
	// hop-chain: m17 from class plus "," between hops; validated, never echoed.
	hopRE     = regexp.MustCompile(`^[A-Za-z0-9%:_/.,-]*$`)
	agentNick = regexp.MustCompile(`<agent-message from="([^"]+)"`)
)

// ParseFrame validates and unwraps one received user frame (D:93-97).
// It never acts on content; all guards of D:362-364 run after the read.
func ParseFrame(line []byte) (Inbound, error) {
	line = bytes.TrimSuffix(line, []byte("\n"))
	if bytes.IndexByte(line, '\n') >= 0 {
		return Inbound{}, errors.New("peer: frame is not one line")
	}
	var env struct {
		MsgV    int    `json:"msgV"`
		MsgID   string `json:"msg_id"`
		Type    string `json:"type"`
		From    string `json:"from"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &env); err != nil {
		return Inbound{}, fmt.Errorf("peer: frame json: %w", err)
	}
	if env.Type != "user" {
		return Inbound{}, errors.New("peer: frame type is not user")
	}
	if env.MsgV != 1 {
		return Inbound{}, errors.New("peer: unsupported msgV")
	}
	if !msgIDRE.MatchString(env.MsgID) {
		return Inbound{}, errors.New("peer: bad msg_id")
	}
	from, err := ParseAddr(env.From)
	if err != nil {
		return Inbound{}, err
	}
	w, err := ParseWrapper(env.Message.Content)
	if err != nil {
		return Inbound{}, err
	}
	if w.From != from {
		return Inbound{}, errors.New("peer: wrapper from differs from envelope from")
	}
	body := w.Body
	in := Inbound{From: from, FromName: w.FromName, Mode: w.Mode, Body: body}
	if n := agentNick.FindStringSubmatch(body); n != nil {
		if err := ParseNick(n[1]); err != nil {
			return Inbound{}, err
		}
		in.AgentNick = n[1]
	}
	return in, nil
}

// parseClass is the inverse of Class.String for a received from-mode.
func parseClass(s string) (Class, error) {
	switch s {
	case "prompting":
		return Prompting, nil
	case "bypass":
		return Bypass, nil
	}
	return 0, fmt.Errorf("peer: unknown from-mode %q", s)
}

// Wrapper is a parsed <cross-session-message> after the guards of D:362-364.
type Wrapper struct {
	From     Addr
	FromName string
	Mode     Class
	Body     string
}

// ParseWrapper validates and unwraps message content received by a session.
func ParseWrapper(content string) (Wrapper, error) {
	m := wrapRE.FindStringSubmatch(content)
	if m == nil {
		return Wrapper{}, errors.New("peer: content is not a cross-session-message wrapper")
	}
	from, err := ParseAddr(m[1])
	if err != nil {
		return Wrapper{}, err
	}
	if !hopRE.MatchString(m[2]) {
		return Wrapper{}, errors.New("peer: hop-chain has a forbidden character")
	}
	mode, err := parseClass(m[4])
	if err != nil {
		return Wrapper{}, err
	}
	if err := ParseNick(m[3]); err != nil {
		return Wrapper{}, err
	}
	if strings.Contains(strings.ToLower(m[5]), closePrefix) {
		return Wrapper{}, errors.New("peer: body contains " + closeTag)
	}
	return Wrapper{From: from, FromName: m[3], Mode: mode, Body: m[5]}, nil
}
