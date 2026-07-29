# whatsmeow-api

REST API on top of [`go.mau.fi/whatsmeow`](https://github.com/tulir/whatsmeow) — manage multiple WhatsApp numbers from a single process, with a web manager UI and a clean HTTP surface for sending text + interactive buttons.

This service lives inside the whatsmeow repository as a `git subdir` module. It is a downstream consumer of whatsmeow; the upstream library code is untouched.

See [`../specs/prd-whatsapp-rest-api.md`](../specs/prd-whatsapp-rest-api.md) for the full PRD (31 user stories, 38 functional requirements, 0 open questions).

## Quickstart

### 1. Required environment variables

```bash
# Required: manager panel password (any non-empty string)
export APP_MANAGER_PASSWORD="your-secure-password"

# Required: encryption key for webhook secrets (32 raw bytes, base64-encoded)
# Generate with:
export APP_ENCRYPTION_KEY="$(openssl rand -base64 32)"

# Optional: HTTP listen address (default ":8080")
export APP_HTTP_ADDR=":8080"

# Optional: manager username shown on the login form (default "admin")
export APP_MANAGER_USERNAME="admin"

# Optional: database (default SQLite at data/whatsmeow-api.db)
# To use Postgres instead:
# export APP_DB_DRIVER="postgres"
# export APP_DB_DSN="postgres://user:pass@host:5432/whatsmeow-api?sslmode=disable"
```

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

US-001 (project skeleton + config loader) is shipped. See `../kanban.md` and `../PROGRESS.md` for the full plan and progress.
