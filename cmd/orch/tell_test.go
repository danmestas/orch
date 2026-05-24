package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/danmestas/orch/internal/registry"
	"github.com/danmestas/orch/internal/synadia"
)

// TestLookupTarget_GoldenParity exercises the same scenarios bin/orch-tell
// + bin/orch-spy handled via shelling out to `orch-registry lookup`. The
// scenarios match the old test-orch-observer-role.sh + test-orch-tell-discovery.sh
// fixtures.
func TestLookupTarget_GoldenParity(t *testing.T) {
	workers := []registry.Worker{
		{PaneID: "%64", Role: "worker", Name: "wk-engineer", InstanceID: "engineer-12-34", Session: "engineer", Subjects: registry.Subjects{Prompt: "agents.prompt.cc.dmestas.engineer"}, Alive: true},
		{PaneID: "%65", Role: "observer", Name: "spy-watcher", InstanceID: "watcher-99-00", Session: "stasi-w99", Subjects: registry.Subjects{Prompt: "agents.prompt.cc.dmestas.watcher"}, Alive: true},
		{PaneID: "%99", Role: "operator", Name: "op-pane", InstanceID: "op-aa-bb", Session: "operator", Subjects: registry.Subjects{Prompt: "agents.prompt.cc.dmestas.op"}, Alive: true},
	}

	cases := []struct {
		target    string
		wantPane  string
		wantFound bool
	}{
		// alias-name lookup
		{"wk-engineer", "%64", true},
		// raw pane id
		{"%65", "%65", true},
		// operator special target
		{"operator", "%99", true},
		{"op", "%99", true},
		// session-label fallback
		{"stasi-w99", "%65", true},
		// instance-id slug
		{"engineer-12-34", "%64", true},
		// miss
		{"nope", "", false},
		// raw pane id miss (registry has %64/%65/%99 only)
		{"%9999", "", false},
	}
	for _, c := range cases {
		w, ok := lookupTarget(workers, c.target)
		if ok != c.wantFound {
			t.Errorf("lookupTarget(%q): found=%v want=%v", c.target, ok, c.wantFound)
			continue
		}
		if ok && w.PaneID != c.wantPane {
			t.Errorf("lookupTarget(%q): pane=%s want=%s", c.target, w.PaneID, c.wantPane)
		}
	}
}

