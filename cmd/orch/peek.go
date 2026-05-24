package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/danmestas/orch/internal/registry"
)

// runPeek is `orch peek`. Replaces bin/orch-peek.
//
//	orch peek                  # all live workers from the operator registry
//	orch peek %114 %115        # specific panes only
//	orch peek --json           # machine-readable output
//	orch peek --since 5m       # only workers that moved in the last duration
//	orch peek --all            # include dead-pane discovery entries
//
// "Live" = the pane id from a registry snapshot appears in tmux's
// `list-panes -a` output. Dead-pane entries are stale shim records and
// are filtered out unless --all.
func runPeek(args []string) error {
	fs := flag.NewFlagSet("peek", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		jsonOut = fs.Bool("json", false, "emit JSON array instead of one-line rows")
		all     = fs.Bool("all", false, "include dead-pane entries (default: skip)")
		sinceS  = fs.String("since", "", "only workers that moved in the last duration (e.g. 5m, 1h)")
		natsURL = fs.String("nats", "", "NATS URL override")
	)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "orch peek:", err)
		return &exitError{code: 1}
	}

	var sinceDur time.Duration
	if *sinceS != "" {
		d, err := parseSince(*sinceS)
		if err != nil {
			fmt.Fprintln(os.Stderr, "orch peek:", err)
			return &exitError{code: 1}
		}
		sinceDur = d
	}

	panesArg := fs.Args()
	for _, p := range panesArg {
		if !strings.HasPrefix(p, "%") {
			fmt.Fprintf(os.Stderr, "orch peek: invalid pane id: %s\n", p)
			return &exitError{code: 1}
		}
	}

	nc, err := connectNATS(*natsURL, "orch-peek")
	if err != nil {
		return err
	}
	if nc != nil {
		defer nc.Close()
	}

	workers, err := snapshotOnce(context.Background(), nc)
	if err != nil {
		return fmt.Errorf("registry snapshot: %w", err)
	}

	livePanes, _ := listLivePanes()
	isLive := func(pane string) bool {
		_, ok := livePanes[pane]
		return ok
	}

	// Resolve target list.
	var targets []registry.Worker
	if len(panesArg) > 0 {
		paneSet := map[string]bool{}
		for _, p := range panesArg {
			paneSet[p] = true
		}
		for _, w := range workers {
			if paneSet[w.PaneID] {
				targets = append(targets, w)
				delete(paneSet, w.PaneID)
			}
		}
		for p := range paneSet {
			fmt.Fprintf(os.Stderr, "orch peek: %s not in the orch registry — skipping\n", p)
		}
	} else {
		for _, w := range workers {
			if w.Role == "operator" {
				// Operator surfaces as a separate top row below.
				continue
			}
			if !*all && !isLive(w.PaneID) {
				continue
			}
			targets = append(targets, w)
		}
	}

	rows := make([]peekRow, 0, len(targets)+1)

	// Prepend operator row when no specific panes were requested.
	if len(panesArg) == 0 {
		if opRow, ok := operatorRow(workers, isLive); ok {
			rows = append(rows, opRow)
		}
	}

	for _, w := range targets {
		rows = append(rows, probeWorker(w, isLive))
	}

	// --since filter (booting/dead rows have null AgeSeconds and are dropped).
	if sinceDur > 0 {
		var filtered []peekRow
		for _, r := range rows {
			if r.AgeSeconds != nil && *r.AgeSeconds <= int64(sinceDur.Seconds()) {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if len(rows) == 0 {
			fmt.Println("[]")
			return nil
		}
		return enc.Encode(rows)
	}

	for _, r := range rows {
		renderPeekRow(r)
	}
	return nil
}

// peekRow is the wire shape for --json output.
type peekRow struct {
	PaneID     string `json:"pane_id"`
	Agent      string `json:"agent"`
	Role       string `json:"role"`
	Bucket     string `json:"bucket"`
	AgeSeconds *int64 `json:"age_seconds"`
	Events     *int64 `json:"events"`
	Said       string `json:"said"`
	Tool       string `json:"tool"`
}

func probeWorker(w registry.Worker, isLive func(string) bool) peekRow {
	row := peekRow{
		PaneID: w.PaneID,
		Agent:  firstNonEmpty(w.Agent, "?"),
		Role:   firstNonEmpty(w.Role, "worker"),
		Tool:   "-",
	}
	if !isLive(w.PaneID) {
		row.Bucket = "dead"
		return row
	}
	// Worker is live but the transcript JSONL may not have appeared
	// yet; treat that as booting.
	cwd := paneCwd(w.PaneID)
	if cwd == "" {
		row.Bucket = "booting"
		return row
	}
	jsonl, mtime := latestTranscriptForCWD(cwd)
	if jsonl == "" {
		row.Bucket = "booting"
		return row
	}
	now := time.Now()
	age := now.Sub(mtime).Seconds()
	if age < 0 {
		age = 0
	}
	ageI := int64(age)
	row.AgeSeconds = &ageI
	row.Bucket = bucketFor(ageI)
	if events, err := countLines(jsonl); err == nil {
		ev := int64(events)
		row.Events = &ev
	}
	if said, tool, err := lastAssistantSnippet(jsonl); err == nil {
		row.Said = said
		if tool != "" {
			row.Tool = tool
		}
	}
	return row
}

func operatorRow(workers []registry.Worker, isLive func(string) bool) (peekRow, bool) {
	for _, w := range workers {
		if w.Role != "operator" {
			continue
		}
		if !isLive(w.PaneID) {
			continue
		}
		row := probeWorker(w, isLive)
		row.Agent = "operator"
		row.Role = "operator"
		return row, true
	}
	return peekRow{}, false
}

// renderPeekRow writes one human-readable line to stdout. The format
// mirrors bin/orch-peek's row layout closely enough that operators
// recognise it; we drop the ANSI coloring (terminals that want color can
// pipe through bat or jq).
func renderPeekRow(r peekRow) {
	age := "?"
	if r.AgeSeconds != nil {
		age = humanAge(*r.AgeSeconds)
	}
	events := "?"
	if r.Events != nil {
		events = fmt.Sprintf("%d", *r.Events)
	}
	said := strings.ReplaceAll(strings.ReplaceAll(r.Said, "\n", "  "), "\t", "  ")
	if len(said) > 60 {
		said = said[:57] + "..."
	}
	fmt.Printf("%-5s  %-8s %-10s %-7s (%-4s ago)  %-5s events  said: %q  tool: %s\n",
		r.PaneID, r.Role, r.Agent, r.Bucket, age, events, said, r.Tool)
}

func bucketFor(seconds int64) string {
	switch {
	case seconds < 30:
		return "ACTIVE"
	case seconds < 300:
		return "recent"
	default:
		return "idle"
	}
}

func humanAge(s int64) string {
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm", s/60)
	case s < 86400:
		return fmt.Sprintf("%dh", s/3600)
	default:
		return fmt.Sprintf("%dd", s/86400)
	}
}

