# Architecture

## Overview

btick is a real-time Bitcoin price oracle service designed for prediction market settlement. It aggregates trade data from multiple exchanges and produces a canonical, manipulation-resistant price.

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           BTC PRICE TICK SERVICE                            │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐        │
│  │   Binance   │  │  Coinbase   │  │   Kraken    │  │    OKX      │        │
│  │   Adapter   │  │   Adapter   │  │   Adapter   │  │   Adapter   │        │
│  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘        │
│         │                │                │                │                 │
│         └────────────────┼────────────────┼────────────────┘                 │
│                          │ rawCh (10k)                                      │
│                          ▼                                                  │
│                 ┌─────────────────┐                                         │
│                 │   Normalizer    │◄─────── Dedup, UUID v7, symbol mapping  │
│                 └────────┬────────┘                                         │
│                          │ normalizedCh (10k)                               │
│                          ▼                                                  │
│                 ┌─────────────────┐                                         │
│                 │    Fan-out      │◄─────── Non-blocking send to both       │
│                 └───────┬─┬───────┘                                         │
│                         │ │                                                 │
│              writerCh   │ │  engineCh                                       │
│               (10k)     │ │   (10k)                                         │
│         ┌───────────────┘ └───────────────┐                                 │
│         │                                 │                                 │
│         ▼                                 ▼                                 │
│  ┌─────────────┐                   ┌─────────────┐                         │
│  │  Batch      │                   │  Snapshot   │     Processing Layer    │
│  │  Writer     │                   │  Engine     │                         │
│  └──────┬──────┘                   └──────┬──────┘                         │
│         │                          ┌──────┴──────┐                         │
│         │                          │             │                         │
│         │                          ▼             ▼                         │
│         │                   ┌───────────┐ ┌───────────┐                    │
│         │                   │ Canonical │ │ 1-Second  │                    │
│         │                   │   Ticks   │ │ Snapshots │                    │
│         │                   └─────┬─────┘ └─────┬─────┘                    │
│         │                         │             │                          │
│         ▼                         ▼             ▼                          │
│  ┌──────────────────────────────────────────────────────┐                  │
│  │              PostgreSQL + TimescaleDB                 │  Storage Layer  │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────────┐          │                  │
│  │  │raw_ticks │ │canonical │ │ snapshots_1s │          │                  │
│  │  │(hyper)   │ │ _ticks   │ │  (hyper)     │          │                  │
│  │  └──────────┘ └──────────┘ └──────────────┘          │                  │
│  │  ┌──────────────┐ ┌────────────────────┐             │                  │
│  │  │ ohlcv_1m     │ │ snapshot_rollups_1h│ (cont. agg) │                  │
│  │  └──────────────┘ └────────────────────┘             │                  │
│  └──────────────────────────────────────────────────────┘                  │
│                          │                                                  │
│                          ▼                                                  │
│  ┌──────────────────────────────────────────────────────┐                  │
│  │                 API Server                            │  API Layer      │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────────┐          │                  │
│  │  │   REST   │ │WebSocket │ │  Settlement  │          │                  │
│  │  │ /v1/...  │ │ /ws/price│ │  Endpoint    │          │                  │
│  │  └──────────┘ └──────────┘ └──────────────┘          │                  │
│  └──────────────────────────────────────────────────────┘                  │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                    ┌───────────────┼───────────────┐
                    │               │               │
                    ▼               ▼               ▼
             ┌───────────┐  ┌───────────┐  ┌───────────────┐
             │  Market   │  │  Trading  │  │   Analytics   │
             │  Service  │  │    UI     │  │   Dashboard   │
             └───────────┘  └───────────┘  └───────────────┘
                              Consumers
