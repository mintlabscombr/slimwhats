# syntax=docker/dockerfile:1.7
# Multi-stage build for slimwhats.
# Target: distroless static image, < 30 MB.
#
# Build:    docker build -t slimwhats -f Dockerfile .
#               --build-arg APP_URL=https://slimwhats.example.com
# Run:      docker run -d -p 8080:8080 \
#             -e APP_MANAGER_PASSWORD=... \
#             -e APP_URL=https://slimwhats.example.com \
#             -v whatsmeow-data:/data \
#             slimwhats
#
# APP_URL is the public URL the service is reachable at. It's baked
# into the embedded OpenAPI spec at build time (--build-arg APP_URL=…)
# and also read at runtime as a regular env var so the same binary
# works across deploys without rebuilding.

# --- CSS builder --------------------------------------------------------------
# Generates internal/handlers/static/app.css from src.css using the
# Tailwind standalone CLI. The output is copied into the Go source tree
# before `go build` so `//go:embed` picks it up. The Tailwind binary is
# not shipped in the runtime image.
FROM alpine:3.20 AS css-builder
ARG TARGETARCH
ARG TAILWIND_VERSION=v3.4.17
RUN case "$TARGETARCH" in \
      amd64) TW_ARCH=x64 ;; \
      arm64) TW_ARCH=arm64 ;; \
      *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac \
    && apk add --no-cache curl ca-certificates \
    && curl -fsSL -o /usr/local/bin/tailwindcss \
       https://github.com/tailwindlabs/tailwindcss/releases/download/${TAILWIND_VERSION}/tailwindcss-linux-${TW_ARCH} \
    && chmod +x /usr/local/bin/tailwindcss
WORKDIR /css
COPY tailwind.config.js ./tailwind.config.js
COPY internal/ ./internal/
COPY internal/handlers/static/src.css ./src.css
RUN tailwindcss -i ./src.css -o ./app.css --minify

# --- Builder ------------------------------------------------------------------
FROM golang:1.25-alpine AS builder
WORKDIR /src

# Public URL to bake into the OpenAPI spec (overridable at build time
# via --build-arg APP_URL=…). Falls back to the dev default if unset.
# The source spec at internal/handlers/openapi.yaml carries a
# __APP_URL__ placeholder; the sed below substitutes it before
# `//go:embed` packages the file into the binary.
ARG APP_URL=http://localhost:8080

# Cache go.mod / go.sum first
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest
COPY . .

# Inject the pre-built CSS so `//go:embed` finds a non-empty app.css.
# src.css is the human-edited source; app.css is the generated output
# (gitignored in the repo, produced here from the CSS stage).
COPY --from=css-builder /css/app.css ./internal/handlers/static/app.css

# Render the OpenAPI spec with APP_URL substituted. Mirrors the
# Makefile `render-openapi` target so the two build paths produce
# identical output.
RUN sed "s|__APP_URL__|${APP_URL}|g" \
        internal/handlers/openapi.yaml \
        > internal/handlers/openapi.gen.yaml

# Build static binary (CGO off so we can use distroless/static)
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/slimwhats ./cmd/slimwhats

# --- Runtime -----------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

# Copy the binary and the default config (the user can override via env)
COPY --from=builder /out/slimwhats /app/slimwhats
COPY config.yaml /app/config.yaml

# Run as non-root (uid 65532 inside the distroless image)
USER nonroot:nonroot

# SQLite database lives here by default
VOLUME ["/data"]
WORKDIR /data

EXPOSE 8080
ENTRYPOINT ["/app/slimwhats"]
