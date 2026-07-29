# syntax=docker/dockerfile:1.7
# Multi-stage build for whatsmeow-api.
# Target: distroless static image, < 30 MB.
#
# Build:    docker build -t whatsmeow-api -f api/Dockerfile .
# Run:      docker run -d -p 8080:8080 \
#             -e APP_MANAGER_PASSWORD=... \
#             -e APP_ENCRYPTION_KEY=... \
#             -v whatsmeow-data:/data \
#             whatsmeow-api

# --- Builder -----------------------------------------------------------------
FROM golang:1.25-alpine AS builder
WORKDIR /src

# Cache go.mod / go.sum first
COPY api/go.mod api/go.sum ./
RUN go mod download

# Copy the rest
COPY api/ ./

# Build static binary (CGO off so we can use distroless/static)
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/whatsapp-api ./cmd/whatsapp-api

# --- Runtime -----------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

# Copy the binary and the default config (the user can override via env)
COPY --from=builder /out/whatsapp-api /app/whatsapp-api
COPY api/config.yaml /app/config.yaml

# Run as non-root (uid 65532 inside the distroless image)
USER nonroot:nonroot

# SQLite database lives here by default
VOLUME ["/data"]
WORKDIR /data

EXPOSE 8080
ENTRYPOINT ["/app/whatsapp-api"]
