// Package writer owns the synthetic JSONL files under
// ~/.claude/projects/<cwd-enc>/<session-uuid>/subagents/. It is the
// single writer in the daemon, so we don't need a lock per file —
// but we still O_APPEND + fsync so a hard crash mid-line doesn't
// leave a torn JSON envelope CC's reader would choke on.
//
// Caller passes (agentID, jsonline []byte); the writer handles target
// directory creation, file opening (cached in a small map), append, and
// fsync. A SwapTarget call rebinds every cached file handle to a new
// subagents/ dir (used when the daemon re-detects the active CC
// session).
package writer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Writer manages O_APPEND handles to per-agent JSONLs. Not safe for
// concurrent use — the daemon serializes all writes through a single
// goroutine.
type Writer struct {
	// targetDir is the active CC session's subagents/ directory.
	// Updated via SwapTarget when the daemon re-detects.
	targetDir string

	// handles caches *os.File per agentID. On SwapTarget we close and
	// drop everything so the next write reopens against the new dir.
	handles map[string]*os.File

	// created tracks every absolute path we've written to, in
	// insertion order, so Sweep() can remove our own residue without
	// touching files we don't own.
	created []string

	// mu guards concurrent Sweep / Close vs Append (the daemon won't
	// race them, but defensive locking here is cheap and lets the
	// daemon's shutdown path call Close from a signal handler).
	mu sync.Mutex
}

// New constructs a writer bound to the given subagents/ dir. The
// directory is created lazily on first append.
func New(targetDir string) *Writer {
	return &Writer{
		targetDir: targetDir,
		handles:   make(map[string]*os.File),
	}
}

// TargetDir returns the current subagents/ directory.
func (w *Writer) TargetDir() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.targetDir
}

// SwapTarget rebinds the writer to a new subagents/ directory. All
// cached file handles are closed; the next Append reopens against the
// new path. Existing synthetic files in the old directory are NOT
// removed automatically — call Sweep before SwapTarget if the operator
// wants the old session's panel cleared.
func (w *Writer) SwapTarget(dir string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for id, f := range w.handles {
		_ = f.Close()
		delete(w.handles, id)
	}
	w.targetDir = dir
	return nil
}

// Append writes one already-newline-terminated JSONL line to the
// agent's file. Creates the subagents/ dir on first use; reopens the
// file lazily.
func (w *Writer) Append(agentID string, line []byte) error {
	if agentID == "" {
		return fmt.Errorf("writer: agentID empty")
	}
	if len(line) == 0 {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.targetDir == "" {
		return fmt.Errorf("writer: no target dir set")
	}
	if err := os.MkdirAll(w.targetDir, 0o755); err != nil {
		return fmt.Errorf("writer: mkdir %s: %w", w.targetDir, err)
	}
	f, ok := w.handles[agentID]
	if !ok {
		path := filepath.Join(w.targetDir, "agent-"+agentID+".jsonl")
		var err error
		f, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("writer: open %s: %w", path, err)
		}
		w.handles[agentID] = f
		w.created = append(w.created, path)
	}
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("writer: append %s: %w", agentID, err)
	}
	// fsync the file so torn writes don't survive a crash mid-line.
	// CC's reader treats partial JSONL lines as fatal.
	return f.Sync()
}

// Close releases all cached handles. Safe to call multiple times.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	var firstErr error
	for id, f := range w.handles {
		if err := f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(w.handles, id)
	}
	return firstErr
}

// Sweep deletes every file the writer has created in this process.
// Called by the daemon's SIGTERM handler when keep-files mode is off.
// Returns the list of paths removed (best-effort — errors logged but
// don't abort the sweep).
func (w *Writer) Sweep() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.created))
	for _, p := range w.created {
		if err := os.Remove(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// EncodePane converts a raw tmux pane id like "%37" or "%pp@7" into a
// filesystem-safe token suitable for inclusion in agent-<id>.jsonl. We
// strip the leading "%" and replace the remaining non-alphanumeric
// characters with "-", giving e.g. "%37" → "pct37". The "pct" prefix
// is fixed so we can distinguish synthetic files from real ones at
// sweep time.
func EncodePane(pane string) string {
	s := strings.TrimPrefix(pane, "%")
	var b strings.Builder
	b.WriteString("pct")
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// IsSyntheticPath reports whether a given subagents/agent-*.jsonl path
// looks like one this bridge produces. Used by orch-down's belt-and-
// suspenders sweep when the daemon was killed before it could clean
// up.
func IsSyntheticPath(path string) bool {
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "agent-pct") {
		return false
	}
	return strings.HasSuffix(base, ".jsonl")
}
