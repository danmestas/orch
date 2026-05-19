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
#     ORCH_VERIFY_TIMEOUT    — total readiness poll budget in seconds (default: 60)
#     ORCH_VERIFY_BACKOFF    — comma-separated wait sequence between verify
#                              attempts (default: `1,2,4,8`). Total wall time
#                              is capped by ORCH_VERIFY_TIMEOUT.
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

# Wrapper tail behaviour: by default, after the agent exits we pause on a
# `read` and then `exec $SHELL -l` so an interactive operator can inspect
# output, drop to a shell, etc. In CI / test contexts that pause is the
# source of zombie panes (closes #178): the agent fails fast, `read`
# blocks forever, the test's `kill-pane` destroys the PTY but the zsh
# wrapper survives (no TTY → `exec $SHELL -l` hangs strangely), and the
# enclosing tmux session can't tear down. Setting
# ORCH_NO_PAUSE_ON_EXIT=1 in the environment that orch-spawn runs in
# drops the pause+shell-fallback tail so agent exit cleanly closes the
# pane. Default behaviour (interactive operator use) is preserved.
if [ "${ORCH_NO_PAUSE_ON_EXIT:-0}" -ne 1 ]; then
    WRAP="$WRAP; echo; echo '[$AGENT exited — press enter]'; read; exec \$SHELL -l"
fi

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
    # cwd-prompt or error text. pi's banner is not yet verified past its
    # first-run dialog; it falls through to title-rename.
    case "$AGENT" in
        claude) BANNER="Claude Code" ;;
        gemini) BANNER="Gemini CLI" ;;
        codex)  BANNER="OpenAI Codex" ;;
        pi)     BANNER="" ;;
        *)      BANNER="" ;;
    esac

    # Retry-with-backoff (closes #28). A fixed timeout is simultaneously too
    # short for cold starts (heavy outfit, MCP load, slow CI runner) and too
    # long for permanently-broken workers (missing binary, wrong CWD). The
    # backoff sequence lets operators tolerate slow starts without paying
    # the worst-case wall time on every failure.
    #
    # ORCH_VERIFY_BACKOFF is a comma-separated wait sequence (default
    # `1,2,4,8`) — the wait BEFORE each attempt's readiness check. After
    # the first attempt finishes its readiness probe, we sleep the second
    # entry, probe again, and so on. The total wall time is capped at
    # ORCH_VERIFY_TIMEOUT regardless of how many attempts remain.
    #
    # Fail-fast cases stop the loop before the next attempt:
    #   - pane vanished from tmux (`pane died`) — the WRAP itself crashed,
    #     no point retrying
    #   - capture-pane shows `command not found` / `No such file or
    #     directory` for the agent binary — harness isn't installed,
    #     retrying won't summon it
    BACKOFF_SPEC=${ORCH_VERIFY_BACKOFF:-1,2,4,8}
    # Split comma-separated sequence into an array. Empty entries are
    # tolerated (just skipped); non-numeric entries fall through to sleep
    # which will error — refuse early instead.
    IFS=',' read -r -a BACKOFF_WAITS <<< "$BACKOFF_SPEC"
    for _w in "${BACKOFF_WAITS[@]}"; do
        case "$_w" in
            ''|*[!0-9.]*)
                echo "orch-spawn: ORCH_VERIFY_BACKOFF must be comma-separated numbers (got: $BACKOFF_SPEC)" >&2
                exit 1 ;;
        esac
    done
    [ "${#BACKOFF_WAITS[@]}" -gt 0 ] || BACKOFF_WAITS=(1 2 4 8)

    start_ts=$(date +%s)
    deadline=$(( start_ts + VERIFY_TIMEOUT ))
    verify_state=""
    attempt=0
    attempts_made=0
    for wait_s in "${BACKOFF_WAITS[@]}"; do
        attempt=$((attempt + 1))
        # Cap each wait so we never sleep past the deadline.
        now=$(date +%s)
        remaining=$(( deadline - now ))
        [ "$remaining" -le 0 ] && break
        # Truncate sleep if it would overshoot the deadline. Use python/awk-free
        # integer comparison; sub-second backoffs are rare in practice.
        eff_wait=$wait_s
        # Compare as integers using awk only when fractional — keep the common
        # integer case shell-only for portability.
        case "$wait_s" in
            *.*)
                eff_wait=$(awk -v w="$wait_s" -v r="$remaining" 'BEGIN{print (w<r)?w:r}') ;;
            *)
                [ "$wait_s" -gt "$remaining" ] && eff_wait=$remaining ;;
        esac
        sleep "$eff_wait"
        attempts_made=$attempt

        # Fail-fast: pane gone.
        if ! tmux list-panes -a -F '#{pane_id}' 2>/dev/null | grep -qx "$PANE"; then
            verify_state="died"; break
        fi

        # Readiness probe: title rename OR banner match.
        cur_cmd=$(tmux display -p -t "$PANE" '#{pane_current_command}' 2>/dev/null || echo "")
        case "$cur_cmd" in
            ""|zsh|bash|sh|fish|dash|ksh)
                if [ -n "$BANNER" ] && \
                   tmux capture-pane -p -J -t "$PANE" 2>/dev/null | grep -qF "$BANNER"; then
                    verify_state="ready"; break
                fi
                # Fail-fast: missing harness binary surfaces as a shell error
                # line in the captured buffer. Patterns cover bash/zsh/dash and
                # the macOS `not found` variant. The WRAP's trailing `read`
                # keeps the pane alive with $SHELL foreground, so we'd
                # otherwise burn the full ORCH_VERIFY_TIMEOUT before failing.
                cap=$(tmux capture-pane -p -J -t "$PANE" 2>/dev/null || echo "")
                if printf '%s' "$cap" | grep -qE "($AGENT: command not found|command not found: $AGENT|$AGENT: not found|No such file or directory.*$AGENT)"; then
                    verify_state="missing-binary"; break
                fi
                ;;
            *)
                verify_state="ready"; break ;;
        esac

        # Out of attempts? Bail before the next iter's sleep so the failure
        # surfaces against the attempt count, not the wall clock.
        [ "$(date +%s)" -ge "$deadline" ] && break
    done

    elapsed=$(( $(date +%s) - start_ts ))
    case "$verify_state" in
        ready)
            echo "orch-spawn: agent ready in pane $PANE (attempt $attempts_made/${#BACKOFF_WAITS[@]}, ${elapsed}s)" >&2 ;;
        died)
            echo "orch-spawn: agent failed to start in $PANE (pane died, attempt $attempts_made/${#BACKOFF_WAITS[@]})" >&2; exit 1 ;;
        missing-binary)
            echo "orch-spawn: agent failed to start in $PANE ($AGENT binary missing — install the harness CLI; verify failed after $attempts_made attempts)" >&2; exit 1 ;;
        *)
            echo "orch-spawn: agent failed to start in $PANE (verify failed after $attempts_made attempts, timeout after ${elapsed}s; set ORCH_VERIFY_TIMEOUT or ORCH_VERIFY_BACKOFF, or pass --no-verify to skip)" >&2; exit 1 ;;
    esac
else
    sleep 1
fi

echo "$PANE"
