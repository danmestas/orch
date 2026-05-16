// orch-cc-subagent-bridge mirrors Synadia Agent Protocol §6 chunks
// from every orch-spawned pane into synthetic Claude Code subagent
// JSONLs, so each pane (claude/codex/pi/gemini/echo) appears in CC's
// subagent panel.
//
// Usage:
//
//	orch-cc-subagent-bridge [--nats URL] [--projects DIR] [--keep-files]
//
// Resolution order:
//
//	NATS:           --nats → $NATS_URL → nats://127.0.0.1:4222
//	Projects dir:   --projects → $ORCH_BRIDGE_CC_PROJECTS_DIR → ~/.claude/projects
//	Keep files:     --keep-files / $ORCH_BRIDGE_KEEP_FILES=1 → preserve JSONLs on exit
//
// The bridge is harness-agnostic: it reads the SAP wire, never the
// agent CLIs themselves. New harness types surface automatically as
// long as their orch-agent-shim registers under metadata.harness.
//
// Lifecycle: started detached by `orch up` after the hub is healthy,
// sent SIGTERM by `orch down`. No CC session yet? Log once, idle, and
// keep polling — opening a new CC window picks the bridge up without
// a restart.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/danmestas/orch/extensions/claudecode-subagent-panel/internal/ccsession"
	"github.com/danmestas/orch/extensions/claudecode-subagent-panel/internal/translator"
	"github.com/danmestas/orch/extensions/claudecode-subagent-panel/internal/writer"
)

const (
	defaultNATSURL   = "nats://127.0.0.1:4222"
	defaultCCVersion = "2.1.143"

	// sessionRescanInterval is how often we re-scan for the active CC
	// session. 30s matches the spec note "operator opens a new CC
	// window" — anything tighter is wasted IO on a stable workstation.
	sessionRescanInterval = 30 * time.Second

	// discoveryInterval is how often we re-poll $SRV.INFO.agents to
	// catch newly-spawned panes. Five seconds is the operator-perceived
	// latency budget — a pane spawned now should appear in the CC
	// panel within a heartbeat-ish window.
	discoveryInterval = 5 * time.Second

	// discoveryTimeout bounds a single discovery request/reply. The
	// outer loop tolerates failure — we just retry on the next tick.
	discoveryTimeout = 2 * time.Second
)

func main() {
	if err := run(); err != nil {
		log.Printf("orch-cc-subagent-bridge: %v", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		natsURL     = flag.String("nats", "", "NATS URL (default $NATS_URL or "+defaultNATSURL+")")
		projectsDir = flag.String("projects", "", "CC projects dir (default $ORCH_BRIDGE_CC_PROJECTS_DIR or ~/.claude/projects)")
		keepFiles   = flag.Bool("keep-files", false, "preserve synthetic JSONLs on exit (default false; respects $ORCH_BRIDGE_KEEP_FILES=1)")
	)
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.LUTC)

	url := firstNonEmpty(*natsURL, os.Getenv("NATS_URL"), defaultNATSURL)
	pdir := firstNonEmpty(*projectsDir, os.Getenv("ORCH_BRIDGE_CC_PROJECTS_DIR"), ccsession.DefaultProjectsDir())
	keep := *keepFiles || os.Getenv("ORCH_BRIDGE_KEEP_FILES") == "1"

	log.Printf("starting: nats=%s projects=%s keep=%v", url, pdir, keep)

	// Connect to NATS with infinite reconnect — orch hub coming
	// online after the bridge is fine; the bridge waits.
	nc, err := nats.Connect(url,
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.Name("orch-cc-subagent-bridge"),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.Printf("nats disconnected: %v", err)
			}
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			log.Printf("nats reconnected: %s", c.ConnectedUrl())
		}),
	)
	if err != nil {
		return fmt.Errorf("connect nats: %w", err)
	}
	defer nc.Drain() //nolint:errcheck // best-effort drain on shutdown

	w := writer.New("") // target dir set after first session-resolve
	defer w.Close()     //nolint:errcheck

	b := &bridge{
		nc:          nc,
		w:           w,
		projectsDir: pdir,
		agents:      make(map[string]*agentState),
		ccVersion:   defaultCCVersion,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := b.start(ctx); err != nil {
		return err
	}

	<-ctx.Done()
	log.Printf("shutting down")

	// Best-effort cleanup of synthetic files unless operator asked to keep them.
	if !keep {
		removed := w.Sweep()
		log.Printf("swept %d synthetic JSONL(s)", len(removed))
	}
	return nil
}

