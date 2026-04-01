# CLAUDE.md — btick

## Project Overview

btick is a **Bitcoin price oracle service** for prediction market settlement. It aggregates real-time BTC/USD price data from four exchanges (Binance, Coinbase, Kraken, OKX) and produces a canonical, manipulation-resistant median price with sub-second precision.

**Tech stack:** Go 1.23, PostgreSQL 17+ with TimescaleDB 2.24+, gorilla/websocket, pgx/v5, shopspring/decimal.

## Quick Reference Commands

```bash
# Build & run
go build -o btick ./cmd/btick
go run ./cmd/btick
go run ./cmd/btick -config custom.yaml

# Tests
go test ./...                          # All tests
go test -v ./internal/engine/          # Verbose, single package
go test -run TestFoo ./internal/pkg/   # Single test

# Docker
docker build -t btick .
docker run -e DATABASE_URL=... -p 8080:8080 btick
```

## Project Structure

```
cmd/btick/main.go          # Entry point, goroutine orchestration, graceful shutdown
internal/
  adapter/                  # Exchange WebSocket connectors (Binance, Coinbase, Kraken, OKX)
    base.go                 # BaseAdapter: connection lifecycle, reconnect with backoff
  api/                      # REST + WebSocket API server
    handlers.go             # REST endpoints: /v1/price/latest, /v1/snapshots, /v1/ticks, /v1/health
    websocket.go            # WebSocket broadcast at /ws/price
  config/                   # YAML config loading with env var expansion
  domain/                   # Immutable domain types (RawEvent, CanonicalTick, Snapshot1s, etc.)
  engine/                   # Core pricing logic: median calculation, outlier rejection, snapshots
    snapshot.go             # SnapshotEngine: 1s snapshots, carry-forward, canonical tick generation
    persistence.go          # Tick/snapshot DB persistence
  normalizer/               # Event deduplication (sync.Map sharded by source), UUID v7 assignment
  storage/                  # PostgreSQL/TimescaleDB layer (pgx, no ORM)
    postgres.go             # Pool creation, migrations
    queries.go              # Read queries
    writer.go               # Batch writes (raw events, ticks, snapshots)
    pruner.go               # Data retention cleanup
  metrics/                  # In-memory Prometheus-compatible metrics at /metrics
migrations/
  001_init.sql              # Idempotent schema: raw_ticks, canonical_ticks, snapshots_1s hypertables
docs/                       # API.md, ARCHITECTURE.md, DATABASE.md, DEPLOYMENT.md, OPTIMIZATION.md, openapi.yaml
```

## Architecture & Data Flow

```
Exchanges → Adapters → Normalizer → Fan-out → Engine → API/WebSocket
                                       ↓         ↓
                                     Writer    Writer
                                       ↓         ↓
                                   PostgreSQL (TimescaleDB)
```

- Each adapter runs in its own goroutine with auto-reconnect and exponential backoff.
- Normalizer deduplicates by `(source, trade_id)` and assigns UUID v7 IDs.
- Fan-out uses **non-blocking channel sends** — full channels drop events rather than block.
- Engine produces 1-second snapshots with median price from healthy sources.
- Canonical ticks are emitted only on price changes.

## Key Conventions

### Code Style
- **Standard Go conventions**: `gofmt`, `go vet`, idiomatic naming (CamelCase types, camelCase unexported).
- **Short variable names**: `evt`, `cfg`, `db`, `mu`, `wg`, `ctx`.
- **Packages**: lowercase, single word (`adapter`, `engine`, `domain`).
- **Interfaces**: named with `-er` suffix or explicit nouns (`Store`, `MessageHandler`); defined on consumer side.

### Error Handling
- Every error is explicitly checked: `if err != nil { ... }`.
- Errors are wrapped with context: `fmt.Errorf("context: %w", err)`.
- Structured logging on errors via `slog` with relevant fields.

### Logging
- Framework: `log/slog` with structured JSON output in production.
- Levels: Debug, Info, Warn, Error.
- Add context with `.With()`: component, source, error details.

### Testing
- Go standard `testing` package only — no external test frameworks.
- Table-driven tests are preferred.
- Mocks defined locally in test files (e.g., `mockStore`).
- Test helpers use `slog.NewTextHandler` at ErrorLevel to suppress noise.
- Use `httptest` for HTTP handler tests.
- ~188 test functions across all packages.

### Database
- **Direct pgx** — no ORM.
- **Split connection pools**: ingest pool (writes, 12 conns) and query pool (reads, 8 conns).
- **Idempotent migrations**: `IF NOT EXISTS`, `DO $$ ... EXCEPTION` blocks.
- **TimescaleDB hypertables** with automatic compression policies.
- **shopspring/decimal** for all price arithmetic — never use `float64` for money.

### Concurrency
- `safeGo()` wrapper for goroutines: panic recovery + graceful shutdown.
- Channels are non-blocking (drop on full, never block producers).
- Drop counters logged every 1000 events.
- Context cancellation propagates shutdown through all goroutines.

## Configuration

Config loaded from `config.yaml` (see `config.yaml.example`). Environment variable expansion supported via `${VAR}` syntax. `DATABASE_URL` env var overrides config DSN.

Key sections: `server`, `database`, `sources` (per-exchange), `pricing` (median mode, outlier %, freshness), `storage` (retention, batching), `health`.

## CI/CD

- **GitHub Actions** (`.github/workflows/build-timescaledb.yml`): builds custom TimescaleDB Docker image to GHCR.
- **No automated test CI** — run `go test ./...` locally before pushing.
- **Deployment**: Docker or Railway.

## Dependencies (direct)

| Module | Purpose |
|--------|---------|
| `github.com/google/uuid` | UUID v7 generation |
| `github.com/gorilla/websocket` | WebSocket client/server |
| `github.com/jackc/pgx/v5` | PostgreSQL driver |
| `github.com/shopspring/decimal` | Exact decimal arithmetic |
| `gopkg.in/yaml.v3` | YAML config parsing |
