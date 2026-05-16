#!/usr/bin/env bash
# executors/tmux/spawn.sh — tmux executor spawn primitive.
#
# Called by bin/orch-spawn (the dispatcher) after argument parsing is complete.
# All context is passed via exported environment variables:
#
#   Required:
#     AGENT            — harness name (claude|pi|codex|gemini)
#     CWD              — working directory for the spawned pane
#     HEADLESS         — 1 for detached orch-headless session, 0 for current window
#     POSITION         — pane split direction: right|left|above|below (headed only)
#     ROLE             — worker|observer
#     NO_FLEET         — 1 to skip fleet-doctrine injection
#     VERIFY           — 1 to poll for readiness before returning
#     GOAL_EXPORTS     — shell fragment exporting SESH_GOAL_* vars (may be empty)
#     NO_SHIM          — 1 to skip orch-agent-shim launch
#     OUTFIT           — outfit name (may be empty)
#     BUNDLE           — suit bundle dir (may be empty; set when OUTFIT is set)
#
#   Optional:
#     ORCH_HEADLESS_SESSION  — name for the headless tmux session (default: orch-headless)
#     ORCH_VERIFY_TIMEOUT    — readiness poll budget in seconds (default: 60)
#
# Output contract (inherited from bin/orch-spawn):
#   stdout — exactly one line: the new pane id (e.g. %42)
#   stderr — informational and error messages
#
# Executor lifecycle contract:
#   spawn → pane_id   (this script)
#   stop  → orch-down <pane_id>  (standard harness teardown)
#
# Synadia metadata advertised in $SRV.INFO:
#   executor: tmux
#   location: local  (same machine as operator)
set -euo pipefail

# Per-agent wrap. Codex/gemini get fleet doctrine via global instructions files
# (injected by install.sh into ~/.codex/AGENTS.md and ~/.gemini/GEMINI.md), so
# no per-spawn flag needed for those.
case $AGENT in
    claude)
        if [ -n "${BUNDLE:-}" ]; then
            # Bundle-based, cwd-inverted: project is cwd (project's CLAUDE.md
            # auto-loads), bundle is --add-dir (skills/agents/hooks accessible).
            # `--add-dir` does NOT load <dir>/CLAUDE.md, so merge the bundle's
            # CLAUDE.md with fleet doctrine into one appended system prompt.
            # Trap removes the bundle (and the merged file inside it) on pane
            # death.
            MERGED="$BUNDLE/.orch-merged-prompt.md"
            : > "$MERGED"
            [ -f "$BUNDLE/CLAUDE.md" ] && cat "$BUNDLE/CLAUDE.md" >> "$MERGED"
            if [ "${NO_FLEET:-0}" -eq 0 ] && [ -f "$HOME/.cache/orch-fleet-prompt.md" ]; then
                [ -s "$MERGED" ] && printf '\n\n' >> "$MERGED"
                cat "$HOME/.cache/orch-fleet-prompt.md" >> "$MERGED"
            fi
            WRAP="trap 'rm -rf \"$BUNDLE\"' EXIT; export ORCH_PANE_ID=\$TMUX_PANE;${GOAL_EXPORTS} cd \"$CWD\" && claude --dangerously-skip-permissions --add-dir \"$BUNDLE\""
            [ -s "$MERGED" ] && WRAP="$WRAP --append-system-prompt-file \"$MERGED\""
        else
            WRAP="export ORCH_PANE_ID=\$TMUX_PANE;${GOAL_EXPORTS} cd \"$CWD\" && claude --dangerously-skip-permissions"
            [ "${NO_FLEET:-0}" -eq 0 ] && WRAP="$WRAP --append-system-prompt-file $HOME/.cache/orch-fleet-prompt.md"
        fi
        ;;
    pi)
        # PI_TELEMETRY=0 suppresses pi's post-update install-telemetry ping
        # (silent when enabled, no first-run dialog). --offline skips startup
        # changelog / package-update HTTP probes so a fresh container boots
        # without network round-trips before the operator's first prompt. pi
        # has no blocking first-run dialogs (no trust gate, no telemetry
        # consent, no model picker — source-confirmed in pi-coding-agent's
        # interactive-mode init), so nothing else needs bypassing.
        WRAP="export ORCH_PANE_ID=\$TMUX_PANE PI_TELEMETRY=0;${GOAL_EXPORTS} cd \"$CWD\" && pi --offline"
        [ "${NO_FLEET:-0}" -eq 0 ] && WRAP="$WRAP --append-system-prompt $HOME/.cache/orch-fleet-prompt.md"
        ;;
    codex)
        # Bypass codex's blocking first-run dialogs (#37, #46) on fresh envs.
        #
        # The trust gate at codex-rs/tui/src/lib.rs reads
        # active_project.trust_level, which the loader resolves by looking up
        # the cwd as a DUNCE-canonicalised path key under [projects."<path>"].
        # Our prior approach passed `-c projects."$CWD".trust_level="trusted"`
        # inline, but that writes the override under the RAW $CWD; when the
        # canonical form differs (e.g. macOS /tmp -> /private/tmp) the lookup
        # misses and the trust dialog fires anyway. The reliable fix is to
        # pre-stage the canonical key in ~/.codex/config.toml, idempotently.
        #
        # The migrate-from-claude prompt is gated by features.external_migration
        # (codex-rs/tui/src/external_agent_config_migration_startup.rs). Our
        # prior wrap used --enable, which is precisely what TURNS THE GATE ON
        # — flipping to --disable suppresses the prompt at the gate.
        #
        # The periodic "Update available!" prompt is NOT bypassable via flags —
        # codex stores the user's "skip until next version" choice in
        # ~/.codex/config.toml after one interactive answer. One-time manual
        # step for new installs.
        CANON_CWD=$(cd "$CWD" && pwd -P)
        mkdir -p "$HOME/.codex"
        touch "$HOME/.codex/config.toml"
        if ! grep -qF "[projects.\"$CANON_CWD\"]" "$HOME/.codex/config.toml"; then
            printf '\n[projects."%s"]\ntrust_level = "trusted"\n' "$CANON_CWD" >> "$HOME/.codex/config.toml"
        fi
        WRAP="export ORCH_PANE_ID=\$TMUX_PANE;${GOAL_EXPORTS} cd \"$CWD\" && codex --disable external_migration --dangerously-bypass-approvals-and-sandbox"
        ;;
    gemini)
        # --skip-trust bypasses gemini's Folder Trust dialog (only active when
        # security.folderTrust.enabled=true in ~/.gemini/settings.json) AND
        # prevents --yolo from being silently downgraded to "default" approval
        # mode in cwds the operator hasn't explicitly trusted. Documented in
        # gemini-cli's bundle/docs/cli/trusted-folders.md under "Headless and
        # automated environments". Auth / theme / IDE-nudge dialogs do not
        # fire when ~/.gemini/{settings.json,oauth_creds.json} already exist
        # with a selectedAuthType — true on any host signed in once.
        WRAP="export ORCH_PANE_ID=\$TMUX_PANE;${GOAL_EXPORTS} cd \"$CWD\" && gemini --yolo --skip-trust"
        ;;
    *)
        echo "orch-spawn: unknown agent: $AGENT (expected claude|pi|codex|gemini)" >&2; exit 1 ;;
