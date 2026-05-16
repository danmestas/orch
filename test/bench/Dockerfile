# Benchmark image: extends the orch Docker test base with the bench runner.
#
# Built by test/bench/measure.sh — that script npm-packs the working tree,
# copies it in as orch-pack.tgz, and passes the test/docker/ dir as the build
# context so all shared layers (NATS, Node, mock-claude) are reused.
#
# The bench-runner script is added on top; `measure.sh` sets --entrypoint to
# /usr/local/bin/bench-runner.sh instead of the default bootstrap.sh.
FROM ubuntu:24.04

ARG DEBIAN_FRONTEND=noninteractive
ARG NATS_CLI_VERSION=0.4.0
ARG NATS_SERVER_VERSION=2.14.0
ARG TARGETARCH

# --- OS deps ---------------------------------------------------------------
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl gnupg git tmux jq fswatch xz-utils unzip bc \
    && rm -rf /var/lib/apt/lists/*

# --- Node 20 ---------------------------------------------------------------
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# --- NATS CLI + nats-server ------------------------------------------------
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

# --- orch (from local pack) ------------------------------------------------
COPY orch-pack.tgz /tmp/orch-pack.tgz
RUN npm install -g /tmp/orch-pack.tgz

# --- Mock claude on PATH ---------------------------------------------------
COPY mock-claude /usr/local/bin/claude
RUN chmod +x /usr/local/bin/claude

# --- orch-agent-shim binary (pre-built by measure.sh on the host) ----------
# Copied in if available; bench-runner.sh skips PATH B gracefully if absent.
COPY orch-agent-shim* /usr/local/bin/
RUN chmod +x /usr/local/bin/orch-agent-shim 2>/dev/null || true

# --- Bench runner ----------------------------------------------------------
COPY bench-runner.sh /usr/local/bin/bench-runner.sh
RUN chmod +x /usr/local/bin/bench-runner.sh

ENV HOME=/root
ENV NATS_URL=nats://localhost:4222
ENV TERM=xterm-256color
WORKDIR /root

ENTRYPOINT ["/usr/local/bin/bench-runner.sh"]
