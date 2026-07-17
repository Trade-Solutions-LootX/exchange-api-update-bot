# exchange-api-update-bot — developer tasks
# Zero third-party Go deps: `go build`/`go test` need no network.

BINARY   := bot
PKG      := ./...
VERSION  := $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)

.PHONY: all build run test vet fmt tidy lint cover docker up down logs clean

all: fmt vet test build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY) ./cmd/bot

run:
	go run ./cmd/bot

# Quick local smoke test: prints messages to stdout instead of Telegram.
dry-run:
	DRY_RUN=true LOG_LEVEL=debug SEND_HISTORY_ON_START=true HISTORY_COUNT=1 go run ./cmd/bot

test:
	go test $(PKG)

race:
	go test -race $(PKG)

vet:
	go vet $(PKG)

fmt:
	gofmt -w .

cover:
	go test -coverprofile=coverage.out $(PKG)
	go tool cover -func=coverage.out | tail -1

docker:
	docker build --build-arg VERSION=$(VERSION) -t exchange-api-update-bot:$(VERSION) -t exchange-api-update-bot:latest .

up:
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f --tail=100

clean:
	rm -rf bin coverage.out