```

---

## Components

### 1. Exchange Adapters

Each adapter maintains a persistent WebSocket connection to an exchange and normalizes incoming data.

| Adapter | Exchange | Streams | Auth Required |
|---------|----------|---------|---------------|
| `BinanceAdapter` | Binance | `btcusdt@trade`, `btcusdt@bookTicker` | No |
| `CoinbaseAdapter` | Coinbase Exchange | `matches`, `ticker`, `heartbeat` | No (public `ws-feed`) |
| `KrakenAdapter` | Kraken | `trade`, `ticker` | No |
| `OKXAdapter` | OKX | `trades`, `tickers` | No |

**Location:** `internal/adapter/`

**Responsibilities:**
- WebSocket connection management with auto-reconnect
- Exponential backoff on failures
- Message parsing and normalization to `domain.RawEvent`
- Ping/pong keepalive handling

**Base Adapter Features:**
- Configurable ping interval
- Max connection lifetime (forced reconnect)
- Connection state tracking
- Consecutive error counting

### 2. Normalizer

Deduplicates raw events, assigns UUID v7 IDs, and maps symbols to canonical form.

**Location:** `internal/normalizer/normalizer.go`

**Responsibilities:**
- UUID v7 assignment (time-ordered)
- Trade deduplication via ring-buffer LRU (100k entries, keyed on `source:trade_id`)
- Symbol mapping to canonical `BTC/USD`
- Non-blocking output channel send (drops on backpressure)

### 3. Snapshot Engine

The core pricing logic that produces canonical prices.

**Location:** `internal/engine/snapshot.go`

**Responsibilities:**
- Maintains per-venue latest state (last trade, last quote)
- Computes canonical price using multi-venue median
- Emits canonical ticks on every price change
- Finalizes 1-second snapshots with watermark delay
- Handles outlier rejection and quality scoring
- Batched canonical tick DB writes (128 rows or 100ms flush interval)
- Pipeline latency histogram logging (per-minute windows)

**Pricing Algorithm:**

```
1. For each venue, get reference price:
   - If fresh trade (< 2s old) → use trade price
   - Else if fresh quote (< 1s old) → use bid/ask midpoint
   - Else → exclude venue

2. If multiple venues available:
   - Compute median of reference prices
   - Reject outliers (> 1% from median)
   - Recompute median if outliers rejected

3. Compute quality score:
   - Source count (50% weight)
   - Data freshness (30% weight)
   - Data type - trade vs midpoint (20% weight)

4. Mark as degraded if < 2 sources
5. Mark as stale if carry-forward needed
```

### 4. Batch Writer

Efficiently writes raw events to the database.

**Location:** `internal/storage/writer.go`

**Features:**
- Batches inserts (up to 2000 rows or 100ms, configurable)
- Uses a temp staging table plus `CopyFrom` and `INSERT ... ON CONFLICT DO NOTHING`
- Falls back to individual inserts on batch failure
- Non-blocking operation

### 5. Retention Pruner

Periodically deletes expired rows to prevent unbounded table growth.

**Location:** `internal/storage/pruner.go`

**Features:**
- Auto-detects TimescaleDB — becomes a no-op when native `drop_chunks` policies are available
- Batched deletes (10k rows per batch with 100ms pauses) to avoid long transactions
- Prunes `raw_ticks`, `canonical_ticks`, and `snapshots_1s`

### 6. API Server

HTTP/WebSocket server for data access. Handlers use `Store` and `Engine` interfaces (not concrete types) for testability — the API package has 100% test coverage.

**Location:** `internal/api/`

**Endpoints:**
| Path | Type | Purpose |
|------|------|---------|
| `/v1/price/latest` | REST | Current price (from memory) |
| `/v1/price/settlement` | REST | Settlement price at 5-min boundary |
| `/v1/price/snapshots` | REST | Historical snapshots query |
| `/v1/price/ticks` | REST | Recent canonical ticks |
| `/v1/price/raw` | REST | Raw exchange data (audit/debug) |
| `/v1/health` | REST | System health |
| `/v1/health/feeds` | REST | Per-source feed health |
| `/ws/price` | WebSocket | Real-time price stream |

**WebSocket Hub features:**
- Welcome message + initial state on connect (no client wait)
- Global sequence numbers (`atomic.Uint64`) on all broadcasts for gap detection
- Per-client subscription filtering (subscribe/unsubscribe message types)
- Application-level heartbeat (default 5s) for liveness during quiet periods
- Non-blocking send with per-client drop counting and periodic logging

---

## Data Flow

### Ingest Path (Exchange → Database)

```
Exchange WS ──► Adapter ──► rawCh ──► Normalizer ──► normalizedCh ──► Fan-out
                                                                       ├──► writerCh ──► BatchWriter ──► raw_ticks table
                                                                       └──► engineCh ──► SnapshotEngine ──► venueState (in-memory)
