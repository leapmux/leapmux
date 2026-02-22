# syntax=docker/dockerfile:1

# Global ARGs — versions are defined in .versions (no defaults here).
ARG BASE_IMAGE
ARG BUF_VERSION

# ------------------------------------------------------------------
# Stage 1: Generate Protocol Buffer code (Go + TypeScript)
# ------------------------------------------------------------------
FROM bufbuild/buf:${BUF_VERSION} AS proto-gen
WORKDIR /src
COPY buf.yaml buf.gen.yaml ./
COPY proto/ proto/
RUN buf generate

# ------------------------------------------------------------------
# Stage 2: Build frontend (TypeScript/SolidJS via Bun)
# ------------------------------------------------------------------
FROM oven/bun:latest AS frontend-builder
WORKDIR /src/frontend

# Install dependencies first (cache layer)
COPY frontend/package.json frontend/bun.lock ./
RUN --mount=type=cache,target=/root/.bun/install/cache \
    bun install --frozen-lockfile

# Copy generated TS proto code + frontend source
COPY --from=proto-gen /src/frontend/src/generated/ src/generated/
COPY frontend/ .
RUN bun run build

# ------------------------------------------------------------------
# Stage 3: Build Go binary (cross-compiled, CGO_ENABLED=0)
# ------------------------------------------------------------------
FROM golang:1.25-alpine AS go-builder

ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG SQLC_VERSION

WORKDIR /src

# Download Go dependencies first (cache layer)
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy generated proto Go code
COPY --from=proto-gen /src/generated/ generated/

# Generate sqlc code
COPY internal/hub/sqlc.yaml internal/hub/sqlc.yaml
COPY internal/hub/db/queries/ internal/hub/db/queries/
COPY internal/hub/db/migrations/ internal/hub/db/migrations/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go install github.com/sqlc-dev/sqlc/cmd/sqlc@v${SQLC_VERSION} && \
    cd internal/hub && sqlc generate

# Rename connectrpc package (mirrors Taskfile generate-proto step)
RUN if [ -d generated/proto/leapmux/v1/leapmuxv1connect ]; then \
      mv generated/proto/leapmux/v1/leapmuxv1connect generated/proto/leapmux/v1/connectrpc && \
      sed -i 's/package leapmuxv1connect/package connectrpc/' generated/proto/leapmux/v1/connectrpc/*.go && \
      sed -i 's|leapmuxv1connect|connectrpc|g' generated/proto/leapmux/v1/connectrpc/*.go; \
    fi

# Embed frontend assets
COPY internal/hub/frontend.embed internal/hub/generated/frontend/embed.go
COPY --from=frontend-builder /src/frontend/.output/public/ internal/hub/generated/frontend/public/

# Copy remaining Go source
COPY cmd/ cmd/
COPY hub/ hub/
COPY worker/ worker/
COPY internal/ internal/

# Build statically linked binary (enables native cross-compilation)
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags "-s -w -X main.version=${VERSION}" \
    -o /leapmux ./cmd/leapmux

# ------------------------------------------------------------------
# Stage 4: Runtime image with s6-overlay
# ------------------------------------------------------------------
FROM ${BASE_IMAGE}

ARG S6_OVERLAY_VERSION
ARG TARGETARCH

# Install s6-overlay (Docker uses amd64/arm64 but s6-overlay releases use
# x86_64/aarch64, so we map the architecture name before downloading).
# Alpine needs ca-certificates for HTTPS; Ubuntu needs ca-certificates, wget,
# and xz-utils (Alpine's BusyBox provides wget and xz-capable tar natively).
RUN case "${TARGETARCH}" in \
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
RUN if [ -f /etc/alpine-release ]; then \
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
COPY --from=go-builder /leapmux /usr/local/bin/leapmux

# Copy s6 service definitions
COPY docker/rootfs/ /

# Create data directory
RUN mkdir -p /data

EXPOSE 4327
VOLUME /data

ENTRYPOINT ["/init"]
