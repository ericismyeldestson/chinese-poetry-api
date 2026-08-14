# Build stage. Both images are pinned by tag and digest for reproducible builds.
FROM golang:1.25.13-alpine3.23@sha256:4ce6af6747b07e99ca3a57eadb77565787390a41b0039dcc8e09ec4c57cfa125 AS builder

ARG VCS_REF=unknown

LABEL org.opencontainers.image.source="https://github.com/ericismyeldestson/chinese-poetry-api" \
      org.opencontainers.image.licenses="GPL-3.0-only" \
      org.opencontainers.image.revision="$VCS_REF"

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache \
    git=2.52.0-r0 \
    gcc=15.2.0-r2 \
    musl-dev=1.2.5-r23 \
    sqlite-dev=3.51.2-r0

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy only necessary source files
COPY cmd/ cmd/
COPY internal/ internal/

# Build the server binary with optimizations and cache
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo \
    -tags sqlite_fts5 \
    -ldflags "-extldflags '-static' -s -w" \
    -trimpath \
    -o server ./cmd/server

# Runtime stage
FROM alpine:3.22.5@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce

ARG VCS_REF=unknown

LABEL org.opencontainers.image.source="https://github.com/ericismyeldestson/chinese-poetry-api" \
      org.opencontainers.image.licenses="GPL-3.0-only" \
      org.opencontainers.image.revision="$VCS_REF"

RUN apk add --no-cache \
    ca-certificates=20260611-r0 \
    curl=8.14.1-r3 \
    gzip=1.14-r1 \
    sqlite=3.49.2-r1 \
    tzdata=2026c-r0 \
    && addgroup -g 10001 -S poetry \
    && adduser -u 10001 -S -D -H -G poetry poetry \
    && mkdir -p /app/data \
    && chown -R 10001:10001 /app

WORKDIR /app

# Copy binary, config, and startup script
COPY --link --from=builder --chown=10001:10001 --chmod=755 /build/server .
COPY --link --chown=10001:10001 --chmod=644 config.yaml .
COPY --link --chown=10001:10001 --chmod=755 scripts/startup.sh .
COPY --link --chown=0:0 --chmod=644 LICENSE NOTICE /usr/share/licenses/chinese-poetry-api/
COPY --link --chown=0:0 --chmod=644 licenses/chinese-poetry-MIT.txt /usr/share/licenses/chinese-poetry-api/
COPY --link --chown=0:0 --chmod=644 licenses/hanconv-MIT.txt licenses/opencc-dictionaries-Apache-2.0.txt /usr/share/licenses/chinese-poetry-api/
COPY --link --chown=0:0 --chmod=644 licenses/opencc-dictionaries.SHA256 /usr/share/licenses/chinese-poetry-api/
COPY --link --chown=0:0 --chmod=644 licenses/go-dependencies.csv /usr/share/licenses/chinese-poetry-api/
COPY --link --chown=0:0 licenses/go-dependencies/ /usr/share/licenses/chinese-poetry-api/go-dependencies/

# Environment variables
ENV PORT=1279 \
    GIN_MODE=release \
    DATA_DIR=data \
    DB_FILE=poetry.db \
    RATE_LIMIT_ENABLED=true \
    RATE_LIMIT_RPS=10 \
    RATE_LIMIT_BURST=20 \
    DATA_RELEASE_VERSION=v1.1.0

EXPOSE 1279

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD ["sh", "-c", "curl --fail --silent --show-error --max-time 2 \"http://127.0.0.1:${PORT}/api/v1/health\""]

USER 10001:10001

ENTRYPOINT ["./startup.sh"]
