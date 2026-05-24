package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// OperatorReaderFile is the OperatorReader backed by
// ~/.cache/orch-operator.json (or the path passed at construction).
//
// File shape (best-effort — only PaneID is required):
//
//	{
//	  "pane_id":  "%17",
//	  "claimed_at": "2026-05-17T12:00:00Z",
//	  "transcript": "/Users/.../session.jsonl"
//	}
//
// File absent is NOT an error: many operator sessions never write this
// marker because the shim's metadata.role="operator" already covers the
// case. The marker exists for legacy / pre-shim sessions.
//
// Per ADR-0003 the operator role is a field on the agent record (set via
// metadata.role); this file is a legacy overlay applied during Snapshot
// to preserve backwards compatibility. The reader is exposed so tests
// can inject deterministic operator-pane values.
type OperatorReaderFile struct {
	Path string
}

// DefaultOperatorPath honours $XDG_CACHE_HOME, falling back to
// $HOME/.cache.
func DefaultOperatorPath() string {
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(v, "orch-operator.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "orch-operator.json")
}

// NewOperatorReader constructs an OperatorReaderFile. Empty path resolves
// via DefaultOperatorPath.
func NewOperatorReader(path string) *OperatorReaderFile {
	if path == "" {
		path = DefaultOperatorPath()
	}
	return &OperatorReaderFile{Path: path}
}

// OperatorPane returns the operator's pane id, or "" when no marker is
// present. Read errors that are NOT "file missing" surface as actual
// errors so a corrupt cache file is visible.
func (o *OperatorReaderFile) OperatorPane(ctx context.Context) (string, error) {
	b, err := os.ReadFile(o.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", o.Path, err)
	}
	var payload struct {
		PaneID string `json:"pane_id"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return "", fmt.Errorf("parse %s: %w", o.Path, err)
	}
	return payload.PaneID, nil
}
