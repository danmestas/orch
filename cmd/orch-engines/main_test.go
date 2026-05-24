package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// build the orch-engines binary once into the test's tempdir, then run
// it through its subcommands. Mirrors cmd/orch-workflow's test pattern.
func buildOrchEngines(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "orch-engines")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build orch-engines: %v\n%s", err, out)
	}
	return bin
}

func TestMainValidatePass(t *testing.T) {
	bin := buildOrchEngines(t)
	cmd := exec.Command(bin, "validate", "tmux", "tmux")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("validate tmux tmux: rc != 0: %v\nout: %s", err, out)
	}
}

func TestMainValidateRejection(t *testing.T) {
	bin := buildOrchEngines(t)
	cmd := exec.Command(bin, "validate", "tmux", "cmux")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Errorf("validate tmux cmux: expected non-zero rc, got 0\nout: %s", out)
	}
	if !strings.Contains(string(out), "unsupported composition") {
		t.Errorf("validate rejection missing diagnostic: %s", out)
	}
}

func TestMainValidateWrongArity(t *testing.T) {
	bin := buildOrchEngines(t)
	cmd := exec.Command(bin, "validate", "tmux")
	_, err := cmd.CombinedOutput()
	if err == nil {
		t.Errorf("validate with 1 arg: expected error, got nil")
	}
}

func TestMainList(t *testing.T) {
	bin := buildOrchEngines(t)
	out, err := exec.Command(bin, "list").CombinedOutput()
	if err != nil {
		t.Fatalf("list: %v\nout: %s", err, out)
	}
	if !strings.Contains(string(out), "persistence=tmux layout=tmux") {
		t.Errorf("list output missing the Phase A default: %s", out)
	}
}

func TestMainNoSubcommand(t *testing.T) {
	bin := buildOrchEngines(t)
	_, err := exec.Command(bin).CombinedOutput()
	if err == nil {
		t.Error("no subcommand: expected error, got nil")
	}
}

func TestMainUnknownSubcommand(t *testing.T) {
	bin := buildOrchEngines(t)
	out, err := exec.Command(bin, "bogus").CombinedOutput()
	if err == nil {
		t.Errorf("bogus subcommand: expected non-zero rc, got 0\nout: %s", out)
	}
	if !strings.Contains(string(out), "unknown subcommand") {
		t.Errorf("unknown-subcommand diagnostic missing: %s", out)
	}
}
