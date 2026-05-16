# Proposal: Optional `metadata.outfit` and `metadata.outfit_hash` fields (§3.2)

**Target:** `synadia-ai/synadia-agent-sdk-docs` — amendment to §3.2 (Required service metadata)  
**Compatibility:** Backward-compatible. Existing registrations without these fields are unaffected.  
**Status:** Draft for upstream review

---

## Motivation

An `agent` identifier (e.g. `claude-code`) describes the harness runtime.
It does not describe *how that harness is configured* — the system prompt,
tool allowlist, model selection, MCP server list, and behavioral constraints
that distinguish an "engineer" pane from a "reviewer" pane or a "planner" pane.

In large fleets, operators need to answer questions like:

- "Show me all engineer-outfit agents currently registered."
- "Is this pane running the focused variant or the wait-watch variant?"
- "Did this task run against the same outfit config as the last run?"

Without a protocol field, callers must embed this information in session
labels (fragile) or maintain an out-of-band registry (expensive).

The orch multi-executor proposal (`docs/multi-executor-workers.md`) introduced
content-addressed outfit bundles — `OUTFIT_BUNDLE_REF=<hash>` — precisely
because knowing *which configuration produced a result* is a first-class
operational concern. Making outfit identity visible in service metadata
extends this to any compliant fleet tool.

---

## Proposed change to §3.2

### BEFORE

| Key                | Type   | Required             | Description |
|--------------------|--------|----------------------|-------------|
| `agent`            | string | Yes                  | Canonical harness identifier. |
| `owner`            | string | Yes                  | Operator / account. |
| `session`          | string | When session-aware   | Harness-specific session label. |
| `protocol_version` | string | Yes                  | Protocol version implemented. |

> Additional metadata keys MAY be included and MUST be preserved by tools that relay service info.

### AFTER

| Key                | Type   | Required             | Description |
|--------------------|--------|----------------------|-------------|
| `agent`            | string | Yes                  | Canonical harness identifier. |
| `owner`            | string | Yes                  | Operator / account. |
| `session`          | string | When session-aware   | Harness-specific session label. |
| `protocol_version` | string | Yes                  | Protocol version implemented. |
| `outfit`           | string | No                   | Configuration bundle name for this agent instance. See §3.2.2 for format conventions. |
| `outfit_hash`      | string | No                   | Stable content-addressed hash of the outfit bundle. Enables version-exact fleet queries. |

> Additional metadata keys MAY be included and MUST be preserved by tools that relay service info.

### New §3.2.2 — Outfit field conventions

**`outfit`** identifies the named configuration bundle applied to this agent
instance. It is a free-form string; the following conventions are RECOMMENDED:

- **Lowercase, hyphen-separated words** for the base name: `engineer`,
  `reviewer`, `planner`, `doc-writer`.
- **Colon-separated suffix** for variants of the same base outfit:
  `engineer:focused`, `engineer:wait-watch`, `planner:deep`, `planner:fast`.
  The part before the colon is the base outfit; the part after is the variant.
  A bare name with no colon implies the default variant.
- **No version numbers in the name.** Version is carried by `outfit_hash`.

Valid examples:

```
engineer
engineer:focused
engineer:wait-watch
planner:deep
doc-writer
reviewer:strict
```

**`outfit_hash`** is a stable identifier for an exact outfit configuration.
It SHOULD be the lowercase hex SHA-256 digest of the canonical outfit bundle
(64 hex characters). When present, it MUST remain stable for identical
configurations: two agents with the same `outfit_hash` MUST have received
identical system prompts, tool allowlists, and model configuration.

SHA-256 is specified rather than left open because the field is used for
hash-equality filters during discovery (§4): a fleet operator querying for
"all panes running outfit version X" filters on `outfit_hash` equality.
Mixed-algorithm fleets (some agents publishing SHA-256, others publishing
e.g. BLAKE3 or SHA-1) would produce silent false negatives — two agents with
identical configuration but different hash algorithms would never match.
Pinning to SHA-256 eliminates this risk.

Implementations SHOULD set `outfit_hash` whenever `outfit` is set, but MAY
omit it when a stable hash is unavailable (e.g. dynamically composed
outfits). Implementations that cannot use SHA-256 SHOULD omit `outfit_hash`
rather than publish a non-SHA-256 value.

**Relationship between `agent` and `outfit`:**  
`agent` is the harness runtime (what executes); `outfit` is the configuration
(what it does). Two registrations with `agent: "claude-code"` and different
`outfit` values are the same harness running different configurations.

---

## Wire example (Appendix B.12 style)

### B.12-o Service info response with `outfit` and `outfit_hash`

Returned by `$SRV.INFO.agents` for a focused-engineer pane:

```json
{
  "name": "agents",
  "id": "N3PQ8RTXZ2YCWKJM15VB6A",
  "version": "0.3.0",
  "description": "Claude Code — eng-2026-05-16-a",
  "metadata": {
    "agent": "claude-code",
    "owner": "dmestas",
    "session": "eng-2026-05-16-a",
    "protocol_version": "0.3",
    "outfit": "engineer:focused",
    "outfit_hash": "a3f8c1e92b74d056f3a91cc8e4b205fd6e1c47b89a2d0fe34c2b8d76a91e5c08"
  },
  "endpoints": [
    {
      "name": "prompt",
      "subject": "agents.prompt.claude-code.dmestas.eng-2026-05-16-a",
      "queue_group": "agents",
      "metadata": {
        "max_payload": "1MB",
        "attachments_ok": true
      }
    },
    {
      "name": "status",
      "subject": "agents.status.claude-code.dmestas.eng-2026-05-16-a",
      "queue_group": "agents"
    }
  ]
}
```

A fleet operator querying for all `engineer:focused` panes would filter on
`metadata.outfit == "engineer:focused"`. A query for a specific config version
would filter on `metadata.outfit_hash == "a3f8c1e92b74d056f3a91cc8e4b205fd6e1c47b89a2d0fe34c2b8d76a91e5c08"`.

---

## Discovery filter guidance

Callers MAY filter discovery results on `outfit` and `outfit_hash` to scope
task dispatch to a particular configuration:

- Filter by `outfit: "engineer"` to match both bare `engineer` and any
  `engineer:<variant>` — implementations SHOULD treat the base name as a prefix
  match unless a variant is explicitly given.
- Filter by `outfit_hash` for exact version matching (e.g. reproducibility
  audits, A/B comparisons).

Callers that do not understand `outfit` or `outfit_hash` see a valid service
info object and interact with the agent normally.

---

## References

- §3.2 Required service metadata (this spec)
- §4 Discovery
- Appendix B.12 (service info wire example)
- Appendix C (known agent identifiers)
- orch multi-executor proposal: `docs/multi-executor-workers.md` (outfit bundle distribution)
