# Architecture

## Overview

BTC Price Tick is a real-time Bitcoin price oracle service designed for prediction market settlement. It aggregates trade data from multiple exchanges and produces a canonical, manipulation-resistant price.

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           BTC PRICE TICK SERVICE                            │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐                         │
│  │   Binance   │  │  Coinbase   │  │   Kraken    │     Exchange Adapters   │
│  │   Adapter   │  │   Adapter   │  │   Adapter   │                         │
│  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘                         │
│         │                │                │                                 │
│         └────────────────┼────────────────┘                                 │
│                          │                                                  │
│                          ▼                                                  │
│                 ┌─────────────────┐                                         │
│                 │   Raw Events    │◄─────── Normalized trade/ticker events  │
│                 │    Channel      │                                         │
│                 └────────┬────────┘                                         │
│                          │                                                  │
│         ┌────────────────┼────────────────┐                                 │
│         │                │                │                                 │
│         ▼                ▼                ▼                                 │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐                         │
│  │  Batch      │  │  Snapshot   │  │  Feed       │     Processing Layer    │
│  │  Writer     │  │  Engine     │  │  Monitor    │                         │
│  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘                         │
│         │                │                │                                 │
│         │         ┌──────┴──────┐         │                                 │
│         │         │             │         │                                 │
│         │         ▼             ▼         │                                 │
│         │  ┌───────────┐ ┌───────────┐    │                                 │
│         │  │ Canonical │ │ 1-Second  │    │                                 │
│         │  │   Ticks   │ │ Snapshots │    │                                 │
│         │  └─────┬─────┘ └─────┬─────┘    │                                 │
│         │        │             │          │                                 │
│         ▼        ▼             ▼          ▼                                 │
│  ┌──────────────────────────────────────────────┐                          │
│  │              PostgreSQL Database              │     Storage Layer       │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────────┐  │                          │
│  │  │raw_ticks │ │canonical │ │ snapshots_1s │  │                          │
│  │  │          │ │ _ticks   │ │              │  │                          │
│  │  └──────────┘ └──────────┘ └──────────────┘  │                          │
│  └──────────────────────────────────────────────┘                          │
│                          │                                                  │
│                          ▼                                                  │
│  ┌──────────────────────────────────────────────┐                          │
│  │                 API Server                    │     API Layer           │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────────┐  │                          │
│  │  │   REST   │ │WebSocket │ │  Settlement  │  │                          │
│  │  │ /v1/...  │ │ /ws/price│ │  Endpoint    │  │                          │
│  │  └──────────┘ └──────────┘ └──────────────┘  │                          │
│  └──────────────────────────────────────────────┘                          │
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
| `CoinbaseAdapter` | Coinbase Advanced | `market_trades`, `ticker` | Optional JWT |
| `KrakenAdapter` | Kraken | `trade`, `ticker` | No |

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

### 2. Snapshot Engine

The core pricing logic that produces canonical prices.

**Location:** `internal/engine/snapshot.go`

**Responsibilities:**
- Maintains per-venue latest state (last trade, last quote)
- Computes canonical price using multi-venue median
- Emits canonical ticks on every price change
- Finalizes 1-second snapshots with watermark delay
- Handles outlier rejection and quality scoring

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

### 3. Batch Writer

Efficiently writes raw events to the database.

**Location:** `internal/storage/writer.go`

**Features:**
- Batches inserts (up to 1000 rows or 200ms)
- Falls back to individual inserts on conflicts
- Non-blocking operation

### 4. API Server

HTTP/WebSocket server for data access.

**Location:** `internal/api/`

**Endpoints:**
| Path | Type | Purpose |
|------|------|---------|
| `/v1/price/latest` | REST | Current price (from memory) |
| `/v1/price/settlement` | REST | Settlement price at 5-min boundary |
| `/v1/price/snapshots` | REST | Historical snapshots query |
| `/v1/price/ticks` | REST | Recent canonical ticks |
| `/v1/price/raw` | REST | Raw exchange data (audit) |
| `/v1/health` | REST | System health |
| `/v1/health/feeds` | REST | Per-source feed health |
| `/ws/price` | WebSocket | Real-time price stream |

---

## Data Flow

### Ingest Path (Exchange → Database)

```
Exchange WS ──► Adapter ──► RawEvent ──► BatchWriter ──► raw_ticks table
                   │
                   └──► SnapshotEngine ──► venueState (in-memory)
```

**Latency:** ~50-150ms from exchange to database

### Price Computation Path

```
RawEvent ──► SnapshotEngine.updateVenueState()
                   │
                   ├──► emitTick() ──► canonical_ticks table
                   │         │
                   │         └──► tickCh ──► WebSocket broadcast
                   │
                   └──► finalizeSnapshot() (every 1s)
                              │
                              ├──► snapshots_1s table
                              │
                              └──► snapshotCh ──► WebSocket broadcast
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
│  - Waits for shutdown signal                                    │
└─────────────────────────────────────────────────────────────────┘
         │
         │ spawns
         ▼
┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐
│ Binance Adapter │  │Coinbase Adapter │  │ Kraken Adapter  │
│   goroutine     │  │   goroutine     │  │   goroutine     │
│                 │  │                 │  │                 │
│ - WS read loop  │  │ - WS read loop  │  │ - WS read loop  │
│ - Ping loop     │  │ - Ping loop     │  │ - Ping loop     │
│ - Reconnect     │  │ - Reconnect     │  │ - Reconnect     │
└────────┬────────┘  └────────┬────────┘  └────────┬────────┘
         │                    │                    │
         └────────────────────┼────────────────────┘
                              │
                              ▼
                    chan domain.RawEvent
                              │
         ┌────────────────────┼────────────────────┐
         │                    │                    │
         ▼                    ▼                    ▼
┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐
│  BatchWriter    │  │ SnapshotEngine  │  │   API Server    │
│   goroutine     │  │   goroutine     │  │   goroutines    │
│                 │  │                 │  │                 │
│ - Batch timer   │  │ - Event loop    │  │ - HTTP handler  │
│ - DB writes     │  │ - 1s ticker     │  │ - WS hub        │
└─────────────────┘  │ - Watermark     │  │ - WS broadcast  │
                     └─────────────────┘  └─────────────────┘
```

**Channel Buffer Sizes:**
- Raw event channel: 10,000
- Snapshot channel: 100
- Tick channel: 1,000
- WS client send channel: 256

---

## Fault Tolerance

### Exchange Connection Failures

```
Connection Lost
      │
      ▼
Exponential Backoff (1s → 2s → 4s → ... → 30s max)
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
| Carry > 10 seconds | Log warning, continue carry |
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
| Database writes | Batched every 200ms |
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
- JSONB raw payloads preserved for audit
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
# Prometheus-style metrics (future)
btc_price_tick_sources_active
btc_price_tick_quality_score
btc_price_tick_data_age_seconds
btc_price_tick_reconnect_total
btc_price_tick_events_processed_total
btc_price_tick_snapshots_finalized_total
btc_price_tick_ws_clients_connected
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

1. **Prometheus metrics export** — `/metrics` endpoint
2. **Additional exchanges** — OKX, Bybit, Gemini
3. **Multiple symbols** — ETH/USD, SOL/USD
4. **TimescaleDB integration** — Automatic partitioning
5. **Signed attestations** — Cryptographic proof of prices
6. **Chainlink integration** — On-chain price feeds
