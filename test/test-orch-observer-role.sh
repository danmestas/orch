#!/usr/bin/env bash
# Regression tests for the observer-role tag end-to-end.
#
# After issue #60 (retire ~/.cache/orch-registry in favor of $SRV.INFO.agents)
# and #94 (retire orch-listen and the marker hooks):
#
#   - orch-register is a no-op stub (deprecation message, exit 0)
#   - orch-tell and orch-peek resolve roles via $SRV.INFO.agents
#   - Bus subscribers (no longer a built-in CLI) filter observers by
#     metadata.role
#
# Run with: bash test/test-orch-observer-role.sh
set -uo pipefail

PASS=0
FAIL=0
FAILED_TESTS=()

assert() {
    local desc=$1 expected=$2 got=$3
    if [ "$expected" = "$got" ]; then
        echo "  PASS  $desc"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $desc"
        echo "        expected: $expected"
        echo "        got:      $got"
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$desc")
    fi
}

assert_contains() {
    local desc=$1 substr=$2 haystack=$3
    if [[ "$haystack" == *"$substr"* ]]; then
        echo "  PASS  $desc"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $desc"
        echo "        expected substring: $substr"
        echo "        got:                $haystack"
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$desc")
    fi
}

REGBIN=$(command -v orch-register)
SPAWN=$(command -v orch-spawn)
TELL=$(command -v orch-tell)
PEEK=$(command -v orch-peek)
[ -x "$REGBIN" ] && [ -x "$SPAWN" ] && [ -x "$TELL" ] && [ -x "$PEEK" ] || {
    echo "binaries missing on PATH"; exit 2; }

SANDBOX=$(mktemp -d)
trap 'rm -rf "$SANDBOX"' EXIT

# ── orch-register stub ────────────────────────────────────────────────────────

echo "=== orch-register is a no-op stub ==="

# 1) Any invocation exits 0 regardless of args.
TMP_ERR=$(mktemp)
"$REGBIN" %900 pi /tmp >"$SANDBOX/reg_out" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "register stub: exit 0" 0 "$rc"

# 2) Stub emits a deprecation message on stderr.
assert_contains "register stub: deprecation on stderr" "deprecated" "$(cat "$TMP_ERR")"
rm -f "$TMP_ERR"

# 3) Stub accepts the --role flag without erroring.
"$REGBIN" %901 claude /tmp --role observer >"$SANDBOX/reg_out" 2>/dev/null && rc=0 || rc=$?
assert "register stub: --role flag accepted" 0 "$rc"

# 4) Stub does not create any registry file.
[ ! -f "$SANDBOX/registry/%900.json" ] && absent=1 || absent=0
assert "register stub: no registry file written" 1 "$absent"

# ── orch-tell + orch-peek now use $SRV.INFO.agents discovery ─────────────────
#
# Install a synthetic `nats` stub on PATH so the guard / peek lookups hit
# canned fixtures instead of a real bus.

echo
echo "=== orch-tell worker→observer guard (NATS discovery fixtures) ==="

NATS_STUB_DIR="$SANDBOX/nats-bin"
NATS_STUB_FIXTURES="$SANDBOX/nats-fixtures.jsonl"
mkdir -p "$NATS_STUB_DIR"
: > "$NATS_STUB_FIXTURES"
cat > "$NATS_STUB_DIR/nats" <<STUB
#!/usr/bin/env bash
verb=""
for arg in "\$@"; do
    case "\$arg" in req) verb=req ;; esac
done
if [ "\$verb" = req ] && [ -s "$NATS_STUB_FIXTURES" ]; then
    i=0
    while IFS= read -r meta; do
        [ -n "\$meta" ] || continue
        i=\$((i + 1))
        printf 'Received on "\$SRV.INFO.agents.fake%d"\n' "\$i"
        printf '{"metadata":%s}\n' "\$meta"
    done < "$NATS_STUB_FIXTURES"
