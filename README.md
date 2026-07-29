# whatsmeow-api

REST API on top of [`go.mau.fi/whatsmeow`](https://github.com/tulir/whatsmeow) — manage multiple WhatsApp numbers from a single process, with a web manager UI and a clean HTTP surface for sending text + interactive buttons.

This service lives inside the whatsmeow repository as a `git subdir` module. It is a downstream consumer of whatsmeow; the upstream library code is untouched.

See [`../specs/prd-whatsapp-rest-api.md`](../specs/prd-whatsapp-rest-api.md) for the full PRD (31 user stories, 38 functional requirements, 0 open questions).

## Quickstart

### 1. Required environment variables

```bash
# Required: manager panel password (any non-empty string)
export APP_MANAGER_PASSWORD="your-secure-password"

# Optional: HTTP listen address (default ":8080")
export APP_HTTP_ADDR=":8080"

# Optional: manager username shown on the login form (default "admin")
export APP_MANAGER_USERNAME="admin"

# Optional: database (default SQLite at data/whatsmeow-api.db)
# To use Postgres instead:
# export APP_DB_DRIVER="postgres"
# export APP_DB_DSN="postgres://user:pass@host:5432/whatsmeow-api?sslmode=disable"
```

> **Note:** `APP_ENCRYPTION_KEY` is no longer required (v1 simplification — webhook secrets are stored as plaintext in the DB; see `PROGRESS.md`). If your `.env` still has the line, it's ignored. The service logs a one-time warning if the key is missing.

### 2. Build and run

```bash
make build
make run
```

### 3. Open the manager

Visit <http://localhost:8080/admin>. Log in with `APP_MANAGER_USERNAME` and `APP_MANAGER_PASSWORD`.

The `/healthz` endpoint is unauthenticated and returns `200 {"status":"ok"}` for liveness probes.

## Layout

```
api/
├── cmd/whatsapp-api/   # main entry point (boot + signal handling)
├── internal/config/     # yaml + env config loader, env validation
├── config.yaml          # non-secret defaults (env vars override)
├── go.mod               # module: github.com/mauroneto/whatsmeow-api
├── Makefile             # build / run / test / lint / migrate
├── .gitignore
└── README.md
```

Future US will populate `internal/auth/`, `internal/store/`, `internal/instance/`, `internal/webhook/`, etc.

## Development

```bash
make test    # run unit tests
make lint    # gofmt + go vet
make clean   # remove build artifacts and local DB files
```

## Status

All 31 user stories from the PRD are shipped. See `../kanban.md` and `../PROGRESS.md` for the full plan and per-US shipping log.

## Docker

The repo ships a multi-stage `Dockerfile` and a `docker-compose.yml` example.

### Build the image

```bash
make docker          # from the api/ directory
# or, from the repo root:
docker build -t whatsmeow-api -f api/Dockerfile .
```

The build is two stages:

1. `golang:1.25-alpine` — compiles a static binary with `CGO_ENABLED=0` and `-ldflags="-s -w"`
2. `gcr.io/distroless/static-debian12:nonroot` — copies the binary, runs as uid 65532

Resulting image size: ~20 MB (target was < 30 MB).

### Run with docker compose (recommended)

```bash
cp .env.example .env       # then edit to set APP_MANAGER_PASSWORD
docker compose up -d
docker compose logs -f
```

The default compose file mounts a `whatsmeow-data` volume at `/data` so the SQLite database survives container restarts.

To switch to PostgreSQL, uncomment the `postgres` service in `docker-compose.yml` and set:

```bash
APP_DB_DRIVER=postgres
APP_DB_DSN=postgres://whatsmeow:whatsmeow@postgres:5432/whatsmeow?sslmode=disable
```

### Run the image directly

```bash
docker run --rm -p 8080:8080 \
    -e APP_MANAGER_PASSWORD=your-password \
    -v whatsmeow-data:/data \
    whatsmeow-api
```

The service answers on `http://localhost:8080`. `/healthz` is unauthenticated and returns 200 for liveness probes (from the host, since the distroless image has no curl).
