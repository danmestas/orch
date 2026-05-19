package natsurl

import (
	"bytes"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// fakeFS lets each test stage an in-memory set of file contents.
type fakeFS map[string]string

func (f fakeFS) read(path string) ([]byte, error) {
	if v, ok := f[path]; ok {
		return []byte(v), nil
	}
	return nil, &fs.PathError{Op: "open", Path: path, Err: errors.New("file does not exist")}
}

// envMap is a fake os.Getenv backed by a map.
type envMap map[string]string

func (e envMap) get(k string) string { return e[k] }

func newDeps(env envMap, home string, files fakeFS) (
	func(string) string,
	func() (string, error),
	func(string) ([]byte, error),
) {
	return env.get,
		func() (string, error) { return home, nil },
		files.read
}

// TestNATSURL_ReadsHubNATSURL verifies that the primary file
// ~/.sesh/hub.nats.url wins when present, no warning is emitted, and the
// legacy hub.url is not consulted.
func TestNATSURL_ReadsHubNATSURL(t *testing.T) {
	home := "/home/test"
	files := fakeFS{
		filepath.Join(home, ".sesh", "hub.nats.url"): "nats://primary.example:4222\n",
		// hub.url present but should be ignored:
		filepath.Join(home, ".sesh", "hub.url"): "nats://leaf.example:7422\n",
	}
	env := envMap{}
	getenv, homeFn, readFile := newDeps(env, home, files)

	var stderr bytes.Buffer
	got := resolve("orch-test", "", getenv, homeFn, readFile, &stderr)

	if want := "nats://primary.example:4222"; got != want {
		t.Errorf("URL: got %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("expected no stderr output, got %q", stderr.String())
	}
}

// TestNATSURL_FallbackToHubURL_WithWarning verifies that when
// hub.nats.url is absent, the legacy hub.url is read AND a deprecation
// warning is written to stderr with the supplied binary name.
func TestNATSURL_FallbackToHubURL_WithWarning(t *testing.T) {
	home := "/home/test"
	files := fakeFS{
		filepath.Join(home, ".sesh", "hub.url"): "nats://legacy.example:4222\n",
	}
	env := envMap{}
	getenv, homeFn, readFile := newDeps(env, home, files)

	var stderr bytes.Buffer
	got := resolve("orch-goal-stop-account-daemon", "", getenv, homeFn, readFile, &stderr)

	if want := "nats://legacy.example:4222"; got != want {
		t.Errorf("URL: got %q, want %q", got, want)
	}
	out := stderr.String()
	if !strings.Contains(out, "orch-goal-stop-account-daemon:") {
		t.Errorf("warning missing binary name; got %q", out)
	}
	if !strings.Contains(out, "legacy ~/.sesh/hub.url") {
		t.Errorf("warning missing legacy-file marker; got %q", out)
	}
	if !strings.Contains(out, "deprecated") {
		t.Errorf("warning missing 'deprecated'; got %q", out)
	}
	if !strings.Contains(out, "upgrade sesh") {
		t.Errorf("warning missing upgrade hint; got %q", out)
	}
}

// TestNATSURL_BothMissing_DefaultsLocalhost verifies the localhost
// fallback when neither sesh file exists and no override / env is set.
func TestNATSURL_BothMissing_DefaultsLocalhost(t *testing.T) {
	home := "/home/test"
	files := fakeFS{} // no files
	env := envMap{}
	getenv, homeFn, readFile := newDeps(env, home, files)

	var stderr bytes.Buffer
	got := resolve("orch", "", getenv, homeFn, readFile, &stderr)

	if got != DefaultURL {
		t.Errorf("URL: got %q, want %q", got, DefaultURL)
	}
	if stderr.Len() != 0 {
		t.Errorf("expected no stderr output, got %q", stderr.String())
	}
}

// TestNATSURL_EnvOverride verifies that $NATS_URL wins over any sesh
// file, and that no warning is emitted even when hub.url exists.
func TestNATSURL_EnvOverride(t *testing.T) {
	home := "/home/test"
	files := fakeFS{
		filepath.Join(home, ".sesh", "hub.nats.url"): "nats://primary.example:4222\n",
		filepath.Join(home, ".sesh", "hub.url"):      "nats://legacy.example:4222\n",
	}
	env := envMap{"NATS_URL": "nats://env.example:4222"}
	getenv, homeFn, readFile := newDeps(env, home, files)

	var stderr bytes.Buffer
	got := resolve("orch", "", getenv, homeFn, readFile, &stderr)

	if want := "nats://env.example:4222"; got != want {
		t.Errorf("URL: got %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("expected no stderr output, got %q", stderr.String())
	}
}

// TestNATSURL_OverrideWinsOverEverything verifies that a non-empty
// override (e.g. --nats flag) outranks env and all on-disk files.
func TestNATSURL_OverrideWinsOverEverything(t *testing.T) {
	home := "/home/test"
	files := fakeFS{
		filepath.Join(home, ".sesh", "hub.nats.url"): "nats://primary.example:4222\n",
	}
	env := envMap{"NATS_URL": "nats://env.example:4222"}
	getenv, homeFn, readFile := newDeps(env, home, files)

	var stderr bytes.Buffer
	got := resolve("orch", "  nats://flag.example:4222  ", getenv, homeFn, readFile, &stderr)

	if want := "nats://flag.example:4222"; got != want {
		t.Errorf("URL: got %q, want %q", got, want)
	}
}

// TestNATSURL_EmptyHubNATSURL_FallsThroughToHubURL verifies that an
// empty hub.nats.url file (whitespace only) is treated as absent and
// the legacy fallback fires.
func TestNATSURL_EmptyHubNATSURL_FallsThroughToHubURL(t *testing.T) {
	home := "/home/test"
	files := fakeFS{
		filepath.Join(home, ".sesh", "hub.nats.url"): "   \n",
		filepath.Join(home, ".sesh", "hub.url"):      "nats://legacy.example:4222\n",
	}
	env := envMap{}
	getenv, homeFn, readFile := newDeps(env, home, files)

	var stderr bytes.Buffer
	got := resolve("orch", "", getenv, homeFn, readFile, &stderr)

	if want := "nats://legacy.example:4222"; got != want {
		t.Errorf("URL: got %q, want %q", got, want)
	}
	if !strings.Contains(stderr.String(), "deprecated") {
		t.Errorf("expected deprecation warning, got %q", stderr.String())
	}
}

// TestNATSURL_EmptyBinName_FallsBackToOrch verifies the warning prefix
// defaults to "orch" when the caller passes an empty binary name.
func TestNATSURL_EmptyBinName_FallsBackToOrch(t *testing.T) {
	home := "/home/test"
	files := fakeFS{
		filepath.Join(home, ".sesh", "hub.url"): "nats://legacy.example:4222\n",
	}
	env := envMap{}
	getenv, homeFn, readFile := newDeps(env, home, files)

	var stderr bytes.Buffer
	_ = resolve("", "", getenv, homeFn, readFile, &stderr)

	if !strings.HasPrefix(stderr.String(), "orch:") {
		t.Errorf("expected warning to start with 'orch:', got %q", stderr.String())
	}
}