// bridge is the daemon-level state. One per process.
type bridge struct {
	nc          *nats.Conn
	w           *writer.Writer
	projectsDir string
	ccVersion   string

	mu     sync.Mutex
	agents map[string]*agentState

	// sessUUID/sessCWD are the active CC session's identifiers,
	// snapshotted on each rescan. Empty until first successful
	// detection.
	sessUUID string
	sessCWD  string
	sessGit  string

	// idleLogged is set once we've reported "no CC session yet" so
	// the daemon log doesn't spam the operator.
	idleLogged bool
}

// agentState tracks per-agent metadata + cursor between chunk lines.
type agentState struct {
	meta   translator.AgentMeta
	cursor translator.Cursor
	// replySubjects is the set of NATS reply subjects we've seen for
	// in-flight prompts to this agent. We subscribe to each lazily on
	// first sighting via the agents.> wildcard. (Currently we just
	// rely on the daemon's single agents.> subscription instead.)
	seeded bool
}

func (b *bridge) start(ctx context.Context) error {
	// Initial session detect — non-fatal on miss.
	b.tryRescanSession()

	// Subscribe to every agent reply stream via the agents.prompt.>
	// wildcard. Each chunk's subject is
	// agents.prompt.<token>.<owner>.<pane-enc>; the reply subject is
	// chosen by the caller (nats request). Subscribing to "agents.>"
	// catches both directions — we filter to chunk shapes in the
	// handler.
	if _, err := b.nc.Subscribe("agents.>", b.onAgentMsg); err != nil {
		return fmt.Errorf("subscribe agents.>: %w", err)
	}

	// Discovery loop — periodic $SRV.INFO.agents poll.
	go b.discoveryLoop(ctx)
	// Session rescan loop — operator opens a new CC window.
	go b.sessionRescanLoop(ctx)

	// Kick off an initial discovery synchronously so the first batch
	// of agents seeds without waiting up to 5s.
	b.runDiscovery(ctx)
	return nil
}

func (b *bridge) discoveryLoop(ctx context.Context) {
	t := time.NewTicker(discoveryInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.runDiscovery(ctx)
		}
	}
}

func (b *bridge) sessionRescanLoop(ctx context.Context) {
	t := time.NewTicker(sessionRescanInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.tryRescanSession()
		}
	}
}

// serviceInfo is the subset of $SRV.INFO.agents reply we care about.
// Synadia's micro framework includes more fields; we ignore them.
type serviceInfo struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Version  string            `json:"version"`
	Metadata map[string]string `json:"metadata"`
}

func (b *bridge) runDiscovery(ctx context.Context) {
	// Use a request/reply with no-responders enabled: send to
	// $SRV.INFO.agents, drain replies for discoveryTimeout, parse each
	// as a serviceInfo, upsert state.
	inbox := nats.NewInbox()
	sub, err := b.nc.SubscribeSync(inbox)
	if err != nil {
		log.Printf("discovery: subscribe inbox: %v", err)
		return
	}
	defer sub.Unsubscribe() //nolint:errcheck

	if err := b.nc.PublishRequest("$SRV.INFO.agents", inbox, nil); err != nil {
		log.Printf("discovery: publish: %v", err)
		return
	}

	deadline := time.Now().Add(discoveryTimeout)
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		msg, err := sub.NextMsg(remaining)
		if err != nil {
			// Inactivity timeout — normal exit.
			break
		}
		var info serviceInfo
		if err := json.Unmarshal(msg.Data, &info); err != nil {
			continue
		}
		if info.Name != "agents" {
			continue
		}
		b.upsertAgent(info)
		_ = ctx // reserved for cancellation propagation if we later add network sub-ops
	}
}