```

**Latency:** ~50-150ms from exchange to database

### Price Computation Path

```
RawEvent ──► SnapshotEngine.updateVenueState()
                   │
                   ├──► emitTick() ──► ticksToWrite ch ──► runTickWriter ──► canonical_ticks (batched)
                   │         │
                   │         └──► tickCh ──► API broadcastLoop ──► WSHub broadcast
                   │
                    └──► finalizeSnapshot() (every 1s, after watermark delay)
                               │
                               ├──► snapshotsToWrite ch ──► runSnapshotWriter ──► snapshots_1s (batched)
                               │
                               └──► snapshotCh ──► API broadcastLoop ──► WSHub broadcast
```

### Settlement Query Path

```
Market Service ──► GET /v1/price/settlement?ts=...
                              │
                              ▼
                   QuerySnapshotAt(ts)
                              │
                              ▼
                   snapshots_1s table
                              │
                              ▼
                   Return settlement price
```

---

## Concurrency Model

```
┌─────────────────────────────────────────────────────────────────┐
│                         Main Goroutine                          │
│  - Initializes components                                       │
│  - Waits for shutdown signal (SIGINT/SIGTERM)                   │
│  - All goroutines launched via safeGo (panic recovery + cancel) │
└─────────────────────────────────────────────────────────────────┘
         │
         │ spawns via safeGo
         ▼
┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐
│ Binance Adapter │  │Coinbase Adapter │  │ Kraken Adapter  │  │   OKX Adapter   │
│   goroutine     │  │   goroutine     │  │   goroutine     │  │   goroutine     │
│                 │  │                 │  │                 │  │                 │
│ - WS read loop  │  │ - WS read loop  │  │ - WS read loop  │  │ - WS read loop  │
│ - Ping loop     │  │ - Ping loop     │  │ - Ping loop     │  │ - Ping loop     │
│ - Reconnect     │  │ - Reconnect     │  │ - Reconnect     │  │ - Reconnect     │
│ - Conn closer   │  │ - Conn closer   │  │ - Conn closer   │  │ - Conn closer   │
└────────┬────────┘  └────────┬────────┘  └────────┬────────┘  └────────┬────────┘
         │                    │                    │                    │
         └────────────────────┼────────────────────┼────────────────────┘
                              │
                              ▼
                    rawCh (10k buffered)
                              │
                              ▼
                    ┌─────────────────┐
                    │   Normalizer    │  Dedup, UUID v7, symbol map
                    └────────┬────────┘
                              │
                    normalizedCh (10k)
                              │
                              ▼
                    ┌─────────────────┐
                    │    Fan-out      │  Non-blocking send to both
                    └───────┬─┬───────┘
                            │ │
         ┌──────────────────┘ └──────────────────┐
         │                                       │
         ▼ writerCh (10k)                        ▼ engineCh (10k)
┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐
│  BatchWriter    │  │ SnapshotEngine  │  │   API Server    │
│   goroutine     │  │   goroutine     │  │   goroutines    │
│                 │  │                 │  │                 │
│ - Batch timer   │  │ - Event loop    │  │ - HTTP handler  │
│ - CopyFrom      │  │ - 1s ticker     │  │ - WS hub run    │
│ - Fallback INS  │  │ - Watermark     │  │   (heartbeat)   │
└─────────────────┘  │ - Tick writer   │  │ - Broadcast loop│
                     │   (batched)     │  │ - Per-client:   │
                     │ - Snapshot      │  │   writer + reader│
                     │   writer        │  └─────────────────┘
                     │   (batched)     │
                     └─────────────────┘

         ┌─────────────────┐  ┌─────────────────┐
         │ Retention Pruner│  │ Feed Health     │  Background goroutines
         │   goroutine     │  │   Updater       │
         │                 │  │   goroutine     │
         │ - TimescaleDB   │  │ - Periodic (5s) │
         │   auto-detect   │  │ - Placeholder   │
         └─────────────────┘  └─────────────────┘
