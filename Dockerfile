FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/ai-gateway-go ./cmd/server

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/ai-gateway-go /ai-gateway-go
EXPOSE 8080
ENTRYPOINT ["/ai-gateway-go"]
