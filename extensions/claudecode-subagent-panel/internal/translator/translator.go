// Package translator maps Synadia Agent Protocol v0.3 chunks (§6) into
// the JSONL line shape Claude Code expects under
// ~/.claude/projects/<cwd-enc>/<session-uuid>/subagents/agent-<id>.jsonl.
//
// The mapping is intentionally deep — the caller hands us a *nats.Msg
// (or seed-line input) and gets back a single []byte JSONL line plus
// the new "parent uuid" cursor that lets the next call thread to this
// one. No NATS, JSON, or filesystem concerns leak out.
//
// Wire reference: github.com/synadia-io/agents/agent-sdk-docs/core-protocol.md
package translator

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// AgentMeta is everything the translator needs that doesn't come from
// the per-chunk wire. Caller (the daemon) builds it once per agent
// from the §3.2 service metadata returned by $SRV.INFO.agents.
type AgentMeta struct {
	// AgentID is the synthetic id used in the JSONL filename and on
	// every `agentId` field. We use the encoded pane id (e.g.
	// "pct37" — see encoder in writer/) so the operator can correlate
	// the panel entry back to a tmux pane.
	AgentID string

	// Harness is the canonical Synadia §C harness name —
	// "claude-code", "codex", "gemini", "pi", "echo". Lands in
	// `message.model` as "orch-<harness>" so the operator sees which
	// CLI is producing each subagent stream.
	Harness string

	// PaneID is the raw "%37" pane id, used only in the seed-line text.
	PaneID string

	// Session is the CC session uuid the line will live under. The CC
	// renderer key-checks this matches the parent dir.
	SessionID string

	// CWD is the CC session's cwd (NOT the agent's cwd). CC's renderer
	// uses this for git-branch resolution and "open file" affordances;
	// pointing it at the operator's workspace is the right UX.
	CWD string

	// GitBranch is best-effort — caller resolves once at startup; we
	// just echo it.
	GitBranch string

	// CCVersion is read from any existing JSONL in the operator's CC
	// project tree, or hardcoded fallback. CC tolerates it being a
	// reasonable-looking value.
	CCVersion string

	// Slug is a random "<adj>-<verb>-<noun>" style identifier CC
	// attaches to every line in a sidechain. We generate one per
	// agent and reuse it.
	Slug string
}

// Cursor is the parent-uuid chain state. After each emitted line the
// caller stores the returned cursor and threads it in on the next call.
type Cursor struct {
	ParentUUID string // empty string → emits null on the next line
}

// Line is the result of one translate call: the marshalled JSONL bytes
// (already newline-terminated, ready to append) plus the new cursor.
type Line struct {
	Bytes []byte
	Next  Cursor
}

// Wire envelope shapes. Keep these private — encodeLine renders them.
type userLine struct {
	ParentUUID  *string `json:"parentUuid"`
	IsSidechain bool    `json:"isSidechain"`
	AgentID     string  `json:"agentId"`
	Type        string  `json:"type"`
	Message     userMsg `json:"message"`
	UUID        string  `json:"uuid"`
	Timestamp   string  `json:"timestamp"`
	UserType    string  `json:"userType"`
	Entrypoint  string  `json:"entrypoint"`
	CWD         string  `json:"cwd"`
	SessionID   string  `json:"sessionId"`
	Version     string  `json:"version"`
	GitBranch   string  `json:"gitBranch"`
	Slug        string  `json:"slug"`
}

type userMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type assistantLine struct {
	ParentUUID  *string      `json:"parentUuid"`
	IsSidechain bool         `json:"isSidechain"`
	AgentID     string       `json:"agentId"`
	Type        string       `json:"type"`
	Message     assistantMsg `json:"message"`
	UUID        string       `json:"uuid"`
	Timestamp   string       `json:"timestamp"`
	UserType    string       `json:"userType"`
	Entrypoint  string       `json:"entrypoint"`
	CWD         string       `json:"cwd"`
	SessionID   string       `json:"sessionId"`
	Version     string       `json:"version"`
	GitBranch   string       `json:"gitBranch"`
	Slug        string       `json:"slug"`
}

