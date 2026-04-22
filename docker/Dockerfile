# syntax=docker/dockerfile:1

# Global ARGs — versions are defined in versions.yaml (no defaults here).
ARG BASE_IMAGE
ARG GOLANG_VERSION
ARG NODE_VERSION

# ------------------------------------------------------------------
# Stage: Node.js (source for the pinned node runtime)
# ------------------------------------------------------------------
FROM node:${NODE_VERSION}-alpine AS node

# ------------------------------------------------------------------
# Stage 1: Build everything via `task build-backend-docker`
# ------------------------------------------------------------------
FROM golang:${GOLANG_VERSION}-alpine AS builder

ARG VERSION=dev
ARG COMMIT_HASH
ARG COMMIT_TIME
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG BUF_VERSION
ARG TASK_VERSION
ARG BUN_VERSION

WORKDIR /src

# Copy the pinned Node.js runtime from the node stage
COPY --from=node /usr/local/bin/node /usr/local/bin/node

# Install build tools (sqlc is installed later, from backend/go.mod)
RUN apk add --no-cache bash curl libgcc libstdc++ rsync yq && \
    wget -qO- "https://github.com/go-task/task/releases/download/v${TASK_VERSION}/task_linux_$(uname -m | sed 's/x86_64/amd64/' | sed 's/aarch64/arm64/').tar.gz" | tar -xz -C /usr/local/bin task && \
    wget -qO /usr/local/bin/buf "https://github.com/bufbuild/buf/releases/download/v${BUF_VERSION}/buf-Linux-$(uname -m)" && chmod +x /usr/local/bin/buf

# Install Bun (pinned)
RUN wget -qO- https://bun.sh/install | BUN_INSTALL=/usr bash -s "bun-v${BUN_VERSION}"

# Download Go dependencies (cache layer)
COPY go.work .
COPY backend/go.mod backend/go.sum backend/
COPY desktop/go/go.mod desktop/go/go.sum desktop/go/
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Install sqlc via backend/go.mod's `tool` directive (version pinned there).
# Running `go install` from inside the module bypasses sqlc's own replace
# directives, which a bare `go install ...@vX` would choke on.
RUN --mount=type=cache,target=/go/pkg/mod \
    cd backend && go install github.com/sqlc-dev/sqlc/cmd/sqlc

# Install frontend dependencies (cache layer)
# scripts/only-bun.mjs is needed by the preinstall hook in package.json.
COPY frontend/package.json frontend/bun.lock frontend/
COPY frontend/patches/ frontend/patches/
COPY frontend/scripts/ frontend/scripts/
RUN --mount=type=cache,target=/root/.bun/install/cache \
    cd frontend && bun install --frozen-lockfile

# Copy all remaining source
COPY . .

# Create empty spinners directory to satisfy generate-spinners status check
# (avoids git clone in Docker; spinners are optional)
RUN mkdir -p frontend/src/spinners

# Build the backend binary via task (single source of truth).
# NITRO_PRESET=node-server avoids bun preset auto-detection which hangs in buildkit.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    NITRO_PRESET=node-server VERSION=${VERSION} COMMIT_HASH=${COMMIT_HASH} COMMIT_TIME=${COMMIT_TIME} \
    task build-backend-docker GOOS=${TARGETOS} GOARCH=${TARGETARCH}

# ------------------------------------------------------------------
# Stage 2: Runtime image with s6-overlay
# ------------------------------------------------------------------
FROM ${BASE_IMAGE}

ARG S6_OVERLAY_VERSION
ARG TARGETARCH

# Install s6-overlay (Docker uses amd64/arm64 but s6-overlay releases use
# x86_64/aarch64, so we map the architecture name before downloading).
# Alpine needs ca-certificates for HTTPS; Ubuntu needs ca-certificates, wget,
# and xz-utils (Alpine's BusyBox provides wget and xz-capable tar natively).
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt,sharing=locked \
    case "${TARGETARCH}" in \
      amd64) S6_ARCH=x86_64 ;; \
      arm64) S6_ARCH=aarch64 ;; \
      *)     S6_ARCH="${TARGETARCH}" ;; \
    esac && \
    if [ -f /etc/alpine-release ]; then \
      apk add --no-cache ca-certificates; \
    else \
      apt-get update && apt-get install -y --no-install-recommends ca-certificates wget xz-utils \
      && rm -rf /var/lib/apt/lists/*; \
    fi && \
    wget -qO /tmp/s6-overlay-noarch.tar.xz \
      "https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}/s6-overlay-noarch.tar.xz" && \
    wget -qO /tmp/s6-overlay-${S6_ARCH}.tar.xz \
      "https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}/s6-overlay-${S6_ARCH}.tar.xz" && \
    tar -C / -Jxpf /tmp/s6-overlay-noarch.tar.xz && \
    tar -C / -Jxpf /tmp/s6-overlay-${S6_ARCH}.tar.xz && \
    rm /tmp/s6-overlay-*.tar.xz

# Install runtime dependencies:
#   ca-certificates  TLS certificate validation
#   tzdata           timezone data
#   git              worktree management, status, branch operations
#   bash, zsh        user-facing terminal shells
#   ncurses          terminfo database for xterm-256color
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt,sharing=locked \
    if [ -f /etc/alpine-release ]; then \
      apk add --no-cache \
        ca-certificates tzdata \
        git bash zsh ncurses; \
    else \
      apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates tzdata \
        git bash zsh ncurses-base \
      && rm -rf /var/lib/apt/lists/*; \
    fi

# Copy binary
COPY --from=builder /src/leapmux /usr/local/bin/leapmux

# Copy s6 service definitions
COPY docker/rootfs/ /

# Create data directory
RUN mkdir -p /data

EXPOSE 4327
VOLUME /data

ENV LEAPMUX_DOCKER=1

ENTRYPOINT ["/init"]