```

**Channel Buffer Sizes:**
- Raw event channel (`rawCh`, adapters → normalizer): 10,000
- Normalized event channel (`normalizedCh`, normalizer → fan-out): 10,000
- Writer channel (`writerCh`, fan-out → batch writer): 10,000
- Engine channel (`engineCh`, fan-out → snapshot engine): 10,000
- Snapshot channel (engine → API broadcast): 100
- Tick channel (engine → API broadcast): 1,000
- Canonical tick write buffer (engine → DB writer): 500
- WS client send channel: 256 (configurable via `server.ws.send_buffer_size`)

---

## Fault Tolerance

### Exchange Connection Failures

```
Connection Lost
      │
      ▼
Exponential Backoff (1s → 2s → 4s → ... → 30s max, with 0.5x-1.5x jitter)
      │
      ▼
Reconnect Attempt
      │
      ├── Success → Reset backoff, resume
      │
      └── Failure → Increment backoff, retry
```

### Degraded Operation

| Condition | System Response |
|-----------|-----------------|
| 1 exchange down | Continue with 2 sources (degraded) |
| 2 exchanges down | Continue with 1 source (highly degraded) |
| All exchanges down | Carry forward last price (stale) |
| Carry > 10 seconds | Log warning, continue carry (configurable) |
| Database down | Continue streaming, drop writes |

### Data Quality Guarantees

1. **Idempotent writes** — Duplicate trade IDs are ignored
2. **Watermark delay** — 250ms grace for late arrivals
3. **Outlier rejection** — Prices >1% from median excluded
4. **Immutable snapshots** — `snapshots_1s` rows never updated

---

## Performance Characteristics

| Metric | Value |
|--------|-------|
| Price update latency | 50-150ms from exchange |
| Snapshot finalization | 250ms after second boundary |
| API response time | <10ms (latest), <50ms (queries) |
| WebSocket broadcast | <5ms to all clients |
| Database writes | Batched every 100ms or 2000 rows |
| Memory usage | ~50MB base + 10KB per raw event buffered |

### Throughput

| Component | Capacity |
|-----------|----------|
| Raw event ingestion | >10,000 events/sec |
| Canonical tick emission | ~50-100/sec (on price changes) |
| WebSocket clients | 1,000+ concurrent |
| Snapshot storage | 86,400 rows/day |

---

## Security Considerations

### Network
- All exchange connections use TLS (wss://)
- API supports CORS for browser clients
- No authentication required for public price data

### Secrets Management
- Database credentials via `DATABASE_URL` env var
- Optional Coinbase JWT via config
- Config file excluded from git

### Data Integrity
- UUIDs for all event IDs (UUID v7 for time-ordering)
- Raw payload bytes preserved for audit
- Source details stored with every snapshot

---

## Monitoring Points

### Health Checks

| Check | Healthy | Warning | Critical |
|-------|---------|---------|----------|
| Source count | ≥3 | 2 | ≤1 |
| Data freshness | <1s | 1-3s | >3s |
| Quality score | ≥0.8 | 0.5-0.8 | <0.5 |
| Consecutive errors | 0 | 1-5 | >5 |

### Key Metrics to Monitor

```
# Exported at GET /metrics
btick_channel_drops_total
btick_writer_flush_duration_seconds
btick_writer_batch_size
btick_snapshot_finalize_lag_seconds
btick_pipeline_latency_ms
btick_ws_clients
btick_ws_drops_total
```

### Alerting Recommendations

| Alert | Condition | Severity |
|-------|-----------|----------|
| All sources down | source_count = 0 for 30s | Critical |
| Degraded pricing | source_count < 2 for 5m | Warning |
| Stale data | is_stale = true for 30s | Warning |
| High reconnect rate | reconnects > 10/hour | Warning |

---

## Future Enhancements

1. **Additional exchanges** — OKX, Bybit, Gemini
2. **Multiple symbols** — ETH/USD, SOL/USD
3. **TimescaleDB integration** — Automatic partitioning
4. **Signed attestations** — Cryptographic proof of prices
5. **Chainlink integration** — On-chain price feeds
