# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS base
WORKDIR /src
RUN apk add --no-cache ca-certificates tzdata
COPY go.mod go.sum ./
RUN go mod download

FROM base AS dev
WORKDIR /app
COPY . .
ENV CONFIG_PATH=/app/config.example.toml
ENV GATEWAY_DB_PATH=/tmp/gateway.db
EXPOSE 8080
CMD ["go", "run", "./cmd/server"]

FROM base AS build
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/ai-gateway ./cmd/server

FROM gcr.io/distroless/static-debian12 AS runtime
COPY --from=build /out/ai-gateway /ai-gateway
EXPOSE 8080
ENTRYPOINT ["/ai-gateway"]