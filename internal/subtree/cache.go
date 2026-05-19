package subtree

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultCacheDir is the on-disk location of applied.yaml records.
// Resolved via $XDG_CACHE_HOME (falling back to $HOME/.cache) so it
// honours the operator's XDG layout.
//
// Proposal 0006 specifies ~/.cache/orch-subtrees/<name>.applied.yaml;
// XDG_CACHE_HOME is the standard way to express that on Linux while
// keeping macOS / dev-machine behaviour identical.
func DefaultCacheDir() string {
	if env := os.Getenv("ORCH_SUBTREE_CACHE_DIR"); env != "" {
		return env
	}
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "orch-subtrees")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback to a relative path so a misconfigured environment
		// still produces working files in the operator's CWD instead
		// of failing.
		return ".orch-subtree-cache"
	}
	return filepath.Join(home, ".cache", "orch-subtrees")
}

// fileCache implements CacheStore on the local filesystem. One yaml
// file per subtree under DefaultCacheDir(). Reads/writes are atomic
// at the filesystem level (write-temp + rename).
type fileCache struct {
	dir string
}

// NewFileCache constructs the production CacheStore.
func NewFileCache(dir string) CacheStore {
	if dir == "" {
		dir = DefaultCacheDir()
	}
	return &fileCache{dir: dir}
}

func (c *fileCache) path(name string) string {
	return filepath.Join(c.dir, name+".applied.yaml")
}

func (c *fileCache) Read(name string) (*AppliedSubtree, error) {
	data, err := os.ReadFile(c.path(name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("subtree %q not found in cache (was it applied?)", name)
		}
		return nil, err
	}
	var a AppliedSubtree
	if err := yaml.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("subtree cache: parse %s: %w", c.path(name), err)
	}
	return &a, nil
}

func (c *fileCache) Write(a *AppliedSubtree) error {
	if a == nil {
		return fmt.Errorf("subtree cache: cannot write nil AppliedSubtree")
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return fmt.Errorf("subtree cache: mkdir %s: %w", c.dir, err)
	}
	data, err := yaml.Marshal(a)
	if err != nil {
		return fmt.Errorf("subtree cache: marshal: %w", err)
	}
	final := c.path(a.Name)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("subtree cache: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("subtree cache: rename %s → %s: %w", tmp, final, err)
	}
	return nil
}

func (c *fileCache) Delete(name string) error {
	err := os.Remove(c.path(name))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (c *fileCache) List() ([]string, error) {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".applied.yaml") {
			continue
		}
		names = append(names, strings.TrimSuffix(n, ".applied.yaml"))
	}
	return names, nil
}
