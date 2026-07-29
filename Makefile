.PHONY: build run test lint migrate clean

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

# Placeholder — real migration wiring lands in US-002.
migrate: build
	./$(BIN) migrate

clean:
	rm -rf bin data
	rm -f *.db *.db-journal
