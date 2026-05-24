package registry

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAliasReader_MissingFileReturnsEmpty(t *testing.T) {
	a := NewAliasReader(filepath.Join(t.TempDir(), "nope"))
	m, err := a.Aliases(context.Background())
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("want empty map, got %v", m)
	}
}

func TestAliasReader_ParsesWellFormedEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "orch-aliases")
	if err := os.WriteFile(path, []byte(`
# comment
engineer=%64
reviewer = %17
verifier=%99

# trailing comment
`), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	a := NewAliasReader(path)
	m, err := a.Aliases(context.Background())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := map[string]string{"engineer": "%64", "reviewer": "%17", "verifier": "%99"}
	if len(m) != len(want) {
		t.Fatalf("size: got %d want %d (%v)", len(m), len(want), m)
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("entry %q: got %q want %q", k, m[k], v)
		}
	}
}

func TestAliasReader_MalformedLinesSurfaceErrorButKeepValidEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "orch-aliases")
	if err := os.WriteFile(path, []byte("good=%64\nbroken-line-no-equals\nalso-good=%99\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	a := NewAliasReader(path)
	m, err := a.Aliases(context.Background())
	if err == nil {
		t.Errorf("malformed line should surface error")
	}
	if m["good"] != "%64" || m["also-good"] != "%99" {
		t.Errorf("valid entries should still parse: %v", m)
	}
}