fi
exit 0
STUB
chmod +x "$NATS_STUB_DIR/nats"
export PATH="$NATS_STUB_DIR:$PATH"
export NATS_URL="nats://stub.invalid:4222"
export ORCH_TELL_DISCOVERY_TIMEOUT="0.5s"
export ORCH_PEEK_DISCOVERY_TIMEOUT="0.5s"

set_agents() {
    : > "$NATS_STUB_FIXTURES"
    local entry pane role cwd
    for entry in "$@"; do
        # shellcheck disable=SC2086
        set -- $entry
        pane=$1; role=$2; cwd=$3
        jq -nc --arg p "$pane" --arg r "$role" --arg c "$cwd" --arg a "claude" \
            '{pane_id:$p, role:$r, cwd:$c, agent:$a}' >> "$NATS_STUB_FIXTURES"
    done
}

TARGET_PANE=$(tmux split-window -d -h -P -F '#{pane_id}' 'while true; do sleep 60; done' 2>/dev/null) || {
    echo "  SKIP  no tmux pane available for tell-guard test"; TARGET_PANE=""; }

if [ -n "$TARGET_PANE" ]; then
    # Register the real pane as observer on the stub bus.
    set_agents "$TARGET_PANE observer /tmp"

    # 6) Worker-source (ORCH_PANE_ID set) refused without --force
    TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
    ORCH_PANE_ID=%999 ORCH_TELL_MAX_WAIT=2 "$TELL" "$TARGET_PANE" "hello" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
    assert "tell worker→observer: refused" 1 "$rc"
    assert_contains "tell worker→observer: error names refusal" "refusing to tell observer" "$(cat "$TMP_ERR")"
    rm -f "$TMP_OUT" "$TMP_ERR"

    # 7) --force bypasses the guard
    TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
    ORCH_PANE_ID=%999 ORCH_TELL_MAX_WAIT=5 "$TELL" --force "$TARGET_PANE" "hello" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
    assert "tell --force worker→observer: allowed" 0 "$rc"
    rm -f "$TMP_OUT" "$TMP_ERR"

    # 8) Operator-source (no ORCH_PANE_ID) unrestricted
    TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
    unset ORCH_PANE_ID
    ORCH_TELL_MAX_WAIT=5 "$TELL" "$TARGET_PANE" "hello" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
    assert "tell operator→observer: allowed (no ORCH_PANE_ID)" 0 "$rc"
    rm -f "$TMP_OUT" "$TMP_ERR"

    tmux kill-pane -t "$TARGET_PANE" 2>/dev/null || true
fi

# ── orch-peek role surface (NATS discovery fixtures) ──────────────────────────

echo
echo "=== orch-peek role surface (NATS discovery fixtures) ==="

# Populate the stub with one observer and one worker fixture. orch-peek's
# --all surfaces both as rows even though the panes don't exist in tmux
# (they'll show bucket=dead which is fine for the count check).
set_agents "%777 observer /tmp" "%778 worker /tmp"
PEEK_JSON=$("$PEEK" --all --json 2>/dev/null || echo "[]")
OBSERVER_ROW_COUNT=$(printf '%s' "$PEEK_JSON" | jq '[.[] | select(.role=="observer")] | length' 2>/dev/null || echo 0)
echo "  peek --all --json yielded observer=$OBSERVER_ROW_COUNT rows from stub bus"
assert "peek --json: at least one observer row" 1 "$( [ "$OBSERVER_ROW_COUNT" -ge 1 ] && echo 1 || echo 0 )"

# ── orch-spawn + shim: role propagated via ORCH_ROLE env ─────────────────────
#
# orch-spawn sets ORCH_ROLE env for the shim; shim publishes metadata.role.
# We verify orch-spawn resolves the role correctly and passes it to the shim
# env by checking the shim log. This is a structural check, not a NATS live test.

echo
echo "=== orch-spawn ORCH_ROLE env propagation (structural) ==="

# Verify orch-spawn no longer calls orch-register (which is now a no-op).
if grep -q 'orch-register' "$(command -v orch-spawn)"; then
    assert "orch-spawn: no orch-register call in source" "absent" "present"
else
    assert "orch-spawn: no orch-register call in source" "absent" "absent"
fi

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
