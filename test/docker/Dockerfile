# Docker test bench for orch: clean Ubuntu + Node + nats + tmux + orch +
# suit. Runs the smoke suite in test/docker/inside-container/tests.sh.
#
# Build with the host helper at test/docker/run-tests.sh — it `npm pack`s
# the working tree first and copies the tarball in, so the image tests the
# code on your current branch (not the npm registry version).
#
# Multi-stage: Stage 1 compiles orch-agent-shim (Go binary) used by T9/T10/T11.
# === Stage 1: build orch-agent-shim =======================================
FROM golang:1.26-bookworm AS shimbuilder

RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY orch-src.tar /tmp/orch-src.tar
RUN mkdir -p /src/orch /out
WORKDIR /src/orch
RUN tar -xf /tmp/orch-src.tar
RUN go build -o /out/orch-agent-shim ./cmd/orch-agent-shim

# === Stage 2: runtime image ===============================================
FROM ubuntu:24.04

ARG DEBIAN_FRONTEND=noninteractive
ARG NATS_CLI_VERSION=0.4.0
ARG NATS_SERVER_VERSION=2.14.0

# Buildx sets TARGETARCH automatically (amd64 / arm64 / ...).
ARG TARGETARCH

# --- OS deps ---------------------------------------------------------------
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl gnupg git tmux jq fswatch xz-utils unzip \
    && rm -rf /var/lib/apt/lists/*

# --- Node 20 (for npm-installable orch + suit) -----------------------------
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# --- NATS CLI + nats-server ------------------------------------------------
# NATS CLI ships as per-arch .zip; nats-server ships as per-arch .tar.gz.
# Both from GitHub releases as single static binaries.
RUN set -eux; \
    arch="$TARGETARCH"; \
    cd /tmp; \
    curl -fsSL -o nats-cli.zip \
      "https://github.com/nats-io/natscli/releases/download/v${NATS_CLI_VERSION}/nats-${NATS_CLI_VERSION}-linux-${arch}.zip"; \
    unzip -q nats-cli.zip; \
    install -m 0755 "nats-${NATS_CLI_VERSION}-linux-${arch}/nats" /usr/local/bin/nats; \
    rm -rf nats-cli.zip "nats-${NATS_CLI_VERSION}-linux-${arch}"; \
    curl -fsSL -o nats-server.tar.gz \
      "https://github.com/nats-io/nats-server/releases/download/v${NATS_SERVER_VERSION}/nats-server-v${NATS_SERVER_VERSION}-linux-${arch}.tar.gz"; \
    tar -xzf nats-server.tar.gz; \
    install -m 0755 "nats-server-v${NATS_SERVER_VERSION}-linux-${arch}/nats-server" /usr/local/bin/nats-server; \
    rm -rf nats-server.tar.gz "nats-server-v${NATS_SERVER_VERSION}-linux-${arch}"

# --- suit (wardrobe) -------------------------------------------------------
# Latest from npm — separate from orch so it can be pinned later.
RUN npm install -g @agent-ops/suit

# --- orch (from local pack — built by host script) -------------------------
COPY orch-pack.tgz /tmp/orch-pack.tgz
RUN npm install -g /tmp/orch-pack.tgz

# --- orch-agent-shim binary (for T9/T10/T11 Synadia contract tests) --------
COPY --from=shimbuilder /out/orch-agent-shim /usr/local/bin/orch-agent-shim

# --- Mock agents on PATH ---------------------------------------------------
COPY inside-container/mock-agents/ /usr/local/bin/mock-agents/
RUN install -m 0755 /usr/local/bin/mock-agents/claude /usr/local/bin/claude

# --- Test scripts ----------------------------------------------------------
COPY inside-container/bootstrap.sh inside-container/tests.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/bootstrap.sh /usr/local/bin/tests.sh

# --- Runtime config --------------------------------------------------------
ENV HOME=/root
ENV NATS_URL=nats://localhost:4222
ENV TERM=xterm-256color
WORKDIR /root

ENTRYPOINT ["/usr/local/bin/bootstrap.sh"]