func (b *bridge) upsertAgent(info serviceInfo) {
	pane := info.Metadata["pane_id"]
	if pane == "" {
		return
	}
	harness := firstNonEmpty(info.Metadata["harness"], info.Metadata["agent"], "unknown")
	agentID := writer.EncodePane(pane)

	b.mu.Lock()
	st, exists := b.agents[agentID]
	if !exists {
		slug, err := translator.RandomSlug()
		if err != nil {
			slug = "orch-agent"
		}
		// Snapshot session identity under the same lock so a concurrent
		// rescan can't slip in.
		sessUUID := b.sessUUID
		sessCWD := b.sessCWD
		sessGit := b.sessGit
		st = &agentState{
			meta: translator.AgentMeta{
				AgentID:   agentID,
				Harness:   harness,
				PaneID:    pane,
				SessionID: sessUUID,
				CWD:       sessCWD,
				GitBranch: sessGit,
				CCVersion: b.ccVersion,
				Slug:      slug,
			},
		}
		b.agents[agentID] = st
	}
	b.mu.Unlock()

	// Seed the JSONL on first sighting — but only if we have an
	// active CC session (otherwise we'd write to "").
	if !exists && st.meta.SessionID != "" {
		b.seedAgent(st)
	}
}

func (b *bridge) seedAgent(st *agentState) {
	b.mu.Lock()
	if st.seeded {
		b.mu.Unlock()
		return
	}
	st.seeded = true
	meta := st.meta
	b.mu.Unlock()

	line, err := translator.SeedLine(meta, time.Now())
	if err != nil {
		log.Printf("seed %s: %v", meta.AgentID, err)
		return
	}
	if err := b.w.Append(meta.AgentID, line.Bytes); err != nil {
		log.Printf("seed write %s: %v", meta.AgentID, err)
		return
	}
	b.mu.Lock()
	st.cursor = line.Next
	b.mu.Unlock()
	log.Printf("seeded agent %s (pane=%s harness=%s)", meta.AgentID, meta.PaneID, meta.Harness)
}

// onAgentMsg processes one message off agents.>. We accept only
// messages whose subject matches agents.prompt.<token>.<owner>.<pane>
// (chunks flow on reply subjects, but we also see prompts here — we
// ignore them). Then we decode the §6.2 chunk and translate.
//
// Note: chunks land on the *reply* subject of a prompt request, which
// is typically a _INBOX.>* subject. Subscribing to agents.> alone
// misses them. The simpler route is to subscribe to BOTH agents.> AND
// _INBOX.>, but _INBOX is broker-wide. Instead, we subscribe to the
// shim's prompt subject (agents.prompt.>) AND every message whose
// Reply field is set; on first sight of a new (subject, reply) pair we
// open a sync-sub on the reply.
//
// Practical simplification for v1: we subscribe to the chunk *publish*
// path — the shim publishes chunks directly on the reply subject the
// caller chose. The bridge needs to "tap" that traffic. Without a
// stream-tap facility built into nats.go's core API, the right v1
// answer is to subscribe to BOTH agents.> AND the broker-wide _INBOX
// pattern via $JS or via a no-responders-aware wildcard.
//
// Pragmatic choice: tap "agents.>" for now (catches event-like topics
// shims publish on; some adapters use agents.event.> or similar) and
// emit best-effort chunks. Real chunk flow ride-along is a future
// upgrade; the v1 panel at least shows the seed line + every agent
// stays present.
func (b *bridge) onAgentMsg(msg *nats.Msg) {
	if msg == nil || len(msg.Data) == 0 {
		return
	}
	// Subject shape: agents.<verb>.<token>.<owner>.<pane-enc>.
	parts := strings.Split(msg.Subject, ".")
	if len(parts) < 5 {
		return
	}
	verb := parts[1]
	// We only react to verbs that aren't `prompt` (the inbound request);
	// chunks flow on hb/event/status subjects per the shim's own
	// publishing pattern, plus per-pane custom topics.
	if verb == "prompt" || verb == "hb" {
		return
	}
	pane := parts[len(parts)-1]
	// pane segment is already encoded (encodePane in the shim). Use
	// it directly as the agent id.
	agentID := pane
	b.mu.Lock()
	st, ok := b.agents[agentID]
	b.mu.Unlock()
	if !ok {
		// Unknown agent — discovery will catch it on next tick.
		return
	}

	chunk, ok := decodeChunkBody(msg.Data)
	if !ok {
		return
	}
	b.mu.Lock()
	cur := st.cursor
	meta := st.meta
	b.mu.Unlock()

	line, err := translator.AssistantLine(meta, cur, chunk, time.Now())
	if err != nil {
		log.Printf("translate %s: %v", agentID, err)
		return
	}
	if line.Bytes == nil {
		return
	}
	if err := b.w.Append(agentID, line.Bytes); err != nil {
		log.Printf("append %s: %v", agentID, err)
		return
	}
	b.mu.Lock()
	st.cursor = line.Next
	b.mu.Unlock()
}

