.PHONY: build test lint run

build:
	go build -o bin/ai-gateway ./cmd/server

test:
	go test ./...

lint:
	golangci-lint run

run:
	go run ./cmd/server
