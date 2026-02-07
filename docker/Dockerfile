# ------------------------------------------------------------------
# Stage 1: Generate Protocol Buffer code (Go + TypeScript)
# ------------------------------------------------------------------
FROM bufbuild/buf:latest AS proto-gen
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
RUN bun install --frozen-lockfile

# Copy generated TS proto code + frontend source
COPY --from=proto-gen /src/frontend/src/generated/ src/generated/
COPY frontend/ .
RUN bun run build

# ------------------------------------------------------------------
# Stage 3: Build Go binary (cross-compiled, CGO_ENABLED=0)
# ------------------------------------------------------------------
FROM golang:1.24-alpine AS go-builder

ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /src

# Download Go dependencies first (cache layer)
COPY go.mod go.sum ./
RUN go mod download

# Copy generated proto Go code
COPY --from=proto-gen /src/generated/ generated/

# Generate sqlc code
COPY internal/hub/sqlc.yaml internal/hub/sqlc.yaml
COPY internal/hub/db/queries/ internal/hub/db/queries/
COPY internal/hub/db/migrations/ internal/hub/db/migrations/
RUN go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest && \
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
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags "-s -w -X main.version=${VERSION}" \
    -o /leapmux ./cmd/leapmux

# ------------------------------------------------------------------
# Stage 4: Runtime image with s6-overlay
# ------------------------------------------------------------------
ARG BASE_IMAGE=alpine:3.21
FROM ${BASE_IMAGE}

ARG S6_OVERLAY_VERSION=3.2.0.2
ARG TARGETARCH=amd64

# Install s6-overlay
ADD https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}/s6-overlay-noarch.tar.xz /tmp/
ADD https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}/s6-overlay-${TARGETARCH}.tar.xz /tmp/
RUN tar -C / -Jxpf /tmp/s6-overlay-noarch.tar.xz && \
    tar -C / -Jxpf /tmp/s6-overlay-${TARGETARCH}.tar.xz && \
    rm /tmp/s6-overlay-*.tar.xz

# Install minimal runtime deps (ca-certificates for TLS, tzdata for timezones)
RUN apk add --no-cache ca-certificates tzdata

# Copy binary
COPY --from=go-builder /leapmux /usr/local/bin/leapmux

# Copy s6 service definitions
COPY docker/rootfs/ /

# Create data directory
RUN mkdir -p /data

EXPOSE 4327
VOLUME /data

ENTRYPOINT ["/init"]