type assistantMsg struct {
	Model      string         `json:"model"`
	ID         string         `json:"id"`
	Type       string         `json:"type"` // always "message"
	Role       string         `json:"role"` // always "assistant"
	Content    []contentBlock `json:"content"`
	StopReason *string        `json:"stop_reason"`
	Usage      usage          `json:"usage"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Chunk is the decoded §6.2 envelope this package consumes. The daemon
// parses the *nats.Msg.Data into one of these (or recognises a §6.5
// zero-byte terminator) before calling Translate*. Keeps the wire
// vocabulary out of the daemon's main loop.
type Chunk struct {
	// Type is the §6.2 type discriminator: "response", "status",
	// "partial", "thinking", "tool_use", "query", "error". Empty
	// string + zero-length body = §6.5 terminator (caller signals via
	// Terminator=true rather than relying on type detection).
	Type string

	// Text is the human-readable content extracted from the chunk's
	// Data — already stripped of JSON envelope. For object-typed
	// chunks (e.g. {"text": "..."}) the caller passes the text field;
	// for string-typed chunks, the string itself.
	Text string

	// ErrCode / ErrMessage are populated for error-headered messages
	// (§9). Both empty for normal chunks.
	ErrCode    string
	ErrMessage string

	// Terminator signals the §6.5 zero-byte terminator. When true,
	// other fields are ignored.
	Terminator bool
}

// SeedLine emits the very first `user`-type line for an agent's
// JSONL — a synthetic prompt explaining what the subagent stream is.
// Called once when the bridge first discovers an agent on
// $SRV.INFO.agents.
func SeedLine(meta AgentMeta, now time.Time) (Line, error) {
	text := fmt.Sprintf(
		"orch agent %s ready (%s) — output below is mirrored from the SAP bus",
		meta.PaneID, meta.Harness,
	)
	u, err := newUUID()
	if err != nil {
		return Line{}, err
	}
	line := userLine{
		ParentUUID:  nil,
		IsSidechain: true,
		AgentID:     meta.AgentID,
		Type:        "user",
		Message:     userMsg{Role: "user", Content: text},
		UUID:        u,
		Timestamp:   now.UTC().Format("2006-01-02T15:04:05.000Z"),
		UserType:    "external",
		Entrypoint:  "cli",
		CWD:         meta.CWD,
		SessionID:   meta.SessionID,
		Version:     meta.CCVersion,
		GitBranch:   meta.GitBranch,
		Slug:        meta.Slug,
	}
	b, err := json.Marshal(line)
	if err != nil {
		return Line{}, err
	}
	b = append(b, '\n')
	return Line{Bytes: b, Next: Cursor{ParentUUID: u}}, nil
}

// AssistantLine emits a single assistant-type line for the given chunk.
// Errors and terminators are rendered as visible assistant text so the
// CC panel surfaces them without a custom renderer. Caller is
// responsible for honoring StopReason at turn boundaries; we always
// emit one line per chunk and let CC's renderer reflow.
//
// Returns Line.Bytes == nil (no error) if the chunk has no surfaceable
// content (e.g. a bare `ack` status). Caller treats that as a no-op.
func AssistantLine(meta AgentMeta, cur Cursor, chunk Chunk, now time.Time) (Line, error) {
	text, stop := renderText(chunk)
	if text == "" && !stop {
		return Line{Next: cur}, nil
	}

	u, err := newUUID()
	if err != nil {
		return Line{}, err
	}
	msgID, err := newMsgID()
	if err != nil {
		return Line{}, err
	}

	content := []contentBlock{}
	if text != "" {
		content = append(content, contentBlock{Type: "text", Text: text})
	}

	var stopReason *string
	if stop {
		s := "end_turn"
		stopReason = &s
	}

	var parent *string
	if cur.ParentUUID != "" {
		p := cur.ParentUUID
		parent = &p
	}

	line := assistantLine{
		ParentUUID:  parent,
		IsSidechain: true,
		AgentID:     meta.AgentID,
		Type:        "assistant",
		Message: assistantMsg{
			Model:      "orch-" + strings.TrimSpace(meta.Harness),
			ID:         msgID,
			Type:       "message",
			Role:       "assistant",
			Content:    content,
			StopReason: stopReason,
			Usage:      usage{InputTokens: 0, OutputTokens: 0},
		},
		UUID:       u,
		Timestamp:  now.UTC().Format("2006-01-02T15:04:05.000Z"),
		UserType:   "external",
		Entrypoint: "cli",
		CWD:        meta.CWD,
		SessionID:  meta.SessionID,
		Version:    meta.CCVersion,
		GitBranch:  meta.GitBranch,
		Slug:       meta.Slug,
	}
	b, err := json.Marshal(line)
	if err != nil {
		return Line{}, err
	}
	b = append(b, '\n')
	return Line{Bytes: b, Next: Cursor{ParentUUID: u}}, nil
}

// renderText picks the surfaceable text for an assistant line and
// reports whether this chunk closes a turn (end_turn stop_reason).
//
//   - response/partial/thinking/tool_use → echo chunk.Text
//   - status → "[status: <text>]" (ack chunks render as nothing —
//     caller's no-op path handles them by returning empty)
//   - error → "[error: <code> <msg>]"
//   - terminator → "" with stop=true
//   - query → "[query: <text>]" (caller flags this as a known limit)
func renderText(c Chunk) (text string, stop bool) {
	if c.Terminator {
		return "", true
	}
	switch c.Type {
	case "response", "partial", "thinking", "tool_use":
		return c.Text, false
	case "status":
		t := strings.TrimSpace(c.Text)
		if t == "" || t == "ack" {
			return "", false
		}
		return "[status: " + t + "]", false
	case "query":
		return "[query: " + c.Text + "]", false
	case "error":
		code := c.ErrCode
		if code == "" {
			code = "ERR"
		}
		msg := c.ErrMessage
		if msg == "" {
			msg = c.Text
		}
		return "[error: " + code + " " + msg + "]", true
	default:
		// Unknown chunk types are surfaced verbatim so debug
		// information isn't silently swallowed. §6.6 of the spec
		// permits silent ignore; we prefer visible breadcrumbs.
		if c.Text == "" {
			return "", false
		}
		return "[" + c.Type + ": " + c.Text + "]", false
	}
}

// newUUID returns an RFC-4122 v4 string. Reads 16 bytes from crypto/rand
// — the daemon will only spam this a handful of times per turn so the
// syscall overhead is irrelevant.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// newMsgID returns a "msg_<hex>"-shaped id matching the shape the real
// Anthropic API hands back — CC's parser is lenient but we match the
// shape so debugging by-eye works.
func newMsgID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "msg_" + hex.EncodeToString(b[:]), nil
}

// RandomSlug returns a "<adj>-<v-ing>-<noun>" identifier in the shape
// CC's renderer uses. Pulls from small fixed lists to keep the daemon
// binary tiny; collisions are fine — one slug per JSONL file.
func RandomSlug() (string, error) {
	adjectives := []string{
		"snazzy", "breezy", "frosty", "cozy", "spry", "balmy", "chipper",
		"plucky", "deft", "lucid", "tidy", "nimble", "wry", "stoic",
	}
	verbs := []string{
		"percolating", "lurking", "humming", "whirring", "drifting",
		"bobbing", "ambling", "darting", "soaring", "weaving", "purring",
	}
	nouns := []string{
		"breeze", "ember", "thicket", "pebble", "lantern", "harbor",
		"meadow", "ripple", "cipher", "anvil", "compass", "lattice",
	}
	pick := func(words []string) (string, error) {
		var idx [1]byte
		if _, err := rand.Read(idx[:]); err != nil {
			return "", err
		}
		return words[int(idx[0])%len(words)], nil
	}
	a, err := pick(adjectives)
	if err != nil {
		return "", err
	}
	v, err := pick(verbs)
	if err != nil {
		return "", err
	}
	n, err := pick(nouns)
	if err != nil {
		return "", err
	}
	return a + "-" + v + "-" + n, nil
}
