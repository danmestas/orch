// Package ccsession discovers the operator's currently-active Claude
// Code session directory. The CC desktop client writes transcript
// JSONLs under ~/.claude/projects/<cwd-enc>/<session-uuid>/*.jsonl;
// the most-recently-modified jsonl tells us which session is in focus.
//
// Detection is best-effort: no CC running, or no projects/ tree at all,
// returns ErrNoSession and the daemon idles. The daemon polls every
// 30s so opening a new CC window is picked up without a restart.
package ccsession

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ErrNoSession is returned when no CC session can be found. Treated
// as non-fatal by the daemon — it logs once and keeps polling.
var ErrNoSession = errors.New("no active Claude Code session")

// Session describes a discovered CC session directory. The daemon
// uses Dir/SubagentsDir as the write target, CWD as the value to plug
// into JSONL line bodies, and UUID for the sessionId field.
type Session struct {
	// Dir is ~/.claude/projects/<cwd-enc>/<session-uuid>/.
	Dir string
	// SubagentsDir is Dir/subagents/.
	SubagentsDir string
	// UUID is the session-uuid component (parent dir name).
	UUID string
	// CWD is the operator's working directory the session belongs to,
	// reconstructed from the cwd-enc parent dir name. Best-effort —
	// if the encoded name is ambiguous we set CWD to the empty string
	// and the caller falls back to the daemon's own cwd.
	CWD string
	// LastModified is the mtime of the most recent JSONL in Dir.
	LastModified time.Time
}

// uuidRe matches the bare RFC-4122 v4 form CC uses for session dir names.
var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// FindMostRecent scans projectsDir (typically ~/.claude/projects) and
// returns the session whose newest jsonl was touched most recently. If
// the dir doesn't exist or contains no sessions with jsonl files it
// returns ErrNoSession.
//
// We bound the scan: top-level project dirs and one level of session
// dirs; jsonl mtime is read per-session via filepath.Walk on just that
// subtree. On an operator's laptop this typically touches a few dozen
// files — fine to do every 30s.
func FindMostRecent(projectsDir string) (Session, error) {
	if projectsDir == "" {
		return Session{}, ErrNoSession
	}
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Session{}, ErrNoSession
		}
		return Session{}, fmt.Errorf("ccsession: read %s: %w", projectsDir, err)
	}

	var best Session
	found := false
	for _, projEntry := range entries {
		if !projEntry.IsDir() {
			continue
		}
		projPath := filepath.Join(projectsDir, projEntry.Name())
		sessions, err := os.ReadDir(projPath)
		if err != nil {
			continue
		}
		for _, sessEntry := range sessions {
			if !sessEntry.IsDir() {
				continue
			}
			if !uuidRe.MatchString(sessEntry.Name()) {
				continue
			}
			sessDir := filepath.Join(projPath, sessEntry.Name())
			latest := newestJSONLMtime(sessDir)
			if latest.IsZero() {
				continue
			}
			if !found || latest.After(best.LastModified) {
				best = Session{
					Dir:          sessDir,
					SubagentsDir: filepath.Join(sessDir, "subagents"),
					UUID:         sessEntry.Name(),
					CWD:          decodeCWD(projEntry.Name()),
					LastModified: latest,
				}
				found = true
			}
		}
	}
	if !found {
		return Session{}, ErrNoSession
	}
	return best, nil
}

// newestJSONLMtime returns the most recent mtime among *.jsonl files
// directly inside dir (we don't descend into subagents/ to avoid
// feedback loops with our own writes). Zero time means "no jsonls".
func newestJSONLMtime(dir string) time.Time {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return time.Time{}
	}
	var newest time.Time
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}

// decodeCWD reverses CC's cwd-encoding: every "/" and "_" in the
// original path becomes "-". The encoding is lossy (a "-" in the
// original path is indistinguishable from a "/") so this is
// best-effort. We return the leading-slashed form: "-Users-op-foo" →
// "/Users/op/foo". An empty / non-leading-dash input returns "".
func decodeCWD(name string) string {
	if name == "" || name[0] != '-' {
		return ""
	}
	return strings.ReplaceAll(name, "-", "/")
}

// DefaultProjectsDir returns ~/.claude/projects, the standard CC
// location. The daemon resolves this once at startup; the caller can
// override via ORCH_BRIDGE_CC_PROJECTS_DIR.
func DefaultProjectsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}
