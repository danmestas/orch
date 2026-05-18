package sources

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ShimLogs scans ~/.cache/orch-shim/<pct-pane>.log for diagnostic context.
// This is a low-priority source: the join doesn't depend on it for
// correctness, but the file's presence + mtime is useful when a worker
// looks dead on the bus and an operator needs to know if the shim ever
// started.
//
// The current registry consumes nothing from shim logs (the proposal
// lists this as "optional, ad-hoc diagnostic fields"). The reader is in
// place so future Worker fields (e.g. LastShimRestart) can flow without
// reshaping the source interface set.
type ShimLogs struct {
	Dir string
}

// DefaultShimLogDir honours $XDG_CACHE_HOME, falling back to
// $HOME/.cache/orch-shim.
func DefaultShimLogDir() string {
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(v, "orch-shim")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "orch-shim")
}

// NewShimLogs constructs a ShimLogs reader. Empty dir resolves via
// DefaultShimLogDir.
func NewShimLogs(dir string) *ShimLogs {
	if dir == "" {
		dir = DefaultShimLogDir()
	}
	return &ShimLogs{Dir: dir}
}

// LogPath returns the absolute path to the per-pane log file, or "" when
// the log does not exist. The file naming convention follows the shim's
// encodePane: "%64" → "pct64.log".
func (l *ShimLogs) LogPath(pane string) string {
	if pane == "" {
		return ""
	}
	name := "pct" + strings.TrimPrefix(pane, "%") + ".log"
	p := filepath.Join(l.Dir, name)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// List returns every pct<N>.log under the directory. Empty list (not
// error) when the directory is absent — operators may not have any
// shim logs yet on a fresh machine.
func (l *ShimLogs) List(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(l.Dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasPrefix(n, "pct") || !strings.HasSuffix(n, ".log") {
			continue
		}
		out = append(out, filepath.Join(l.Dir, n))
	}
	return out, nil
}
