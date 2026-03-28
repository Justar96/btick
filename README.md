# btick

Manipulation-resistant BTC/USD price from Binance, Coinbase, Kraken, and OKX. Built for prediction market settlement.

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-14+-336791?style=flat&logo=postgresql)](https://www.postgresql.org/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Median across venues, 1-second snapshots, sub-second WebSocket updates, outlier rejection. No API keys needed — all public feeds.

## Get running

```bash
git clone https://github.com/justar9/btick.git && cd btick
cp config.yaml.example config.yaml   # edit DATABASE_URL or leave empty (postgres is optional)
go run ./cmd/btick
```

```bash
# check it works
curl localhost:8080/v1/health
curl localhost:8080/v1/price/latest
wscat -c ws://localhost:8080/ws/price
```

## API at a glance

```
GET  /v1/price/latest              current canonical price
GET  /v1/price/settlement?ts=      price at 5-min boundary
GET  /v1/price/snapshots?start=&end=
GET  /v1/price/ticks?limit=
GET  /v1/health
GET  /v1/health/feeds
WS   /ws/price                     live stream (subscribe/unsubscribe filtering)
```

Full reference, examples, and OpenAPI spec in [docs/API.md](docs/API.md) and [docs/openapi.yaml](docs/openapi.yaml).

## How it works

```
Binance ──┐
Coinbase ─┼┐
Kraken ───┼┼→ Normalizer → SnapshotEngine (1s median) → REST + WebSocket
OKX ──────┘┘                      ↓
                             PostgreSQL
```

Each second: collect per-venue prices, take the median, reject outliers >1% off, emit a snapshot. A canonical tick fires only when the price changes. Carry-forward fills gaps up to 10s.

Everything is channel-based goroutines with non-blocking sends — drops before it blocks.

Details in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Dev

```bash
go test ./...                          # all tests
go test -run TestName ./internal/pkg/  # one test
go build -o btick ./cmd/btick         # build
air                                    # hot reload (needs air installed)
```

## Deploy

```bash
railway up                                              # railway
docker build -t btick . && docker run -e DATABASE_URL=... -p 8080:8080 btick  # docker
```

Production setup, env vars, and Railway config in [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md).

## Docs

- [API.md](docs/API.md) — endpoints, examples, WebSocket protocol
- [ARCHITECTURE.md](docs/ARCHITECTURE.md) — pipeline, concurrency, design decisions
- [DATABASE.md](docs/DATABASE.md) — schema, queries, TimescaleDB setup
- [DEPLOYMENT.md](docs/DEPLOYMENT.md) — production deployment
- [openapi.yaml](docs/openapi.yaml) — OpenAPI 3.1 spec

## License

[MIT](LICENSE)
