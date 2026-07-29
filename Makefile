.PHONY: build run dev stop test lint clean docker docker-run

GO ?= go
BIN := bin/whatsapp-api
AIR  ?= $(shell command -v air 2>/dev/null || echo $(GOPATH)/bin/air)

build:
	$(GO) build -o $(BIN) ./cmd/whatsapp-api

run: build
	./$(BIN)

# Kill any running whatsapp-api (or the air wrapper that spawns it) so
# you can reclaim the port without hunting PIDs. Use this when you see
# "listen tcp :8080: bind: address already in use" or want to switch
# between `make dev` and `./bin/whatsapp-api`.
stop:
	@PIDS=$$(pgrep -f 'bin/whatsapp-api' 2>/dev/null); \
	if [ -n "$$PIDS" ]; then \
		echo "Killing: $$PIDS"; \
		kill $$PIDS 2>/dev/null || true; \
		sleep 1; \
		PIDS=$$(pgrep -f 'bin/whatsapp-api' 2>/dev/null); \
		if [ -n "$$PIDS" ]; then \
			echo "Force killing: $$PIDS"; \
			kill -9 $$PIDS 2>/dev/null || true; \
		fi; \
	else \
		echo "No whatsapp-api process running."; \
	fi
	@PIDS=$$(pgrep -f '$(AIR)' 2>/dev/null); \
	if [ -n "$$PIDS" ]; then \
		echo "Also killing air wrapper: $$PIDS"; \
		kill $$PIDS 2>/dev/null || true; \
	fi

# Hot-reload dev mode. Requires `air` (go install github.com/air-verse/air@latest).
# On any change to cmd/ internal/ migrations/, air rebuilds and restarts.
dev:
	@if [ ! -x "$(AIR)" ]; then \
		echo "air not found. Install with: go install github.com/air-verse/air@latest"; \
		exit 1; \
	fi
	$(AIR) -c .air.toml

test:
	$(GO) test ./...

lint:
	$(GO) fmt ./...
	$(GO) vet ./...

# Docker targets (run from repo root so api/Dockerfile is found in context)
docker:
	docker build -t whatsmeow-api -f api/Dockerfile .

docker-run: docker
	docker run --rm -p 8080:8080 \
		-e APP_MANAGER_PASSWORD=$${APP_MANAGER_PASSWORD} \
		-v whatsmeow-data:/data \
		whatsmeow-api

clean:
	rm -rf bin data
	rm -f *.db *.db-journal
