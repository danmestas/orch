package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// OperatorFile is the OperatorReader backed by ~/.cache/orch-operator.json
// (or the path passed at construction).
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
type OperatorFile struct {
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

// NewOperatorFile constructs an OperatorFile reader. Empty path resolves
// via DefaultOperatorPath.
func NewOperatorFile(path string) *OperatorFile {
	if path == "" {
		path = DefaultOperatorPath()
	}
	return &OperatorFile{Path: path}
}

// OperatorPane returns the operator's pane id, or "" when no marker is
// present. Read errors that are NOT "file missing" surface as actual
// errors so a corrupt cache file is visible.
func (o *OperatorFile) OperatorPane(ctx context.Context) (string, error) {
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