// parseSince accepts the bin/orch-peek duration shorthand: 30s, 5m, 2h, 1d.
// Plain integers are seconds.
func parseSince(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	unit := s[len(s)-1]
	num := s[:len(s)-1]
	switch unit {
	case 's':
		return parseDurationN(num, time.Second)
	case 'm':
		return parseDurationN(num, time.Minute)
	case 'h':
		return parseDurationN(num, time.Hour)
	case 'd':
		return parseDurationN(num, 24*time.Hour)
	}
	// Trailing char was a digit — interpret whole string as seconds.
	return parseDurationN(s, time.Second)
}

func parseDurationN(num string, unit time.Duration) (time.Duration, error) {
	var n int64
	if _, err := fmt.Sscanf(num, "%d", &n); err != nil || n < 0 {
		return 0, fmt.Errorf("bad --since value: %q (expected an integer followed by s/m/h/d, or a bare seconds count)", num)
	}
	return time.Duration(n) * unit, nil
}

// listLivePanes returns a set of pane ids from tmux list-panes -a.
func listLivePanes() (map[string]struct{}, error) {
	cmd := exec.Command("tmux", "list-panes", "-a", "-F", "#{pane_id}")
	out, err := cmd.Output()
	if err != nil {
		return map[string]struct{}{}, err
	}
	set := map[string]struct{}{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			set[line] = struct{}{}
		}
	}
	return set, nil
}

