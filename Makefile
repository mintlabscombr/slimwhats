.PHONY: build run test lint migrate clean docker docker-run

GO ?= go
BIN := bin/whatsapp-api

build:
	$(GO) build -o $(BIN) ./cmd/whatsapp-api

run: build
	./$(BIN)

test:
	$(GO) test ./...

lint:
	$(GO) fmt ./...
	$(GO) vet ./...

migrate: build
	./$(BIN) migrate

# Docker targets (run from repo root so api/Dockerfile is found in context)
docker:
	docker build -t whatsmeow-api -f api/Dockerfile .

docker-run: docker
	docker run --rm -p 8080:8080 \
		-e APP_MANAGER_PASSWORD=$${APP_MANAGER_PASSWORD} \
		-e APP_ENCRYPTION_KEY=$${APP_ENCRYPTION_KEY} \
		-v whatsmeow-data:/data \
		whatsmeow-api

clean:
	rm -rf bin data
	rm -f *.db *.db-journal
