package tmuxctl

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// mockTmux is a scripted TmuxRunner for tests. Each call consumes one
// entry from the corresponding script slice (or returns the last entry
// indefinitely when the slice is exhausted).
type mockTmux struct {
	panes    [][]string
	commands []string
	captures []string
	calls    struct {
		list, cmd, capture int
	}
	listErr error
}

func (m *mockTmux) ListPaneIDs() ([]string, error) {
	defer func() { m.calls.list++ }()
	if m.listErr != nil {
		return nil, m.listErr
	}
	if len(m.panes) == 0 {
		return nil, nil
	}
	i := m.calls.list
	if i >= len(m.panes) {
		i = len(m.panes) - 1
	}
	return m.panes[i], nil
}

func (m *mockTmux) CurrentCommand(_ string) (string, error) {
	defer func() { m.calls.cmd++ }()
	if len(m.commands) == 0 {
		return "", nil
	}
	i := m.calls.cmd
	if i >= len(m.commands) {
		i = len(m.commands) - 1
	}
	return m.commands[i], nil
}

func (m *mockTmux) CapturePane(_ string) (string, error) {
	defer func() { m.calls.capture++ }()
	if len(m.captures) == 0 {
		return "", nil
	}
	i := m.calls.capture
	if i >= len(m.captures) {
		i = len(m.captures) - 1
	}
	return m.captures[i], nil
}

func TestVerifyReadyOnTitleRename(t *testing.T) {
	mt := &mockTmux{
		panes:    [][]string{{"%64"}},
		commands: []string{"claude"},
		captures: []string{""},
	}
	res := Verify(VerifyOpts{
		PaneID:  "%64",
		Agent:   "claude",
		Backoff: []time.Duration{10 * time.Millisecond},
		Timeout: 1 * time.Second,
		Tmux:    mt,
		sleep:   func(time.Duration) {},
	})
	if res.State != StateReady {
		t.Fatalf("want StateReady, got %v", res.State)
	}
	if res.Attempts != 1 {
		t.Fatalf("want 1 attempt, got %d", res.Attempts)
	}
}

func TestVerifyReadyOnBanner(t *testing.T) {
	// Title still on shell, but capture-pane shows the banner. Mirrors
	// the cold-start case where a heavy outfit + many MCP servers push
	// the title-rename past the readiness window.
	mt := &mockTmux{
		panes:    [][]string{{"%64"}},
		commands: []string{"zsh"},
		captures: []string{"... Claude Code ..."},
	}
	res := Verify(VerifyOpts{
		PaneID:  "%64",
		Agent:   "claude",
		Backoff: []time.Duration{1 * time.Millisecond},
		Timeout: 1 * time.Second,
		Tmux:    mt,
		sleep:   func(time.Duration) {},
	})
	if res.State != StateReady {
		t.Fatalf("want StateReady, got %v", res.State)
	}
}

func TestVerifyPaneDied(t *testing.T) {
	mt := &mockTmux{
		// Pane gone on first probe.
		panes:    [][]string{{}},
		commands: []string{"zsh"},
		captures: []string{""},
	}
	res := Verify(VerifyOpts{
		PaneID:  "%64",
		Agent:   "claude",
		Backoff: []time.Duration{1 * time.Millisecond},
		Timeout: 1 * time.Second,
		Tmux:    mt,
		sleep:   func(time.Duration) {},
	})
	if res.State != StateDied {
		t.Fatalf("want StateDied, got %v", res.State)
	}
}

func TestVerifyMissingBinary(t *testing.T) {
	// Capture buffer shows the bash "command not found" shape.
	mt := &mockTmux{
		panes:    [][]string{{"%64"}},
		commands: []string{"zsh"},
		captures: []string{"sh: claude: command not found"},
	}
	res := Verify(VerifyOpts{
		PaneID:  "%64",
		Agent:   "claude",
		Backoff: []time.Duration{1 * time.Millisecond},
		Timeout: 1 * time.Second,
		Tmux:    mt,
		sleep:   func(time.Duration) {},
	})
	if res.State != StateMissingBinary {
		t.Fatalf("want StateMissingBinary, got %v", res.State)
	}
}

func TestVerifyTimedOut(t *testing.T) {
	mt := &mockTmux{
		panes:    [][]string{{"%64"}},
		commands: []string{"zsh", "zsh", "zsh", "zsh"},
		captures: []string{"", "", "", ""},
	}
	res := Verify(VerifyOpts{
		PaneID:  "%64",
		Agent:   "claude",
		Backoff: []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond},
		Timeout: 1 * time.Second,
		Tmux:    mt,
		sleep:   func(time.Duration) {},
	})
	if res.State != StateTimedOut {
		t.Fatalf("want StateTimedOut, got %v", res.State)
	}
	if res.Attempts != 3 {
		t.Fatalf("want 3 attempts, got %d", res.Attempts)
	}
}

func TestVerifyShellListMatchesBash(t *testing.T) {
	// The shell-name set must match executors/tmux/spawn.sh's case.
	// If a new shell is added there it must be added here too.
	want := []string{"", "zsh", "bash", "sh", "fish", "dash", "ksh"}
	for _, name := range want {
		if !shellNames[name] {
			t.Errorf("shellNames missing entry %q", name)
		}
	}
}

