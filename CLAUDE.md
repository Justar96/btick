# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

Real-time Bitcoin price oracle for prediction markets. Consumes WebSocket feeds from Binance, Coinbase, and Kraken, computes a multi-venue median price, and exposes REST/WebSocket APIs for settlement and live streaming.

## Build & Run

```bash
go build -o btctick ./cmd/btctick        # build binary
go run ./cmd/btctick                      # run directly
go run ./cmd/btctick -config config.yaml  # explicit config path
go test ./...                             # all tests
go test ./internal/engine/...             # single package
go test -run TestSnapshotMedian ./internal/engine/...  # single test
```

Hot reload with [air](https://github.com/air-verse/air): `air`

PostgreSQL is optional — the app runs without a database. Set `DATABASE_URL` env var or configure `database.dsn` in `config.yaml`. Config supports `${ENV_VAR}` expansion.

## Architecture

**Event pipeline (channel-based, all in goroutines):**

```
Exchange Adapters (3) → rawCh(10k) → Normalizer → normalizedCh(10k) → fan-out
                                                                        ├→ Writer → PostgreSQL
                                                                        └→ SnapshotEngine → ticks/snapshots → API + WebSocket Hub
```

Orchestrated in `cmd/btctick/main.go`.

**Key packages:**

| Package | Role |
|---------|------|
| `internal/adapter` | Per-exchange WebSocket clients with auto-reconnect (`base.go` has shared backoff logic) |
| `internal/normalizer` | Deduplication (bounded LRU), UUID v7 assignment, symbol mapping |
| `internal/engine` | `SnapshotEngine` — 1-second windowed median pricing with outlier rejection, carry-forward, quality scoring |
| `internal/storage` | pgx-based PostgreSQL: batch writer (`CopyFrom`), time-range queries, health upserts |
| `internal/api` | HTTP server + WebSocket hub; handlers read from engine channels and storage |
| `internal/domain` | Core types: `RawEvent`, `CanonicalTick`, `Snapshot1s`, `FeedHealth`, `LatestState` |
| `internal/config` | YAML loader with env var expansion and `time.Duration` converters |

**Pricing logic** (`internal/engine/snapshot.go`): Each 1-second window collects trades/quotes per venue, computes per-venue prices, takes the median across venues, rejects outliers >1% from median, and emits a `Snapshot1s`. A `CanonicalTick` is emitted only when the price changes. Carry-forward reuses the last price for up to 10 seconds if no fresh data arrives.

## API Surface

- `GET /v1/price/latest` — current price + quality
- `GET /v1/price/settlement?ts=` — price at 5-minute boundary (settlement)
- `GET /v1/price/snapshots?start=&end=` — historical 1s snapshots
- `GET /v1/price/ticks?limit=` — recent price changes
- `GET /v1/health` / `GET /v1/health/feeds` — system and per-source health
- `WS /ws/price` — live stream of `latest_price` and `snapshot_1s` messages

## Key Design Decisions

- All prices use `shopspring/decimal` — never float64 for money.
- Adapters each have distinct WebSocket protocols and auth (Coinbase needs JWT). Shared reconnection logic lives in `base.go`.
- The snapshot engine requires `minimum_healthy_sources: 2` for a "confirmed" quality score.
- Database tables: `raw_ticks` (partitioned by date), `canonical_ticks`, `snapshots_1s`, `feed_health`. Migrations in `migrations/001_init.sql`.
