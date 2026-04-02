# Multi-stage build for livedocs (CGO required for tree-sitter).
#
# Build:  docker build -t livedocs .
# Run:    docker run --rm livedocs version

# ---------------------------------------------------------------------------
# Stage 1: Build
# ---------------------------------------------------------------------------
FROM golang:1.25 AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
        gcc libc6-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 go build -o /livedocs ./cmd/livedocs

# ---------------------------------------------------------------------------
# Stage 2: Runtime (minimal image)
# ---------------------------------------------------------------------------
FROM alpine:3.21

# libc compatibility for CGO binary
RUN apk add --no-cache libc6-compat

COPY --from=builder /livedocs /usr/local/bin/livedocs

ENTRYPOINT ["livedocs"]
