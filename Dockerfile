# syntax=docker/dockerfile:1.7
# Multi-stage build for slimwhats.
# Target: distroless static image, < 30 MB.
#
# Build:    docker build -t slimwhats -f api/Dockerfile .
# Run:      docker run -d -p 8080:8080 \
#             -e APP_MANAGER_PASSWORD=... \
#             -e APP_ENCRYPTION_KEY=... \
#             -v whatsmeow-data:/data \
#             slimwhats

# --- CSS builder --------------------------------------------------------------
# Generates api/internal/handlers/static/app.css from src.css using the
# Tailwind standalone CLI. The output is copied into the Go source tree
# before `go build` so `//go:embed` picks it up. The Tailwind binary is
# not shipped in the runtime image.
FROM alpine:3.20 AS css-builder
ARG TAILWIND_VERSION=v3.4.17
RUN apk add --no-cache curl ca-certificates \
    && curl -fsSL -o /usr/local/bin/tailwindcss \
       https://github.com/tailwindlabs/tailwindcss/releases/download/${TAILWIND_VERSION}/tailwindcss-linux-x64 \
    && chmod +x /usr/local/bin/tailwindcss
WORKDIR /css
COPY api/internal/handlers/static/src.css ./src.css
RUN tailwindcss -i ./src.css -o ./app.css --minify

# --- Builder ------------------------------------------------------------------
FROM golang:1.25-alpine AS builder
WORKDIR /src

# Cache go.mod / go.sum first
COPY api/go.mod api/go.sum ./
RUN go mod download

# Copy the rest
COPY api/ ./

# Inject the pre-built CSS so `//go:embed` finds a non-empty app.css.
# src.css is the human-edited source; app.css is the generated output
# (gitignored in the repo, produced here from the CSS stage).
COPY --from=css-builder /css/app.css ./internal/handlers/static/app.css

# Build static binary (CGO off so we can use distroless/static)
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/slimwhats ./cmd/slimwhats

# --- Runtime -----------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

# Copy the binary and the default config (the user can override via env)
COPY --from=builder /out/slimwhats /app/slimwhats
COPY api/config.yaml /app/config.yaml

# Run as non-root (uid 65532 inside the distroless image)
USER nonroot:nonroot

# SQLite database lives here by default
VOLUME ["/data"]
WORKDIR /data

EXPOSE 8080
ENTRYPOINT ["/app/slimwhats"]