esac

WRAP="$WRAP; echo; echo '[$AGENT exited — press enter]'; read; exec \$SHELL -l"

if [ "${HEADLESS:-0}" -eq 1 ]; then
    SESSION=${ORCH_HEADLESS_SESSION:-orch-headless}
    if tmux has-session -t "$SESSION" 2>/dev/null; then
        PANE=$(tmux new-window -d -t "$SESSION:" -n "$AGENT" -P -F '#{pane_id}' "$WRAP")
    else
        PANE=$(tmux new-session -d -s "$SESSION" -n "$AGENT" -P -F '#{pane_id}' "$WRAP")
    fi
else
    # Prefer $TMUX_PANE (the invoker's pane); tmux display returns the
    # active client's focused pane which drifts on focus changes.
    CUR=${TMUX_PANE:-$(tmux display -p '#{pane_id}')}
    case "${POSITION:-right}" in
        right) SPLIT_ARGS=(-h) ;;
        left)  SPLIT_ARGS=(-h -b) ;;
        above) SPLIT_ARGS=(-v -b) ;;
        below) SPLIT_ARGS=(-v) ;;
    esac
    PANE=$(tmux split-window -d "${SPLIT_ARGS[@]}" -P -F '#{pane_id}' -t "$CUR" "$WRAP")
fi

# Brief settle so ORCH_PANE_ID is in the agent's env before the listener
# might fire and so registry capture is meaningful.
if [ "${VERIFY:-0}" -eq 1 ]; then
    VERIFY_TIMEOUT=${ORCH_VERIFY_TIMEOUT:-60}
    # Banner: a distinctive substring of the agent's TUI startup. Used as a
    # second readiness signal when the process-title rename lags behind the
    # interactive REPL (large outfits + accessories load a sizable CLAUDE.md
    # and MCP servers, which can push the rename past the title check).
    # Empty BANNER → title-rename only (unchanged legacy behaviour). Only
    # banners verified empirically against the agent's actual TUI go here —
    # a too-generic string risks false-positive matches against the WRAP's
    # cwd-prompt or error text. codex / pi banners are not yet verified
    # past their first-run dialogs; they fall through to title-rename.
    case "$AGENT" in
        claude) BANNER="Claude Code" ;;
        gemini) BANNER="Gemini CLI" ;;
        codex)  BANNER="" ;;
        pi)     BANNER="" ;;
        *)      BANNER="" ;;
    esac
    deadline=$(( $(date +%s) + VERIFY_TIMEOUT ))
    verify_state=""
    while [ "$(date +%s)" -lt "$deadline" ]; do
        if ! tmux list-panes -a -F '#{pane_id}' 2>/dev/null | grep -qx "$PANE"; then
            verify_state="died"; break
        fi
        cur_cmd=$(tmux display -p -t "$PANE" '#{pane_current_command}' 2>/dev/null || echo "")
        case "$cur_cmd" in
            ""|zsh|bash|sh|fish|dash|ksh)
                # Title-rename not yet — try banner-match as the cheap
                # fallback. If BANNER is empty we skip and keep polling.
                if [ -n "$BANNER" ] && \
                   tmux capture-pane -p -J -t "$PANE" 2>/dev/null | grep -qF "$BANNER"; then
                    verify_state="ready"; break
                fi
                ;;
            *)
                verify_state="ready"; break ;;
        esac
        sleep 0.5
    done
    case "$verify_state" in
        ready)
            echo "orch-spawn: agent ready in pane $PANE" >&2 ;;
        died)
            echo "orch-spawn: agent failed to start in $PANE (pane died)" >&2; exit 1 ;;
        *)
            echo "orch-spawn: agent failed to start in $PANE (timeout)" >&2; exit 1 ;;
    esac
else
    sleep 1
fi

echo "$PANE"
