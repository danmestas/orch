package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// runMigrateAliases scans common shell-config files and the current git
// repo (if any) for references to the retired bash CLIs and prints
// sed-style rewrite suggestions to stdout.
//
// We never auto-write — the operator owns their config. The output is a
// human-readable diff with `sed -i` commands that can be copy-pasted.
//
//	orch migrate-aliases                     # default scan
//	orch migrate-aliases --no-git            # skip the git grep step
//	orch migrate-aliases --extra <path>...   # also scan these files
//
// Rewrites (one-to-one, no semantic translation needed for any):
//
//	orch-tell            → orch tell
//	orch-ask             → orch ask
//	orch-peek            → orch peek
//	orch-spy             → orch spy
//	orch-claim-operator  → (delete; set ORCH_ROLE=operator instead)
//	orch-register        → (delete; auto-registered by orch-agent-shim)
func runMigrateAliases(args []string) error {
	fs := flag.NewFlagSet("migrate-aliases", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		noGit = fs.Bool("no-git", false, "skip the `git grep` scan of the current repo")
		extra multiFlag
	)
	fs.Var(&extra, "extra", "additional file path to scan (may be repeated)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "orch migrate-aliases:", err)
		return &exitError{code: 1}
	}

	files := defaultMigrateAliasesFiles()
	files = append(files, extra...)
	if !*noGit {
		files = append(files, gitGrepHits()...)
	}

	// De-duplicate while preserving order.
	seen := map[string]bool{}
	var uniq []string
	for _, f := range files {
		if seen[f] {
			continue
		}
		seen[f] = true
		uniq = append(uniq, f)
	}

	fmt.Println("# orch migrate-aliases — suggested rewrites for retired bash CLIs")
	fmt.Println("#")
	fmt.Println("# Mapping (one-to-one; the new `orch` Go binary dispatches subcommands):")
	for _, m := range aliasMappings {
		if m.replace == "" {
			fmt.Printf("#   %-22s → (delete the line — superseded; see comment in suggestion)\n", m.from)
		} else {
			fmt.Printf("#   %-22s → %s\n", m.from, m.replace)
		}
	}
	fmt.Println()

	any := false
	sort.Strings(uniq)
	for _, f := range uniq {
		if !fileExists(f) {
			continue
		}
		hits, err := scanFileForAliases(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "orch migrate-aliases: scan %s: %v\n", f, err)
			continue
		}
		if len(hits) == 0 {
			continue
		}
		any = true
		fmt.Printf("## %s — %d match%s\n", f, len(hits), pluralize(len(hits)))
		for _, h := range hits {
			fmt.Printf("  line %d: %s\n", h.line, h.text)
		}
		// Emit one sed command per mapping that actually matched in
		// this file. We use BSD-compatible sed (no -i'' flag-less
		// in-place editing): operators can paste these into bash and
		// edit the `-i` invocation for their platform.
		for _, m := range collectMappingsHit(hits) {
			if m.replace == "" {
				fmt.Printf("  # Remove %s — superseded; %s\n", m.from, m.note)
				fmt.Printf("  sed -i.bak '/\\b%s\\b/d' %s\n", regexp.QuoteMeta(m.from), shellQuote(f))
			} else {
				fmt.Printf("  sed -i.bak 's/\\b%s\\b/%s/g' %s\n", regexp.QuoteMeta(m.from), m.replace, shellQuote(f))
			}
		}
		fmt.Println()
	}

	if !any {
		fmt.Println("# No references found. Nothing to migrate.")
	}
	return nil
}

type aliasMapping struct {
	from    string
	replace string // empty means delete the line
	note    string
}

// aliasMappings is the canonical bin → subcommand table. Listed once so
// the scanner, the suggestion printer, and the human-readable header
// share a source of truth.
var aliasMappings = []aliasMapping{
	{from: "orch-tell", replace: "orch tell"},
	{from: "orch-ask", replace: "orch ask"},
	{from: "orch-peek", replace: "orch peek"},
	{from: "orch-spy", replace: "orch spy"},
	{from: "orch-claim-operator", note: "set ORCH_ROLE=operator in your shell instead so orch-agent-shim publishes role=operator on $SRV.INFO.agents"},
	{from: "orch-register", note: "the shim auto-registers panes; manual registration is not needed"},
}

type aliasHit struct {
	line    int
	text    string
	mapping aliasMapping
}

var aliasFromRE = func() *regexp.Regexp {
	parts := make([]string, 0, len(aliasMappings))
	for _, m := range aliasMappings {
		parts = append(parts, regexp.QuoteMeta(m.from))
	}
	return regexp.MustCompile(`\b(` + strings.Join(parts, "|") + `)\b`)
}()

func scanFileForAliases(path string) ([]aliasHit, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck
	scanner := bufio.NewScanner(f)
	// Some shell-config files can have long heredocs.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var hits []aliasHit
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		text := scanner.Text()
		matches := aliasFromRE.FindAllString(text, -1)
		if len(matches) == 0 {
			continue
		}
		for _, m := range matches {
			mapping, ok := mappingByName(m)
			if !ok {
				continue
			}
			hits = append(hits, aliasHit{
				line:    lineNum,
				text:    text,
				mapping: mapping,
			})
		}
	}
	return hits, scanner.Err()
}

func mappingByName(name string) (aliasMapping, bool) {
	for _, m := range aliasMappings {
		if m.from == name {
			return m, true
		}
	}
	return aliasMapping{}, false
}

func collectMappingsHit(hits []aliasHit) []aliasMapping {
	seen := map[string]bool{}
	var out []aliasMapping
	for _, h := range hits {
		if seen[h.mapping.from] {
			continue
		}
		seen[h.mapping.from] = true
		out = append(out, h.mapping)
	}
	return out
}

// defaultMigrateAliasesFiles returns the canonical list of shell-config
// paths to scan. We err on the side of completeness — non-existent paths
// are skipped silently downstream.
func defaultMigrateAliasesFiles() []string {
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(home, ".bash_aliases"),
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".zprofile"),
		filepath.Join(home, ".profile"),
		filepath.Join(home, ".config", "fish", "config.fish"),
		filepath.Join(home, ".config", "orch-aliases"),
	}
}

// gitGrepHits returns a list of file paths in the current git repo (cwd)
// that contain at least one reference to a retired CLI. Returns empty
// (silently) when not inside a git repo or when git is not installed.
func gitGrepHits() []string {
	if _, err := exec.LookPath("git"); err != nil {
		return nil
	}
	pattern := `orch-(tell|peek|spy|ask|claim-operator|register)\b`
	cmd := exec.Command("git", "grep", "-lE", pattern)
	out, err := cmd.Output()
	if err != nil {
		// Not in a repo, or no matches — both are "nothing to add".
		return nil
	}
	wd, _ := os.Getwd()
	var paths []string
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		paths = append(paths, filepath.Join(wd, string(line)))
	}
	return paths
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func pluralize(n int) string {
	if n == 1 {
		return ""
	}
	return "es"
}

func shellQuote(s string) string {
	// Conservative single-quote wrap with embedded-apostrophe escape.
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// multiFlag implements flag.Value for repeatable string flags.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(s string) error { *m = append(*m, s); return nil }
