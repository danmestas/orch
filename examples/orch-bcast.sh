#!/usr/bin/env bash
# Usage: orch-bcast.sh <pane> <method:stop-hook|polling> <agent_label> <prompt>
# Captures end-to-end timing for one harness in a broadcast experiment.
set -uo pipefail

PANE=$1; METHOD=$2; AGENT=$3; PROMPT=$4

now_ns() { date +%s%N; }

T_BASH_START_NS=$(now_ns)

# Pre-snapshot scrollback for diff
BEFORE=$(mktemp -t hbcast-before-XXXXXX)
trap 'rm -f "$BEFORE" "$AFTER" 2>/dev/null' EXIT
tmux capture-pane -t "$PANE" -pS -4000 > "$BEFORE"
LAST_LINE=$(tac "$BEFORE" | grep -m1 -v '^[[:space:]]*$' || true)

T_SEND_NS=$(now_ns)
orch-tell "$PANE" "$PROMPT"

case "$METHOD" in
    stop-hook)
        # Inline single-pane Stop wait via fswatch; deleted orch-watch-stop in favor
        # of orch-listen (multi-pane), and broadcast doesn't want the multi-pane
        # semantics (each bg bash is paired with one pane).
        STOPINFO=/tmp/hbcast-stopinfo-${PANE//%/p}.txt
        DIR="${ORCH_STOP_DIR:-$HOME/.cache/orch-stop}"
        TARGET="$DIR/$PANE.event"
        rm -f "$STOPINFO" "$TARGET"
        deadline=$(( $(date +%s) + 600 ))
        while [ ! -e "$TARGET" ]; do
            [ "$(date +%s)" -ge "$deadline" ] && { echo "TIMEOUT" > "$STOPINFO"; break; }
            fswatch -1 "$DIR" > /dev/null 2>&1 || sleep 1
        done
        [ -e "$TARGET" ] && cat "$TARGET" > "$STOPINFO"
        T_SETTLED_NS=$(now_ns)
        ;;
    polling)
        # Let the prompt echo land before sampling
        sleep 3
        orch-wait "$PANE" --stable 3 --interval 1 --timeout 600 --quiet
        T_SETTLED_NS=$(now_ns)
        STOPINFO=""
        ;;
    *)
        echo "ERR: unknown method $METHOD" >&2; exit 1 ;;
esac

# Capture response
AFTER=$(mktemp -t hbcast-after-XXXXXX)
tmux capture-pane -t "$PANE" -pS -4000 > "$AFTER"

T_BASH_END_NS=$(now_ns)

# Emit a structured report
{
    echo "AGENT=$AGENT"
    echo "PANE=$PANE"
    echo "METHOD=$METHOD"
    echo "T_BASH_START_NS=$T_BASH_START_NS"
    echo "T_SEND_NS=$T_SEND_NS"
    echo "T_SETTLED_NS=$T_SETTLED_NS"
    echo "T_BASH_END_NS=$T_BASH_END_NS"
    echo "DELTA_SEND_TO_SETTLED_MS=$(( (T_SETTLED_NS - T_SEND_NS) / 1000000 ))"
    echo "DELTA_SETTLED_TO_BASH_END_MS=$(( (T_BASH_END_NS - T_SETTLED_NS) / 1000000 ))"
    if [ -n "$STOPINFO" ] && [ -s "$STOPINFO" ]; then
        echo "=== stop-hook payload ==="
        cat "$STOPINFO"
        # Compute hook-fire to bash-end latency precisely
        HOOK_NS=$(grep '^ts_ns=' "$STOPINFO" | cut -d= -f2)
        if [ -n "$HOOK_NS" ]; then
            echo "DELTA_HOOK_TO_BASH_END_MS=$(( (T_BASH_END_NS - HOOK_NS) / 1000000 ))"
        fi
    fi
    echo "=== response (new content since send) ==="
    if [ -n "$LAST_LINE" ]; then
        LM=$(grep -nF -- "$LAST_LINE" "$AFTER" | tail -1 | cut -d: -f1 || true)
        if [ -n "$LM" ]; then
            tail -n +"$((LM + 1))" "$AFTER"
        else
            cat "$AFTER"
        fi
    else
        cat "$AFTER"
    fi
}