// paneCwd reads the pane's current working directory from tmux. Returns
// "" when tmux can't reach the pane (live check should have already filtered).
func paneCwd(pane string) string {
	cmd := exec.Command("tmux", "display-message", "-t", pane, "-p", "#{pane_current_path}")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// projectsDir is the root containing Claude Code's per-cwd transcript
// directories. Overridable via ORCH_PROJECTS_DIR to match bin/orch-peek.
func projectsDir() string {
	if p := os.Getenv("ORCH_PROJECTS_DIR"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

// encodeCWD mirrors Claude Code's encoding: resolve symlinks, then
// replace "/" and "_" with "-".
func encodeCWD(cwd string) string {
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		resolved = cwd
	}
	enc := strings.ReplaceAll(resolved, "/", "-")
	enc = strings.ReplaceAll(enc, "_", "-")
	return enc
}

// latestTranscriptForCWD returns the newest *.jsonl file under
// projectsDir()/<encoded-cwd>/ by mtime.
func latestTranscriptForCWD(cwd string) (string, time.Time) {
	dir := filepath.Join(projectsDir(), encodeCWD(cwd))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", time.Time{}
	}
	var best string
	var bestT time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(bestT) {
			bestT = info.ModTime()
			best = filepath.Join(dir, e.Name())
		}
	}
	return best, bestT
}

// countLines is the cheap event counter — one line of JSONL per event.
func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close() //nolint:errcheck
	buf := make([]byte, 32*1024)
	count := 0
	prevTail := byte(0)
	for {
		n, err := f.Read(buf)
		for i := 0; i < n; i++ {
			if buf[i] == '\n' {
				count++
			}
		}
		if n > 0 {
			prevTail = buf[n-1]
		}
		if err != nil {
			break
		}
	}
	// If the file does not end with newline, the last line still counts.
	if prevTail != '\n' && prevTail != 0 {
		count++
	}
	return count, nil
}

// lastAssistantSnippet scans the JSONL transcript for the latest
// assistant text + tool-use names. Returns the short preview only; the
// caller truncates further for display.
func lastAssistantSnippet(path string) (string, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close() //nolint:errcheck

	// Stream line-by-line — transcripts can be megabytes.
	dec := json.NewDecoder(f)
	dec.UseNumber()
	type contentBlock struct {
		Type string          `json:"type"`
		Text string          `json:"text"`
		Name string          `json:"name"`
		_    json.RawMessage `json:"-"`
	}
	type message struct {
		Content []contentBlock `json:"content"`
	}
	type record struct {
		Type    string  `json:"type"`
		Message message `json:"message"`
	}
	lastText := ""
	lastTool := ""
	for {
		var r record
		if err := dec.Decode(&r); err != nil {
			if err == io.EOF {
				break
			}
			// Skip malformed lines.
			continue
		}
		if r.Type != "assistant" {
			continue
		}
		for _, b := range r.Message.Content {
			if b.Type == "text" && b.Text != "" {
				lastText = b.Text
			} else if b.Type == "tool_use" && b.Name != "" {
				lastTool = b.Name
			}
		}
	}
	return lastText, lastTool, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" && v != "null" {
			return v
		}
	}
	return ""
}
