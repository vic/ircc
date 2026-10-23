package rooms

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseName(t *testing.T) {
	ok := []string{"&" + strings.Repeat("a", 61), "&review", "&a-b_c", "&ünï"}
	bad := []string{"&a\u200bb", "&a\u202eb", "&a\ufeffb", "&a\u2066b", "&a,b", "&" + strings.Repeat("a", 62), "&" + strings.Repeat("é", 22), "", "&", "review", `&a"b`, "&a/b", `&a\b`, "&a<b", "&a>b", "&a b", "&a\tb", "&a\x00b", "&a\x7fb", "&a\xffb"}
	for _, s := range ok {
		if n, err := ParseName(s); err != nil || n.String() != s {
			t.Errorf("%q: %v %q", s, err, n.String())
		}
	}
	for _, s := range bad {
		if _, err := ParseName(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

func mustName(t *testing.T, s string) Name {
	t.Helper()
	n, err := ParseName(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSaveLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rooms")
	if rs, errs := Load(dir); len(rs) != 0 || len(errs) != 0 {
		t.Fatalf("missing dir: %v %v", rs, errs)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r := Room{Name: mustName(t, "&review"), Topic: "t", Members: []Member{{Session: "s1", Nick: "bob"}}}
	if err := Save(dir, r); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "%26review.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	if es, _ := os.ReadDir(dir); len(es) != 1 {
		t.Errorf("temp file left: %v", es)
	}
	got, errs := Load(dir)
	if len(errs) != 0 || !reflect.DeepEqual(got, []Room{r}) {
		t.Fatalf("got %+v %v", got, errs)
	}
}

func TestLoadBadFiles(t *testing.T) {
	dir := t.TempDir()
	good := Room{Name: mustName(t, "&ok")}
	if err := Save(dir, good); err != nil {
		t.Fatal(err)
	}
	must(t, os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{`), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, "inj.json"), []byte(`{"name":"&a\"b"}`), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, "evil.json"), []byte(`{"name":"&ok"}`), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, "big.json"), []byte(`{"topic":"`+strings.Repeat("x", 70000)+`"}`), 0o600))
	must(t, os.Mkdir(filepath.Join(dir, "d.json"), 0o700))
	must(t, os.WriteFile(filepath.Join(dir, "note.txt"), []byte(`x`), 0o600))
	got, errs := Load(dir)
	if len(got) != 1 || got[0].Name != good.Name || len(errs) != 4 {
		t.Fatalf("got %+v errs %v", got, errs)
	}
}

func TestSaveZeroRoom(t *testing.T) {
	if err := Save(t.TempDir(), Room{}); err == nil {
		t.Fatal("zero Name accepted")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestMemberNickChecked(t *testing.T) {
	dir := t.TempDir()
	r := Room{Name: mustName(t, "&m"), Members: []Member{{Session: "s", Nick: `a"b`}}}
	if err := Save(dir, r); err == nil {
		t.Fatal("Save accepted bad nick")
	}
	must(t, os.WriteFile(filepath.Join(dir, "%26m.json"), []byte(`{"name":"&m","members":[{"session":"s","nick":"a<b"}]}`), 0o600))
	if rs, errs := Load(dir); len(rs) != 0 || len(errs) != 1 {
		t.Fatalf("got %v %v", rs, errs)
	}
}