func TestParseBackoff(t *testing.T) {
	tests := []struct {
		in      string
		want    []time.Duration
		wantErr bool
	}{
		{"", DefaultBackoff, false},
		{"1,2,4,8", []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}, false},
		{"0.5,1", []time.Duration{500 * time.Millisecond, 1 * time.Second}, false},
		{"1,,2", []time.Duration{1 * time.Second, 2 * time.Second}, false}, // empty entries tolerated
		{"abc", nil, true},
		{"-1", nil, true},
	}
	for _, tt := range tests {
		got, err := ParseBackoff(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseBackoff(%q) err=%v, wantErr=%v", tt.in, err, tt.wantErr)
			continue
		}
		if tt.wantErr {
			continue
		}
		if len(got) != len(tt.want) {
			t.Errorf("ParseBackoff(%q) len=%d, want %d", tt.in, len(got), len(tt.want))
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("ParseBackoff(%q)[%d] = %v, want %v", tt.in, i, got[i], tt.want[i])
			}
		}
	}
}

func TestParseTimeout(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"", DefaultVerifyTimeout},
		{"30", 30 * time.Second},
		{"0", DefaultVerifyTimeout},      // zero → default
		{"-5", DefaultVerifyTimeout},     // negative → default
		{"junk", DefaultVerifyTimeout},   // unparseable → default
	}
	for _, tt := range tests {
		got := ParseTimeout(tt.in)
		if got != tt.want {
			t.Errorf("ParseTimeout(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestHasMissingBinaryError(t *testing.T) {
	tests := []struct {
		buf, agent string
		want       bool
	}{
		{"", "claude", false},
		{"some output", "claude", false},
		{"sh: claude: command not found", "claude", true},
		{"zsh: command not found: claude", "claude", true},
		{"dash: claude: not found", "claude", true},
		{"/usr/bin/env: claude: No such file or directory", "claude", true},
		{"sh: codex: command not found", "claude", false}, // wrong agent
	}
	for _, tt := range tests {
		got := hasMissingBinaryError(tt.buf, tt.agent)
		if got != tt.want {
			t.Errorf("hasMissingBinaryError(%q, %q) = %v, want %v",
				tt.buf, tt.agent, got, tt.want)
		}
	}
}

func TestCanonicalAgentName(t *testing.T) {
	if got := CanonicalAgentName("claude"); got != "claude-code" {
		t.Errorf("CanonicalAgentName(claude) = %q, want claude-code", got)
	}
	if got := CanonicalAgentName("codex"); got != "codex" {
		t.Errorf("CanonicalAgentName(codex) = %q, want codex", got)
	}
}

func TestPaneAliveListErrFalse(t *testing.T) {
	mt := &mockTmux{listErr: errors.New("tmux dead")}
	if paneAlive(mt, "%64") {
		t.Fatal("paneAlive should be false when ListPaneIDs errors")
	}
}

func TestVerifyConsumesBackoffSlice(t *testing.T) {
	// Each backoff entry should drive exactly one probe.
	mt := &mockTmux{
		panes:    [][]string{{"%64"}},
		commands: []string{"zsh", "zsh", "zsh"},
		captures: []string{"", "", ""},
	}
	res := Verify(VerifyOpts{
		PaneID:  "%64",
		Agent:   "claude",
		Backoff: []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond},
		Timeout: 1 * time.Second,
		Tmux:    mt,
		sleep:   func(time.Duration) {},
	})
	if res.Attempts != 3 {
		t.Errorf("want 3 attempts, got %d", res.Attempts)
	}
	if res.State != StateTimedOut {
		t.Errorf("want StateTimedOut, got %v", res.State)
	}
}

func TestDefaultBackoffMatchesBashContract(t *testing.T) {
	// The bash default is `1,2,4,8`. If anyone tweaks DefaultBackoff,
	// this assertion makes the contract drift visible.
	want := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	if len(DefaultBackoff) != len(want) {
		t.Fatalf("DefaultBackoff len=%d, want %d", len(DefaultBackoff), len(want))
	}
	for i := range want {
		if DefaultBackoff[i] != want[i] {
			t.Errorf("DefaultBackoff[%d] = %v, want %v", i, DefaultBackoff[i], want[i])
		}
	}
}

func TestAgentBannersContractDocumented(t *testing.T) {
	// Every banner must be a non-empty string OR explicit empty (pi).
	// A typo in the agent key would be silent — pin the four known
	// agents here so a typo surfaces in CI.
	want := map[string]string{
		"claude": "Claude Code",
		"gemini": "Gemini CLI",
		"codex":  "OpenAI Codex",
		"pi":     "",
	}
	for k, v := range want {
		got, ok := agentBanners[k]
		if !ok {
			t.Errorf("agentBanners missing %q", k)
			continue
		}
		if got != v {
			t.Errorf("agentBanners[%q] = %q, want %q", k, got, v)
		}
		// Banner substring must not be hijacked by a generic cwd prompt.
		// Cheap sanity check — banners should be longer than 3 chars.
		if v != "" && len(strings.TrimSpace(v)) < 4 {
			t.Errorf("agentBanners[%q] too short: %q", k, v)
		}
	}
}