// TestObserverGuard mirrors the worker→observer refusal rule in bin/orch-tell.
// The guard is implemented inline inside runTell; this test exercises the
// equivalent predicates directly so the rule survives refactoring.
func TestObserverGuard(t *testing.T) {
	cases := []struct {
		name       string
		senderPane string // ORCH_PANE_ID env at call time
		force      bool
		targetRole string
		want       string // "allow" or "refuse"
	}{
		{"worker→observer no force", "%999", false, "observer", "refuse"},
		{"worker→observer with force", "%999", true, "observer", "allow"},
		{"worker→worker no force", "%999", false, "worker", "allow"},
		{"operator→observer no force", "", false, "observer", "allow"},
		{"operator→worker no force", "", false, "worker", "allow"},
		{"unknown role default-allow", "%999", false, "", "allow"}, // empty role does not match "observer"
	}
	for _, c := range cases {
		got := classifyTell(c.senderPane, c.force, c.targetRole)
		if got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

// classifyTell exists for testability — the actual guard runs inline in
// runTell against ORCH_PANE_ID env. Keep this in lockstep with that
// branch (only the boolean test changes there).
func classifyTell(senderPane string, force bool, targetRole string) string {
	if senderPane != "" && !force && targetRole == "observer" {
		return "refuse"
	}
	return "allow"
}

// TestExitCodeForServiceError is duplicated in internal/synadia/synadia_test.go
// at the unit level; this test asserts the cmd/orch wiring uses it correctly
// — surfaceServiceError surfaces both the header and the §9.1 body.
func TestSurfaceServiceError_ExitCodes(t *testing.T) {
	// Redirect stderr to /dev/null for the duration so test output stays clean.
	prev := os.Stderr
	devNull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stderr = devNull
	defer func() {
		os.Stderr = prev
		devNull.Close() //nolint:errcheck
	}()

	cases := []struct {
		code int
		want int
	}{
		{400, synadia.ExitBadRequest},
		{401, synadia.ExitUnauthorized},
		{403, synadia.ExitUnauthorized},
		{404, synadia.ExitNotFound},
		{409, synadia.ExitConflict},
		{429, synadia.ExitTooManyRequests},
		{500, synadia.ExitGeneric},
		{503, synadia.ExitGeneric},
	}
	for _, c := range cases {
		err := surfaceServiceError(c.code, "msg", `{"message":"x","retry_after_s":3}`)
		ee, ok := err.(*exitError)
		if !ok {
			t.Fatalf("code=%d: expected *exitError, got %T", c.code, err)
		}
		if ee.code != c.want {
			t.Errorf("code=%d: exit=%d want=%d", c.code, ee.code, c.want)
		}
	}
}

// TestReadPrompt_Positional verifies prompt assembly from positional args.
func TestReadPrompt_Positional(t *testing.T) {
	got, err := readPrompt([]string{"hello", "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello world" {
		t.Errorf("readPrompt: %q want %q", got, "hello world")
	}
}

// TestWriteSendLog_Format checks the JSON shape the send-log writer
// emits is compatible with the old bin/orch-tell entry shape.
func TestWriteSendLog_Format(t *testing.T) {
	tmp := t.TempDir()
	logPath := tmp + "/send.log"
	t.Setenv("ORCH_SEND_LOG", logPath)
	t.Setenv("ORCH_PANE_ID", "%500")

	writeSendLog("%600", "the prompt body")

	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	line := strings.TrimSpace(string(b))
	// Smoke-check: every required field appears in the JSON.
	for _, want := range []string{
		`"ts_ns":`,
		`"pane":"%600"`,
		`"sender":"%500"`,
		`"prompt_preview":"the prompt body"`,
		`"prompt_len":15`,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("send-log line missing %s: %s", want, line)
		}
	}
}

// TestPeekHumanAge verifies the same bucket / age shorthand bin/orch-peek used.
func TestPeekHumanAge(t *testing.T) {
	cases := []struct {
		s    int64
		want string
	}{
		{5, "5s"},
		{59, "59s"},
		{60, "1m"},
		{3599, "59m"},
		{3600, "1h"},
		{86399, "23h"},
		{86400, "1d"},
		{172800, "2d"},
	}
	for _, c := range cases {
		got := humanAge(c.s)
		if got != c.want {
			t.Errorf("humanAge(%d) = %q want %q", c.s, got, c.want)
		}
	}
}

func TestPeekBucketFor(t *testing.T) {
	cases := []struct {
		s    int64
		want string
	}{
		{0, "ACTIVE"},
		{29, "ACTIVE"},
		{30, "recent"},
		{299, "recent"},
		{300, "idle"},
		{3600, "idle"},
	}
	for _, c := range cases {
		got := bucketFor(c.s)
		if got != c.want {
			t.Errorf("bucketFor(%d) = %q want %q", c.s, got, c.want)
		}
	}
}

func TestPeekParseSince(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"30s", 30 * time.Second},
		{"5m", 5 * time.Minute},
		{"2h", 2 * time.Hour},
		{"1d", 24 * time.Hour},
		{"42", 42 * time.Second}, // bare integer = seconds (matches bash)
	}
	for _, c := range cases {
		got, err := parseSince(c.in)
		if err != nil {
			t.Errorf("parseSince(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseSince(%q): %v want %v", c.in, got, c.want)
		}
	}
	if _, err := parseSince("abc"); err == nil {
		t.Errorf("parseSince(abc): expected error")
	}
}
