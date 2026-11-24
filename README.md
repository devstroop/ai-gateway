# ai-gateway-go

A Go implementation of the ai-gateway service.

## Requirements

- Go 1.22+

## Getting started

```bash
go run ./cmd/server
```

The server listens on `:8080` by default. Override with the `PORT` environment variable:

```bash
PORT=9090 go run ./cmd/server
```

## Endpoints

- `GET /health` — health check returning `{"status":"ok"}`.

## Build

```bash
go build -o bin/ai-gateway-go ./cmd/server
```

## Test

```bash
go test ./...
```
## Admin store persistence

The admin store is in-memory. Admin accounts are created via the first-run setup flow at `/admin` (one-time; once an admin exists, setup is disabled). `GATEWAY_ADMIN_USERNAME`/`GATEWAY_ADMIN_PASSWORD` are no longer supported. Persistence is planned for a later phase.
