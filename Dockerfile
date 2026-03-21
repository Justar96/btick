# Build stage
FROM golang:1.23-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /btick ./cmd/btick

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /btick /btick
COPY config.yaml.example /config.yaml
COPY migrations/ /migrations/

EXPOSE 8080

CMD ["/btick", "-config", "/config.yaml"]
