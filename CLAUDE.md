# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

btick is a **Crypto coin price oracle service** for prediction market settlement. It aggregates real-time BTC/USD price data from four exchanges (Binance, Coinbase, Kraken, OKX) and produces a canonical, manipulation-resistant median price with sub-second precision.

**Backend:** Go 1.23, PostgreSQL 17+ with TimescaleDB 2.24+, gorilla/websocket, pgx/v5, shopspring/decimal.

**Frontend:** React 19, TypeScript, Vite 6, TanStack Router + Query, openapi-fetch, Liveline (chart), CSS Modules. Bun workspace.

## Quick Reference Commands

```bash
# Go backend
go build -o btick ./cmd/btick
go run ./cmd/btick
go test ./...                          # All tests
go test -v ./internal/engine/          # Verbose, single package
go test -run TestFoo ./internal/pkg/   # Single test

# Web frontend (from repo root or web/)
bun install                            # Install deps (Bun workspace)
bun run --cwd web dev                  # Dev server (Vite, proxies /v1 and /ws to :8080)
bun run --cwd web build                # Production build (tsc + vite build)
bun run --cwd web generate             # Regenerate API types from docs/openapi.yaml

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
    server.go               # Server struct, broadcastLoop, middleware
    websocket.go            # WSHub: subscription filtering, per-client goroutines
  config/                   # YAML config loading with env var expansion
  domain/                   # Immutable domain types (RawEvent, CanonicalTick, Snapshot1s, etc.)
  engine/                   # Core pricing logic: median calculation, outlier rejection, snapshots
    snapshot.go             # SnapshotEngine: 1s snapshots, carry-forward, canonical tick generation
    multi.go                # MultiEngine: aggregates per-symbol engines for the API layer
    persistence.go          # Tick/snapshot DB persistence
  normalizer/               # Event deduplication (sync.Map sharded by source), UUID v7 assignment
  storage/                  # PostgreSQL/TimescaleDB layer (pgx, no ORM)
  metrics/                  # In-memory Prometheus-compatible metrics at /metrics
web/                        # React frontend (Bun workspace member)
  src/
    main.tsx                # React root: QueryClientProvider + RouterProvider
    router.tsx              # TanStack Router: / (redirect), /$symbol (coin page), /api
    api/
      schema.d.ts           # Generated types from openapi.yaml (do not hand-edit)
      client.ts             # openapi-fetch instance
      queries.ts            # TanStack Query options factories per endpoint
    ws/
      types.ts              # WS message + client action types
      useWebSocket.ts       # Hook: connect, auto-reconnect with backoff, parse messages
      context.tsx           # WebSocketProvider: bridges WS → React state + TanStack Query cache
    components/             # UI components with co-located CSS Modules
    routes/                 # Route components (TanStack Router file convention)
    styles/                 # Global CSS, custom properties
```

## Architecture & Data Flow

### Backend pipeline (per symbol)

```
Exchanges → Adapters → Normalizer → Fan-out → Engine
                                       ↓          ↓
                                 shared Writer   MultiEngine → API/WebSocket
                                       ↓
                                 PostgreSQL (TimescaleDB)
```

- **Multi-symbol**: each symbol gets an independent pipeline (adapters, normalizer, engine).
- Config supports both legacy single-symbol (`canonical_symbol` + `sources`) and new multi-symbol (`symbols[]`) format.
- Each adapter runs in its own goroutine with auto-reconnect and exponential backoff.
- Normalizer deduplicates by `(source, trade_id)` and assigns UUID v7 IDs.
- Fan-out uses **non-blocking channel sends** — full channels drop events rather than block.
- Engine produces 1-second snapshots with median price. Canonical ticks emitted only on price changes.
- `MultiEngine` merges snapshot/tick/source-price channels and routes `LatestState(symbol)` to the correct engine.

### Frontend data flow

```
WebSocketProvider ──→ React state (prices, sourcePrices, sourceStatus)
       │              └─→ consumed by usePrice(), useSourcePrices(), useSourceStatus()
       └──→ TanStack Query cache (latest price, live snapshots)
              └─→ consumed by components via queryOptions from api/queries.ts
```

