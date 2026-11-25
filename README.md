# ai-gateway

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
- `GET /ready` — readiness check.
- `GET /v1/models` — list advertised models.
- `POST /v1/chat/completions` — chat completions (streaming supported).
- `POST /v1/embeddings` — embeddings.
- `POST /v1/messages` — Anthropic messages (native or bridged).
- `POST /v1/responses` — responses API (native or bridged).
- `GET /v1/auth/whoami` — identity for the supplied API key.
- `GET /api/admin/setup/status` — whether first-run setup is required.
- `POST /api/admin/setup` — create the initial admin (one-time).
- `POST /api/admin/auth/login` — admin login (sets session cookie).
- `GET /api/admin/auth/session` — current admin session.
- `POST /api/admin/auth/logout` — admin logout.
- `GET|PUT|DELETE /api/admin/runtime` — runtime settings (default model).
- `GET|POST /api/admin/users` — list / create users (admin only).

## Build

```bash
go build -o bin/ai-gateway ./cmd/server
```

## Test

```bash
go test ./...
```
## Admin store persistence

The admin store is in-memory. Admin accounts are created via the first-run setup flow at `/admin` (one-time; once an admin exists, setup is disabled). `GATEWAY_ADMIN_USERNAME`/`GATEWAY_ADMIN_PASSWORD` are no longer supported. Persistence is planned for a later phase.


## Frontend

The `web/` directory is a Vite + React 19 app built on the Astryx design system, with
client-side data fetching (TanStack Query) against the Go API.

```bash
cd web
npm install
npm run dev   # http://localhost:5173, proxies /health, /v1, /api to :8080
npm run build # outputs to web/dist
```

The Go server stays a pure JSON API; run it separately (`go run ./cmd/server`) during development.


## Tests

```bash
go test ./...
cd web && npm run test
```
