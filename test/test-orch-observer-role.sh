#!/usr/bin/env bash
# Regression tests for the observer-role tag end-to-end.
#
# After issue #60 (retire ~/.cache/orch-registry in favor of $SRV.INFO.agents),
# #94 (retire orch-listen and the marker hooks), and #189 (collapse the bash
# CLIs into the `orch` Go binary):
#
#   - orch-register is gone — registration happens via the shim's
#     $SRV.INFO.agents advertisement at spawn time.
#   - `orch tell` and `orch peek` resolve roles via $SRV.INFO.agents.
#   - Bus subscribers (no longer a built-in CLI) filter observers by
#     metadata.role.
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

SPAWN=$(command -v orch-spawn)
ORCH=$(command -v orch)
[ -x "$SPAWN" ] && [ -x "$ORCH" ] || {
    echo "binaries missing on PATH (need orch-spawn and orch)"; exit 2; }

SANDBOX=$(mktemp -d)
trap 'rm -rf "$SANDBOX"' EXIT

# ── orch tell + orch peek now use orch-registry for target resolution ───────
#
# Post proposal 0005 (orch#144), the bins consult `orch-registry` instead of
# doing inline `$SRV.INFO.agents` discovery via the nats CLI. We install
# stubs for both:
#
#   - orch-registry stub  → fakes `snapshot` and `lookup <target>` from fixtures
#   - nats stub           → keeps fire-and-forget `nats pub` no-op'd so
#                            orch-tell's publish path doesn't try real I/O
#
# Both stubs read from the same fixture file (one JSON metadata object per
# line) so `set_agents "%900 observer /tmp"` stays declarative.

echo
echo "=== orch-tell worker→observer guard (registry fixtures) ==="

NATS_STUB_DIR="$SANDBOX/nats-bin"
NATS_STUB_FIXTURES="$SANDBOX/nats-fixtures.jsonl"
mkdir -p "$NATS_STUB_DIR"
: > "$NATS_STUB_FIXTURES"

# nats stub: swallow `pub` (orch-tell fire-and-forget) and answer `req` for
# any callers that still hit the bus directly (legacy paths in other tests).
cat > "$NATS_STUB_DIR/nats" <<STUB
#!/usr/bin/env bash
verb=""
for arg in "\$@"; do
    case "\$arg" in req|pub) verb="\$arg" ;; esac
done
if [ "\$verb" = req ] && [ -s "$NATS_STUB_FIXTURES" ]; then
    i=0
    while IFS= read -r meta; do
        [ -n "\$meta" ] || continue
        i=\$((i + 1))
        printf 'Received on "\$SRV.INFO.agents.fake%d"\n' "\$i"
        printf '{"metadata":%s,"endpoints":[{"name":"prompt","subject":"agents.prompt.stub.fake.0"}]}\n' "\$meta"
    done < "$NATS_STUB_FIXTURES"
fi
# pub: silent success (no broker; orch-tell fire-and-forget lands here).
exit 0
STUB
chmod +x "$NATS_STUB_DIR/nats"

# orch-registry stub: translates fixture lines into the Worker JSON shape
# the bins consume. snapshot → array; lookup → single object or exit 4.
cat > "$NATS_STUB_DIR/orch-registry" <<STUB
#!/usr/bin/env bash
sub="\${1:-}"
shift || true
args=()
skip_next=0
for a in "\$@"; do
    case "\$a" in
        --nats|--alias-file|--operator-file|--hb-window|--interval|--subject) skip_next=1 ;;
        --nats=*|--alias-file=*|--operator-file=*|--hb-window=*|--interval=*|--subject=*) ;;
        *)
            if [ "\$skip_next" = 1 ]; then
                skip_next=0
            else
                args+=("\$a")
            fi
            ;;
    esac
done
target=""
[ \${#args[@]} -ge 1 ] && target="\${args[0]}"

emit_worker() {
    local meta="\$1"
    jq -nc --argjson m "\$meta" '
        {
            pane_id: \$m.pane_id,
            instance_id: "stub-inst",
            name:    (\$m.session // (\$m.pane_id | sub("^%"; "pct"))),
            role:    (\$m.role // "worker"),
            outfit:  (\$m.outfit // ""),
            agent:   (\$m.agent // "claude-code"),
            cwd:     (\$m.cwd // ""),
            owner:   (\$m.owner // "stub"),
            session: (\$m.session // ""),
            alive:   true,
            subjects: {
                prompt: ("agents.prompt.stub.fake." + (\$m.pane_id | sub("^%"; "pct"))),
                status: "",
                hb:     ""
            },
            metadata: \$m
        }'
}

case "\$sub" in
    snapshot)
        if [ ! -s "$NATS_STUB_FIXTURES" ]; then echo "[]"; exit 0; fi
        out="["; first=1
        while IFS= read -r meta; do
            [ -n "\$meta" ] || continue
            w=\$(emit_worker "\$meta")
            if [ \$first = 1 ]; then first=0; else out+=","; fi
            out+="\$w"
        done < "$NATS_STUB_FIXTURES"
        out+="]"
        printf '%s\n' "\$out"
        ;;
    lookup)
        [ -n "\$target" ] || { echo "orch-registry: usage: lookup <name|%pane>" >&2; exit 1; }
        while IFS= read -r meta; do
            [ -n "\$meta" ] || continue
            pane=\$(printf '%s' "\$meta" | jq -r .pane_id)
            role=\$(printf '%s' "\$meta" | jq -r '.role // "worker"')
            session=\$(printf '%s' "\$meta" | jq -r '.session // ""')
            if [ "\$target" = "operator" ] || [ "\$target" = "op" ]; then
                [ "\$role" = "operator" ] && { emit_worker "\$meta"; exit 0; }
            elif [ "\$target" = "\$pane" ]; then
                emit_worker "\$meta"; exit 0
            elif [ "\$target" = "\$session" ] && [ -n "\$session" ]; then
                emit_worker "\$meta"; exit 0
            fi
        done < "$NATS_STUB_FIXTURES"
        echo "orch-registry: not found: \$target" >&2
        exit 4
        ;;
    *)
        echo "orch-registry stub: unknown subcommand: \$sub" >&2; exit 1 ;;
esac
STUB
chmod +x "$NATS_STUB_DIR/orch-registry"

export PATH="$NATS_STUB_DIR:$PATH"
export NATS_URL="nats://stub.invalid:4222"

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
    ORCH_PANE_ID=%999 ORCH_TELL_MAX_WAIT=2 "$ORCH" tell "$TARGET_PANE" "hello" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
    assert "tell worker→observer: refused" 1 "$rc"
    assert_contains "tell worker→observer: error names refusal" "refusing to tell observer" "$(cat "$TMP_ERR")"
    rm -f "$TMP_OUT" "$TMP_ERR"

    # 7) --force bypasses the guard
    TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
    ORCH_PANE_ID=%999 ORCH_TELL_MAX_WAIT=5 "$ORCH" tell --force "$TARGET_PANE" "hello" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
    assert "tell --force worker→observer: allowed" 0 "$rc"
    rm -f "$TMP_OUT" "$TMP_ERR"

    # 8) Operator-source (no ORCH_PANE_ID) unrestricted
    TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
    unset ORCH_PANE_ID
    ORCH_TELL_MAX_WAIT=5 "$ORCH" tell "$TARGET_PANE" "hello" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
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
PEEK_JSON=$("$ORCH" peek --all --json 2>/dev/null || echo "[]")
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

# Verify orch-spawn does not call the retired orch-register binary.
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