// decodeChunkBody parses a §6.2 chunk envelope. Falls back to treating
// the payload as raw response text if it's not JSON.
func decodeChunkBody(data []byte) (translator.Chunk, bool) {
	// §6.5 zero-byte terminator.
	if len(data) == 0 {
		return translator.Chunk{Terminator: true}, true
	}
	var env struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err != nil || env.Type == "" {
		// Not a typed chunk envelope — surface as raw response text.
		return translator.Chunk{Type: "response", Text: string(data)}, true
	}
	chunk := translator.Chunk{Type: env.Type}
	// Try string first, then object-with-text.
	var s string
	if err := json.Unmarshal(env.Data, &s); err == nil {
		chunk.Text = s
		return chunk, true
	}
	var obj map[string]any
	if err := json.Unmarshal(env.Data, &obj); err == nil {
		if t, ok := obj["text"].(string); ok {
			chunk.Text = t
		} else {
			// Fallback: stringify the whole object.
			raw, _ := json.Marshal(obj)
			chunk.Text = string(raw)
		}
		return chunk, true
	}
	chunk.Text = string(env.Data)
	return chunk, true
}

// tryRescanSession is called on startup and on each session-rescan
// tick. On a successful resolve, swaps the writer's target dir and
// records the session identifiers. On miss, logs once and stays idle.
func (b *bridge) tryRescanSession() {
	sess, err := ccsession.FindMostRecent(b.projectsDir)
	if err != nil {
		if errors.Is(err, ccsession.ErrNoSession) {
			if !b.idleLogged {
				log.Printf("no active Claude Code session yet — bridge idle (polling every %s)", sessionRescanInterval)
				b.idleLogged = true
			}
			return
		}
		log.Printf("session scan: %v", err)
		return
	}
	b.mu.Lock()
	changed := sess.UUID != b.sessUUID
	if changed {
		b.sessUUID = sess.UUID
		b.sessCWD = sess.CWD
		b.sessGit = resolveGitBranch(sess.CWD)
		// Re-seed every known agent against the new dir on next chunk:
		// reset their seeded flag and re-apply session identity.
		for _, st := range b.agents {
			st.seeded = false
			st.meta.SessionID = b.sessUUID
			st.meta.CWD = b.sessCWD
			st.meta.GitBranch = b.sessGit
			st.cursor = translator.Cursor{}
		}
	}
	b.mu.Unlock()

	if changed {
		if err := b.w.SwapTarget(sess.SubagentsDir); err != nil {
			log.Printf("swap target: %v", err)
			return
		}
		log.Printf("active CC session: %s (dir=%s)", sess.UUID, sess.SubagentsDir)
		b.idleLogged = false
		// Re-seed all known agents under the new session.
		b.mu.Lock()
		states := make([]*agentState, 0, len(b.agents))
		for _, st := range b.agents {
			states = append(states, st)
		}
		b.mu.Unlock()
		for _, st := range states {
			b.seedAgent(st)
		}
	}
}

// resolveGitBranch is a best-effort git branch lookup for the CC
// session's cwd. Empty string on miss (not a repo, no git on PATH, ...).
func resolveGitBranch(cwd string) string {
	if cwd == "" {
		return ""
	}
	if _, err := os.Stat(filepath.Join(cwd, ".git")); err != nil {
		return ""
	}
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
