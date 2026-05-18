# Docker test bench for sesh-specific communication patterns.
#
# Extends the orch substrate test (test/docker/) with a real sesh hub
# instead of a bare nats-server. Validates the patterns documented in
# sesh's README + working-with-sesh.md + nats-bridge.md — hub lifecycle,
# leaf attachment, cross-leaf pub/sub, JetStream durability, Fossil
# sync, subject scoping, and the orch-bridge integration.
#
# Multi-stage build:
#   builder  — clones sesh + EdgeSync (sibling-dir per sesh's go.mod
#              replace directive) + libfossil pulls transitively;
#              builds the `sesh` binary.
#   runtime  — Ubuntu 24.04 + Node + tmux + jq + nats CLI + orch from
#              npm pack + the prebuilt sesh binary copied from builder.

# === Stage 1: build sesh from source ======================================
FROM golang:1.26-bookworm AS builder

ARG SESH_REF=main
ARG EDGESYNC_REF=main
ARG SESH_OPS_REF=main

RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# sesh's go.mod has `replace github.com/danmestas/EdgeSync => ../EdgeSync`
# so the two repos must be sibling dirs at build time.
WORKDIR /src
RUN git clone --depth 1 --branch "$EDGESYNC_REF" https://github.com/danmestas/EdgeSync.git \
    && git clone --depth 1 --branch "$SESH_REF" https://github.com/danmestas/sesh.git \
    && git clone --depth 1 --branch "$SESH_OPS_REF" https://github.com/danmestas/sesh-ops.git

WORKDIR /src/sesh
RUN go build -o /out/sesh ./cmd/sesh \
    && /out/sesh --help >/dev/null 2>&1 || true

# sesh-ops is the reference CLI for sesh's task / goal / scoped-memory
# conventions. The bench's Group 9+ patterns drive these primitives —
# without sesh-ops on PATH the bench can only SKIP them.
WORKDIR /src/sesh-ops
RUN go build -o /out/sesh-ops ./cmd/sesh-ops \
    && /out/sesh-ops --help >/dev/null 2>&1 || true

# orch-agent-shim is no longer built from this repo — the shim moved to
# github.com/danmestas/synadia-agent-shim (orch#140 / proposal 0001).
# The runtime stage installs @agent-ops/synadia-agent-shim transitively
# via `npm install -g /tmp/orch-pack.tgz`, which fetches the platform
# binary in its postinstall. No builder-stage step needed here.

# === Stage 2: runtime image ===============================================
FROM ubuntu:24.04

ARG DEBIAN_FRONTEND=noninteractive
ARG NATS_CLI_VERSION=0.4.0
ARG TARGETARCH

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl gnupg git tmux jq fswatch xz-utils unzip openssl \
    && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# NATS CLI only — sesh embeds its own nats-server, no separate one needed.
RUN set -eux; arch="$TARGETARCH"; cd /tmp; \
    curl -fsSL -o nats.zip \
      "https://github.com/nats-io/natscli/releases/download/v${NATS_CLI_VERSION}/nats-${NATS_CLI_VERSION}-linux-${arch}.zip"; \
    unzip -q nats.zip; \
    install -m 0755 "nats-${NATS_CLI_VERSION}-linux-${arch}/nats" /usr/local/bin/nats; \
    rm -rf nats.zip "nats-${NATS_CLI_VERSION}-linux-${arch}"

# sesh + sesh-ops binaries from builder stage. orch-agent-shim now ships
# via the @agent-ops/synadia-agent-shim npm dependency installed below.
COPY --from=builder /out/sesh /usr/local/bin/sesh
COPY --from=builder /out/sesh-ops /usr/local/bin/sesh-ops

# orch + suit + wardrobe (matches test/docker/ pattern).
RUN npm install -g @agent-ops/suit
COPY orch-pack.tgz /tmp/orch-pack.tgz
RUN npm install -g /tmp/orch-pack.tgz

COPY inside-container/mock-agents/ /usr/local/bin/mock-agents/
RUN install -m 0755 /usr/local/bin/mock-agents/claude  /usr/local/bin/claude \
    && install -m 0755 /usr/local/bin/mock-agents/codex   /usr/local/bin/codex \
    && install -m 0755 /usr/local/bin/mock-agents/pi      /usr/local/bin/pi \
    && install -m 0755 /usr/local/bin/mock-agents/gemini  /usr/local/bin/gemini

COPY inside-container/bootstrap.sh inside-container/tests.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/bootstrap.sh /usr/local/bin/tests.sh

ENV HOME=/root
ENV TERM=xterm-256color
WORKDIR /root

ENTRYPOINT ["/usr/local/bin/bootstrap.sh"]
