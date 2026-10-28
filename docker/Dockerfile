# syntax=docker/dockerfile:1

# Global ARGs. versions.env defines the versions, so no ARG has a default here.
ARG BASE_IMAGE
ARG GOLANG_VERSION
ARG NODE_VERSION

# ------------------------------------------------------------------
# Stage: Node.js (source for the pinned node runtime)
# ------------------------------------------------------------------
FROM --platform=${BUILDPLATFORM} node:${NODE_VERSION}-alpine AS node

# ------------------------------------------------------------------
# Stage: tools (the pinned build toolchain)
# ------------------------------------------------------------------
FROM --platform=${BUILDPLATFORM} golang:${GOLANG_VERSION}-alpine AS tools

# BusyBox ash runs each RUN below. `pipefail` makes a failed download fail
# the step. Without it, `wget ... | bash` ran an empty script and hid the
# failure of the Bun installer download. `wget | tar` was already safe,
# because tar reads to EOF and fails on a truncated archive.
SHELL ["/bin/ash", "-eo", "pipefail", "-c"]

ARG BUF_VERSION
ARG TASK_VERSION
ARG BUN_VERSION

WORKDIR /src

# Copy the pinned Node.js runtime from the node stage
COPY --from=node /usr/local/bin/node /usr/local/bin/node

# Git supplies the spinner sources that Task requires.
# Go tool dependencies reside in backend/go.mod and desktop/go/go.mod.
# Task invokes them through go tool, so the build needs no separate go install step.
RUN apk add --no-cache bash curl git libgcc libstdc++ rsync && \
    wget -qO- "https://github.com/go-task/task/releases/download/v${TASK_VERSION}/task_linux_$(uname -m | sed 's/x86_64/amd64/' | sed 's/aarch64/arm64/').tar.gz" | tar -xz -C /usr/local/bin task && \
    wget -qO /usr/local/bin/buf "https://github.com/bufbuild/buf/releases/download/v${BUF_VERSION}/buf-Linux-$(uname -m)" && chmod +x /usr/local/bin/buf

# Install Bun (pinned)
RUN wget -qO- https://bun.sh/install | BUN_INSTALL=/usr bash -s "bun-v${BUN_VERSION}"

# ------------------------------------------------------------------
# Stage: build everything via `task build-backend-docker`
# ------------------------------------------------------------------
FROM tools AS builder

# Copy all remaining source
COPY . .

# BuildKit passes every ARG to each later RUN. A per-commit value must
# therefore come after the toolchain layers above: a declaration before
# them runs the tool installation again on every commit.
ARG VERSION=dev
ARG COMMIT_HASH
ARG COMMIT_TIME
# Import BuildKit's target values without defaults that override --platform.
ARG TARGETOS
ARG TARGETARCH

# Build the backend binary via task (single source of truth).
# Build tools run natively because Bun's allocator fails under x64 QEMU on ARM.
# Only the final Go compilation uses the requested target platform.
# NITRO_PRESET=node-server avoids bun preset auto-detection which hangs in buildkit.
#
# Local builds retain these cache mounts. CI supplies the Go module mount through
# buildkit-cache-dance. The GitHub Actions exporter does not save cache mounts.
# See https://docs.docker.com/build/ci/github-actions/cache/#cache-mounts.
# CI does not supply a Bun mount. A cold Bun install took 3.7 seconds, less than the cache-dance
# build.
# Task downloads both dependency sets into the mounts below. Separate download layers are
# unnecessary.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/root/.bun/install/cache \
    NITRO_PRESET=node-server VERSION=${VERSION} COMMIT_HASH=${COMMIT_HASH} COMMIT_TIME=${COMMIT_TIME} \
    task build-backend-docker GOOS=${TARGETOS} GOARCH=${TARGETARCH}

# ------------------------------------------------------------------
# Stage 2: Runtime image with s6-overlay
# ------------------------------------------------------------------
FROM ${BASE_IMAGE}

ARG S6_OVERLAY_VERSION
ARG TARGETARCH

# Install s6-overlay. Docker uses amd64/arm64, but the s6-overlay releases
# use x86_64/aarch64, so the script maps the architecture name first.
# Alpine needs ca-certificates for HTTPS. Ubuntu needs these packages:
# - ca-certificates.
# - wget.
# - xz-utils.
# Alpine BusyBox already supplies wget and xz-capable tar.
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
# - ca-certificates supplies TLS certificate validation.
# - tzdata supplies timezone data.
# - git supports worktrees and branches. It reports repository status also.
# - bash and zsh supply terminal shells.
# - ncurses supplies the xterm-256color terminfo entry.
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

# Copy the startup script. s6 runs it as CMD and preserves its exit status.
COPY --chmod=0755 docker/rootfs/usr/local/bin/leapmux-start /usr/local/bin/leapmux-start

# Create data directory
RUN mkdir -p /data

EXPOSE 4327
VOLUME /data

ENTRYPOINT ["/init"]
CMD ["/usr/local/bin/leapmux-start"]
