.PHONY: build run dev test lint clean docker docker-run

GO ?= go
BIN := bin/whatsapp-api
AIR  ?= $(shell command -v air 2>/dev/null || echo $(GOPATH)/bin/air)

build:
	$(GO) build -o $(BIN) ./cmd/whatsapp-api

run: build
	./$(BIN)

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
