#!/usr/bin/env bash
# Usage: orch-bcast.sh <pane> <method:shim|polling> <agent_label> <prompt>
# Captures end-to-end timing for one harness in a broadcast experiment.
#
# As of orch#94, the only event channel is the Synadia bus. `shim` mode waits
# for a turn-end status chunk on `agents.>` for the target pane; `polling`
# mode uses screen-stability via orch-wait for adapter-less harnesses.
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
orch tell "$PANE" "$PROMPT"

case "$METHOD" in
    shim)
        # Subscribe to this pane's event stream and wait for an ack/terminator.
        OWNER="${ORCH_OWNER:-$USER}"
        PANE_ENC="pct${PANE#%}"
        STOPINFO=/tmp/hbcast-stopinfo-${PANE//%/p}.txt
        rm -f "$STOPINFO"
        timeout 600 nats sub --raw --count=1 \
            "agents.status.cc.${OWNER}.${PANE_ENC}" >"$STOPINFO" 2>&1 || \
            echo "TIMEOUT" > "$STOPINFO"
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
        echo "ERR: unknown method $METHOD (expected shim|polling)" >&2; exit 1 ;;
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
        echo "=== shim turn-end payload ==="
        cat "$STOPINFO"
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
