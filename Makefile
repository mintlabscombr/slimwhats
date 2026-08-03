.PHONY: build run dev stop test lint clean docker docker-run setup css-build css-watch render-openapi update-whatsmeow

GO ?= go
BIN := bin/slimwhats
AIR  ?= $(shell command -v air 2>/dev/null || echo $(GOPATH)/bin/air)

# --- Tailwind (F-02 / US-032) -------------------------------------------------
# Standalone CLI binary — no Node toolchain required. Downloaded by `make setup`.
# Version pinned for reproducibility; bump on a deliberate upgrade.
TAILWIND_VERSION ?= v3.4.17
TAILWIND_BIN     := bin/tailwindcss
# Map uname output → Tailwind release asset suffix (macos-x64, linux-arm64, …)
# uname -s  → "Darwin" / "Linux"; Tailwind uses "macos" for the former.
# uname -m  → "x86_64" / "aarch64" / "arm64" / "armv7l"; mapped to Tailwind's
#             "x64" / "arm64" / "arm64" / "armv7" suffixes.
TAILWIND_PLATFORM := $(shell uname -s | tr A-Z a-z | sed 's/^darwin$$/macos/')-$(shell uname -m | sed 's/x86_64/x64/;s/aarch64/arm64/;s/armv7l/armv7/')

CSS_SRC := internal/handlers/static/src.css
CSS_OUT := internal/handlers/static/app.css

# --- OpenAPI (APP_URL substitution) -------------------------------------------
# Source spec lives at internal/handlers/openapi.yaml with a __APP_URL__
# placeholder. We render openapi.gen.yaml with APP_URL substituted in so
# `//go:embed` (in internal/handlers/swagger.go) bakes the right server
# URL into the binary. Mirrors how `app.css` is generated from `src.css`.
OPENAPI_SRC := internal/handlers/openapi.yaml
OPENAPI_GEN := internal/handlers/openapi.gen.yaml
# Resolution order for APP_URL: 1) make-time override (APP_URL=… make …),
# 2) env var already exported, 3) .env file, 4) dev default. The recipe
# (not `$(shell)`) does the resolution so it runs at recipe time — that
# way `make render-openapi APP_URL=https://…` actually re-renders
# instead of being short-circuited by Make's file-up-to-date check.
APP_URL_DEFAULT := http://localhost:8080

setup: $(TAILWIND_BIN)

$(TAILWIND_BIN):
	@echo "Downloading tailwindcss $(TAILWIND_VERSION) for $(TAILWIND_PLATFORM)..."
	@mkdir -p bin
	@curl -fsSL -o $(TAILWIND_BIN) \
		https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-$(TAILWIND_PLATFORM)
	@chmod +x $(TAILWIND_BIN)
	@$(TAILWIND_BIN) --help >/dev/null
	@echo "Installed $$($(TAILWIND_BIN) --help 2>&1 | head -1)"

css-build: $(TAILWIND_BIN)
	@mkdir -p $$(dirname $(CSS_OUT))
	$(TAILWIND_BIN) -i $(CSS_SRC) -o $(CSS_OUT) --minify

# Dev-only: rebuild CSS on every save. Run in a parallel terminal next to
# `make dev` for live CSS reload; `make dev` itself runs a one-shot
# `make css-build` via the air pre_cmd (so even without css-watch the
# embedded CSS is fresh on every Go change).
css-watch: $(TAILWIND_BIN)
	$(TAILWIND_BIN) -i $(CSS_SRC) -o $(CSS_OUT) --watch

# Render openapi.gen.yaml from openapi.yaml with APP_URL substituted.
# Always re-runs — the .PHONY rule below bypasses Make's
# up-to-date check so `make render-openapi APP_URL=https://x` works.
.PHONY: render-openapi
render-openapi: $(OPENAPI_SRC) Makefile
	@APP_URL=$${APP_URL:-$$([ -f .env ] && grep -E '^APP_URL=' .env | head -1 | sed 's/^APP_URL=//')}; \
	APP_URL=$${APP_URL:-$(APP_URL_DEFAULT)}; \
	echo "Rendering $(OPENAPI_GEN) with APP_URL=$$APP_URL"; \
	sed "s|__APP_URL__|$$APP_URL|g" $(OPENAPI_SRC) > $(OPENAPI_GEN)
	@touch $(OPENAPI_GEN)

# --- Build --------------------------------------------------------------------
build: css-build render-openapi
	$(GO) build -o $(BIN) ./cmd/slimwhats

run: build
	./$(BIN)

# Kill any running slimwhats (or the air wrapper that spawns it) so
# you can reclaim the port without hunting PIDs. Use this when you see
# "listen tcp :8080: bind: address already in use" or want to switch
# between `make dev` and `./bin/slimwhats`.
stop:
	@PIDS=$$(pgrep -f 'bin/slimwhats' 2>/dev/null); \
	if [ -n "$$PIDS" ]; then \
		echo "Killing: $$PIDS"; \
		kill $$PIDS 2>/dev/null || true; \
		sleep 1; \
		PIDS=$$(pgrep -f 'bin/slimwhats' 2>/dev/null); \
		if [ -n "$$PIDS" ]; then \
			echo "Force killing: $$PIDS"; \
			kill -9 $$PIDS 2>/dev/null || true; \
		fi; \
	else \
		echo "No slimwhats process running."; \
	fi
	@PIDS=$$(pgrep -f '$(AIR)' 2>/dev/null); \
	if [ -n "$$PIDS" ]; then \
		echo "Also killing air wrapper: $$PIDS"; \
		kill $$PIDS 2>/dev/null || true; \
	fi

# Hot-reload dev mode. Requires `air` (go install github.com/air-verse/air@latest)
# and the Tailwind binary (`make setup` to download). For live CSS changes
# on save, also run `make css-watch` in a parallel terminal — the air
# pre_cmd only rebuilds CSS on Go changes.
dev:
	@if [ ! -x "$(AIR)" ]; then \
		echo "air not found. Install with: go install github.com/air-verse/air@latest"; \
		exit 1; \
	fi
	@if [ ! -x "$(TAILWIND_BIN)" ]; then \
		echo "tailwindcss not found. Run: make setup"; \
		exit 1; \
	fi
	$(AIR) -c .air.toml

test:
	$(GO) test ./...

# whatsmeow ships no tagged releases, so @latest resolves to the newest
# commit on its default branch. Run this periodically to pick it up.
update-whatsmeow:
	$(GO) get -u go.mau.fi/whatsmeow@latest
	$(GO) mod tidy

lint:
	$(GO) fmt ./...
	$(GO) vet ./...

# Docker targets (run from repo root)
docker:
	docker build -t slimwhats -f Dockerfile .

docker-run: docker
	docker run --rm -p 8080:8080 \
		-e APP_MANAGER_PASSWORD=$${APP_MANAGER_PASSWORD} \
		-v whatsmeow-data:/data \
		slimwhats

clean:
	rm -rf bin data
	rm -f *.db *.db-journal
	rm -f internal/handlers/static/app.css
	rm -f $(OPENAPI_GEN)
