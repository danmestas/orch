package tmux_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danmestas/orch/internal/instance/mock"
	"github.com/danmestas/orch/internal/layout"
	tmuxlayout "github.com/danmestas/orch/internal/layout/tmux"
)

// Compile-time assertion that *Engine satisfies layout.Engine.
var _ layout.Engine = (*tmuxlayout.Engine)(nil)

func TestEngineName(t *testing.T) {
	e := tmuxlayout.New()
	if e.Name() != "tmux" {
		t.Errorf("Name() = %q, want %q", e.Name(), "tmux")
	}
}

func TestSpawnEmptySlugIsNoop(t *testing.T) {
	dir := t.TempDir()
	aliasFile := filepath.Join(dir, "aliases")

	e := tmuxlayout.New(
		tmuxlayout.WithAliasesFile(aliasFile),
		tmuxlayout.WithTmuxBin("/usr/bin/false"), // would fail if invoked
	)
	h := &mock.Handle{IDValue: "", LocatorValue: "%64"}
	if err := e.Spawn(layout.SpawnSpec{Slug: ""}, h); err != nil {
		t.Fatalf("Spawn empty-slug: %v", err)
	}
	// Alias file should NOT have been created.
	if _, err := os.Stat(aliasFile); !os.IsNotExist(err) {
		t.Errorf("Spawn empty-slug created alias file: %v", err)
	}
}

func TestSpawnRejectsNonPaneLocator(t *testing.T) {
	e := tmuxlayout.New(
		tmuxlayout.WithAliasesFile(filepath.Join(t.TempDir(), "aliases")),
		tmuxlayout.WithTmuxBin("/usr/bin/false"),
	)
	h := &mock.Handle{IDValue: "alpha", LocatorValue: "not-a-pane"}
	err := e.Spawn(layout.SpawnSpec{Slug: "alpha"}, h)
	if err == nil {
		t.Fatal("Spawn with non-pane locator: want error, got nil")
	}
	if !strings.Contains(err.Error(), "not a tmux pane id") {
		t.Errorf("Spawn err lacks 'not a tmux pane id': %v", err)
	}
}

func TestSpawnWritesAliasFile(t *testing.T) {
	dir := t.TempDir()
	aliasFile := filepath.Join(dir, "aliases")

	e := tmuxlayout.New(
		tmuxlayout.WithAliasesFile(aliasFile),
		tmuxlayout.WithTmuxBin("/usr/bin/true"), // select-pane is best-effort
	)

	h := &mock.Handle{IDValue: "alpha", LocatorValue: "%42"}
	if err := e.Spawn(layout.SpawnSpec{Slug: "alpha"}, h); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	got, err := os.ReadFile(aliasFile)
	if err != nil {
		t.Fatalf("read alias file: %v", err)
	}
	if !strings.Contains(string(got), "alpha=%42") {
		t.Errorf("alias file missing 'alpha=%%42'; got:\n%s", got)
	}
}

func TestSpawnReplacesPriorEntry(t *testing.T) {
	dir := t.TempDir()
	aliasFile := filepath.Join(dir, "aliases")
	// Pre-seed with a stale entry + an unrelated entry that should
	// survive.
	if err := os.WriteFile(aliasFile, []byte("alpha=%1\nbeta=%2\n"), 0o644); err != nil {
		t.Fatalf("seed alias file: %v", err)
	}

	e := tmuxlayout.New(
		tmuxlayout.WithAliasesFile(aliasFile),
		tmuxlayout.WithTmuxBin("/usr/bin/true"),
	)

	h := &mock.Handle{IDValue: "alpha", LocatorValue: "%99"}
	if err := e.Spawn(layout.SpawnSpec{Slug: "alpha"}, h); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	got, err := os.ReadFile(aliasFile)
	if err != nil {
		t.Fatalf("read alias file: %v", err)
	}
	gotS := string(got)
	if strings.Contains(gotS, "alpha=%1") {
		t.Errorf("stale alpha=%%1 not removed; got:\n%s", gotS)
	}
	if !strings.Contains(gotS, "alpha=%99") {
		t.Errorf("new alpha=%%99 not written; got:\n%s", gotS)
	}
	if !strings.Contains(gotS, "beta=%2") {
		t.Errorf("unrelated beta=%%2 collateral damage; got:\n%s", gotS)
	}
}

func TestCloseRemovesEntry(t *testing.T) {
	dir := t.TempDir()
	aliasFile := filepath.Join(dir, "aliases")
	if err := os.WriteFile(aliasFile, []byte("alpha=%1\nbeta=%2\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	e := tmuxlayout.New(
		tmuxlayout.WithAliasesFile(aliasFile),
		tmuxlayout.WithTmuxBin("/usr/bin/true"),
	)

	if err := e.Close("alpha"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, _ := os.ReadFile(aliasFile)
	if strings.Contains(string(got), "alpha=") {
		t.Errorf("Close didn't remove alpha; got:\n%s", got)
	}
	if !strings.Contains(string(got), "beta=%2") {
		t.Errorf("Close removed beta; got:\n%s", got)
	}
}

func TestCloseIdempotent(t *testing.T) {
	dir := t.TempDir()
	aliasFile := filepath.Join(dir, "aliases")

	e := tmuxlayout.New(
		tmuxlayout.WithAliasesFile(aliasFile),
		tmuxlayout.WithTmuxBin("/usr/bin/true"),
	)

	// Close on missing-file is fine.
	if err := e.Close("nonexistent"); err != nil {
		t.Errorf("Close on missing file: %v", err)
	}
	// Close on empty slug is no-op.
	if err := e.Close(""); err != nil {
		t.Errorf("Close empty slug: %v", err)
	}
}

func TestArrangePresetUnknownErrors(t *testing.T) {
	e := tmuxlayout.New(tmuxlayout.WithTmuxBin("/usr/bin/true"))
	err := e.Arrange("nonsense")
	if err == nil {
		t.Error("Arrange(nonsense): want error, got nil")
	}
}

func TestArrangePresetEmptyIsNoop(t *testing.T) {
	e := tmuxlayout.New(tmuxlayout.WithTmuxBin("/usr/bin/false"))
	if err := e.Arrange(""); err != nil {
		t.Errorf("Arrange(\"\"): %v", err)
	}
	if err := e.Arrange("default"); err != nil {
		t.Errorf("Arrange(\"default\"): %v", err)
	}
}
