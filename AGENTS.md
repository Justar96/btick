
## What This Is

Real-time Bitcoin price oracle for prediction markets. Consumes WebSocket feeds from Binance, Coinbase, and Kraken, computes a multi-venue median price, and exposes REST/WebSocket APIs for settlement and live streaming.

## Build & Run

```bash
go build -o btick ./cmd/btick        # build binary
go run ./cmd/btick                      # run directly
go run ./cmd/btick -config config.yaml  # explicit config path
go test ./...                             # all tests
go test ./internal/engine/...             # single package
go test -run TestSnapshotMedian ./internal/engine/...  # single test
```

Hot reload with [air](https://github.com/air-verse/air): `air`

Docker: `docker build -t btick .` then `docker run -e DATABASE_URL=... -p 8080:8080 btick`

**Config setup:** `config.yaml` is gitignored. Copy from `config.yaml.example` to get started. The `DATABASE_URL` env var overrides whatever is in `database.dsn`. Config supports `${ENV_VAR}` expansion for all values.

PostgreSQL is optional — the app runs without a database.

## Architecture

**Event pipeline (channel-based, all in goroutines):**

```
Exchange Adapters (3) → rawCh(10k) → Normalizer → normalizedCh(10k) → fan-out
                                                                        ├→ Writer → PostgreSQL
                                                                        └→ SnapshotEngine → ticks/snapshots → API + WebSocket Hub
```

Orchestrated in `cmd/btick/main.go`.

**Key packages:**

| Package | Role |
|---------|------|
| `internal/adapter` | Per-exchange WebSocket clients with auto-reconnect (`base.go` has shared backoff logic) |
| `internal/normalizer` | Deduplication (ring-buffer LRU, 100k entries), UUID v7 assignment, symbol mapping |
| `internal/engine` | `SnapshotEngine` — 1-second windowed median pricing with outlier rejection, carry-forward, quality scoring |
| `internal/storage` | pgx-based PostgreSQL: batch writer (staging table + `CopyFrom`), time-range queries, health upserts, retention pruner |
| `internal/api` | HTTP server + WebSocket hub; handlers use `Store`/`Engine` interfaces for testability (100% coverage) |
| `internal/domain` | Core types: `RawEvent`, `CanonicalTick`, `Snapshot1s`, `FeedHealth`, `LatestState` |
| `internal/config` | YAML loader with env var expansion and `time.Duration` converters |

**Pricing logic** (`internal/engine/snapshot.go`): Each 1-second window collects trades/quotes per venue, computes per-venue prices, takes the median across venues, rejects outliers >1% from median, and emits a `Snapshot1s`. A `CanonicalTick` is emitted only when the price changes. Carry-forward reuses the last price for up to 10 seconds if no fresh data arrives.

**Concurrency patterns:**
- All goroutines launch via `safeGo()` in main.go — panic recovery + context cancellation for clean shutdown.
- All channel sends are non-blocking with `select/default` — drops events rather than blocking producers. Drop counts are logged.
- The batch writer uses a temp staging table + `CopyFrom` + `INSERT ... ON CONFLICT DO NOTHING`, falling back to individual inserts on failure.
- The retention pruner auto-detects TimescaleDB and becomes a no-op when native `drop_chunks` policies are available.

## API Surface

- `GET /v1/price/latest` — current price + quality
- `GET /v1/price/settlement?ts=` — price at 5-minute boundary (settlement)
- `GET /v1/price/snapshots?start=&end=` — historical 1s snapshots
- `GET /v1/price/ticks?limit=` — recent price changes
- `GET /v1/health` / `GET /v1/health/feeds` — system and per-source health
- `WS /ws/price` — live stream (see WebSocket Protocol below)

## WebSocket Protocol (`/ws/price`)

**Connection lifecycle:**
1. Client connects → receives `welcome` message (no `seq`)
2. Receives `latest_price` with `"message":"initial_state"` or `"message":"no_data_yet"` (no `seq`)
3. Live broadcast messages follow with incrementing `seq` numbers

**Message types (server → client):**
- `welcome` — sent once on connect, `"message":"btick/v1"`
- `latest_price` — emitted on price change; also sent as initial state on connect
- `snapshot_1s` — emitted every second with windowed median price
- `heartbeat` — emitted every 5 seconds (configurable), carries `seq` for gap detection during quiet periods

**Sequence numbers:** All broadcast messages carry a monotonically increasing `seq` field (uint64). Clients detect gaps: "received seq N then N+3 → missed 2 messages, call `/v1/price/latest` to resync." With subscription filtering, clients subscribed to a subset will naturally see seq gaps — this is expected.

**Subscription filtering (client → server):**
```json
{"action": "subscribe", "types": ["snapshot_1s", "latest_price", "heartbeat"]}
{"action": "unsubscribe", "types": ["snapshot_1s"]}
```
Default: all types subscribed. Unknown actions/types are silently ignored (forward-compatible). A client that sends nothing receives all types (backward-compatible).

**Drop handling:** If a client's send buffer fills (slow consumer), messages are dropped rather than blocking producers. Drop counts are logged server-side every 100 drops.

**Configuration (`server.ws` in config.yaml):**
- `send_buffer_size` (default 256) — per-client buffered channel size
- `heartbeat_interval_sec` (default 5) — app-level heartbeat period
- `ping_interval_sec` (default 30) — WebSocket-level ping period
- `read_deadline_sec` (default 60) — client must respond to ping within this window

## Key Design Decisions

- All prices use `shopspring/decimal` — never float64 for money.
- Adapters each have distinct WebSocket protocols and auth (Coinbase needs JWT). Shared reconnection logic lives in `base.go` with exponential backoff (1s–30s) and jitter.
- The snapshot engine requires `minimum_healthy_sources: 2` for a "confirmed" quality score.
- Database tables: `raw_ticks` (partitioned by date), `canonical_ticks`, `snapshots_1s`, `feed_health`. Migrations in `migrations/001_init.sql`.
- Venue ref price resolution: trade price preferred, falls back to bid/ask midpoint if trade is stale.

## Code Style

- ~700 LOC file guideline; split when clarity improves
- No V2 copies — extract helpers instead of duplicating with suffixes
- 100 char width, 2-space indent, double quotes (oxfmt default), semicolons
- camelCase for variables/functions, PascalCase for classes/interfaces
- Early returns over deep nesting

## Hard-Cut Product Policy

No external user base — optimize for one canonical current-state implementation. No compatibility bridges, migration shims, fallback paths, or dual behavior for old states. Prefer fail-fast diagnostics and explicit recovery over automatic migration or silent fallbacks. Delete old-state compatibility code rather than carrying it forward.

If temporary compatibility code is introduced, call out in the same diff: why it exists, why the canonical path is insufficient, exact deletion criteria, and the tracking task.
