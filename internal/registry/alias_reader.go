package registry

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// AliasReaderFile is the AliasReader backed by ~/.config/orch-aliases (or
// the path overridden in the constructor).
//
// Format: one entry per line, "name=%pane_id". Lines starting with "#"
// and blank lines are ignored. Whitespace around "=" is tolerated.
//
// File absent is NOT an error — operators may run without aliases.
//
// Per ADR-0003 the alias file is not a separate source of truth; it is
// an operator-side overlay applied to the agent record during Snapshot.
// The reader is exposed so tests can inject deterministic alias maps.
type AliasReaderFile struct {
	Path string
}

// DefaultAliasPath honours $XDG_CONFIG_HOME, falling back to $HOME/.config.
func DefaultAliasPath() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "orch-aliases")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "orch-aliases")
}

// NewAliasReader constructs an AliasReaderFile. Empty path resolves via
// DefaultAliasPath.
func NewAliasReader(path string) *AliasReaderFile {
	if path == "" {
		path = DefaultAliasPath()
	}
	return &AliasReaderFile{Path: path}
}

// Aliases parses the alias file. Returns an empty map (not an error) when
// the file is absent. Malformed lines are skipped with a stderr-style
// error wrap returned alongside whatever did parse — callers usually
// ignore the error and use the partial map.
func (a *AliasReaderFile) Aliases(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	f, err := os.Open(a.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return out, fmt.Errorf("open %s: %w", a.Path, err)
	}
	defer f.Close()

	var malformed []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.Index(line, "=")
		if i <= 0 {
			malformed = append(malformed, line)
			continue
		}
		name := strings.TrimSpace(line[:i])
		pane := strings.TrimSpace(line[i+1:])
		if name == "" || pane == "" {
			malformed = append(malformed, line)
			continue
		}
		out[name] = pane
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return out, fmt.Errorf("read %s: %w", a.Path, scanErr)
	}
	if len(malformed) > 0 {
		return out, fmt.Errorf("%s: %d malformed line(s); first: %q",
			a.Path, len(malformed), malformed[0])
	}
	return out, nil
}