- **WS is the primary data source** for live prices. The `WebSocketProvider` in `ws/context.tsx` subscribes to `latest_price`, `snapshot_1s`, `source_price`, and `source_status` message types.
- WS messages bridge into TanStack Query cache so components using `useQuery` get live updates without polling.
- REST endpoints (via openapi-fetch) are used for historical data (snapshots, ticks) and initial loads.
- API types are generated from `docs/openapi.yaml` → `web/src/api/schema.d.ts` via `openapi-typescript`. Run `bun run --cwd web generate` after modifying the OpenAPI spec.
- Vite dev server proxies `/v1` and `/ws` to Go backend at `localhost:8080`.

### WS message types

| Type | Direction | Purpose |
|------|-----------|---------|
| `latest_price` | server→client | Canonical tick with price, quality, source_details |
| `snapshot_1s` | server→client | 1-second price snapshot (feeds chart) |
| `source_price` | server→client | Per-exchange raw price |
| `source_status` | server→client | Exchange connection state changes |
| `subscribe`/`unsubscribe` | client→server | Filter message types |

## Key Conventions

### Go Backend
- **Standard Go conventions**: `gofmt`, `go vet`, idiomatic naming.
- **Short variable names**: `evt`, `cfg`, `db`, `mu`, `wg`, `ctx`.
- **Interfaces**: defined on consumer side with `-er` suffix or explicit nouns (`Store`, `Engine`).
- Every error explicitly checked and wrapped: `fmt.Errorf("context: %w", err)`.
- `log/slog` structured logging with `.With()` context fields.
- Go standard `testing` package only — table-driven tests, mocks defined locally in test files.
- **Direct pgx** — no ORM. Split connection pools (ingest writes, query reads).
- **shopspring/decimal** for all price arithmetic — never `float64` for money.
- `safeGo()` wrapper for goroutines: panic recovery + graceful shutdown.
- Channels are non-blocking (drop on full, never block producers).

### Web Frontend
- **CSS Modules** (`.module.css`) co-located with components — no Tailwind, no CSS-in-JS.
- **Design**: Paper-tone minimal (`#fafafa` bg, black ink, thin `#ebebeb` dividers). Inspired by Liveline docs + Chainlink data feeds. No dark theme.
- **TanStack Router** with code-based route definitions in `router.tsx` (not file-based generation).
- **TanStack Query** for REST data with `queryOptions` factories in `api/queries.ts`.
- **openapi-fetch** for type-safe REST calls generated from the OpenAPI spec.
- **Liveline** library for the real-time animated price chart.
- Path alias: `@/` maps to `web/src/` (configured in tsconfig + vite).

## Configuration

Config loaded from `config.yaml` (see `config.yaml.example`). Environment variable expansion supported via `${VAR}` syntax. `DATABASE_URL` env var overrides config DSN.

Key sections: `server`, `database`, `sources` (per-exchange), `pricing` (median mode, outlier %, freshness), `storage` (retention, batching), `health`.

Frontend uses `VITE_API_URL` env var to override API base URL (defaults to same-origin with Vite proxy in dev).

## REST API Endpoints

```
GET  /v1/price/latest          ?symbol=   Current canonical price
GET  /v1/price/settlement      ?ts=       Price at 5-min boundary
GET  /v1/price/snapshots       ?start=&end=
GET  /v1/price/ticks           ?limit=
GET  /v1/price/raw             ?source=&start=&end=&limit=
GET  /v1/health
GET  /v1/health/feeds
GET  /v1/symbols                          List configured symbols
GET  /metrics                             Prometheus metrics
WS   /ws/price                            Live stream (subscribe/unsubscribe filtering)
```

## CI/CD

- **GitHub Actions** (`.github/workflows/build-timescaledb.yml`): builds custom TimescaleDB Docker image to GHCR.
- **No automated test CI** — run `go test ./...` locally before pushing.
- **Deployment**: Docker or Railway.
