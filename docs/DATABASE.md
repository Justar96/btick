# Database Schema

## Overview

btick uses PostgreSQL 17+ with TimescaleDB 2.24+ for persistent storage. The schema is designed for:
- High-throughput event ingestion via TimescaleDB hypertables
- Fast time-range queries with automatic chunk management
- Immutable audit trail
- Compressed storage with continuous aggregates

---

## Tables

### raw_ticks

Stores every normalized event from exchange feeds. Primary source of truth for auditing.

```sql
CREATE TABLE raw_ticks (
    event_id              UUID NOT NULL,            -- UUID v7 (time-ordered)
    source                TEXT NOT NULL,            -- 'binance', 'coinbase', 'kraken'
    symbol_native         TEXT NOT NULL,            -- Exchange symbol: 'btcusdt', 'BTC-USD'
    symbol_canonical      TEXT NOT NULL,            -- Canonical: 'BTC/USD'
    event_type            TEXT NOT NULL,            -- 'trade' or 'ticker'
    exchange_ts           TIMESTAMPTZ NOT NULL,     -- Timestamp from exchange (hypertable time column)
    recv_ts               TIMESTAMPTZ NOT NULL,     -- When we received it
    price                 NUMERIC(20,8),            -- Trade price (nullable for ticker)
    size                  NUMERIC(28,12),           -- Trade size
    side                  TEXT,                     -- 'buy' or 'sell'
    trade_id              TEXT,                     -- Exchange trade ID
    sequence              TEXT,                     -- Sequence number if provided
    bid                   NUMERIC(20,8),            -- Best bid (ticker only)
    ask                   NUMERIC(20,8),            -- Best ask (ticker only)
    raw_payload           BYTEA NOT NULL            -- Original message bytes for audit/debug
);
-- Converted to TimescaleDB hypertable:
SELECT create_hypertable('raw_ticks', 'exchange_ts',
    chunk_time_interval => INTERVAL '1 hour');
```

**Indexes:**
```sql
-- Deduplication by trade ID (includes exchange_ts for hypertable compatibility)
CREATE UNIQUE INDEX idx_raw_ticks_source_trade ON raw_ticks (source, trade_id, exchange_ts)
    WHERE trade_id IS NOT NULL;
```

**Compression:**
```sql
ALTER TABLE raw_ticks SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'source, symbol_canonical',
    timescaledb.compress_orderby = 'exchange_ts DESC');
ALTER TABLE raw_ticks SET (timescaledb.compress_bloomfilter = 'trade_id');
SELECT add_compression_policy('raw_ticks', INTERVAL '2 hours');
```

**Bloom Filter:** `trade_id` bloom filtering is enabled for compressed chunks to speed up exact-match trade lookups without full decompression.

**Row Size:** ~500 bytes average (depends on raw_payload size)

**Growth Rate:** ~1-5 million rows/day

**Retention:** 1 day (configurable, set via `raw_retention_days` or TimescaleDB retention policy)

---

### canonical_ticks

Stores every canonical price change. More compact than raw_ticks.

```sql
CREATE TABLE canonical_ticks (
    tick_id              UUID NOT NULL,             -- UUID v7
    ts_event             TIMESTAMPTZ NOT NULL,      -- Timestamp of price change (hypertable time column)
    canonical_symbol     TEXT NOT NULL,             -- 'BTC/USD'
    canonical_price      NUMERIC(20,8) NOT NULL,    -- Computed canonical price
    basis                TEXT NOT NULL,             -- How price was computed
    is_stale             BOOLEAN NOT NULL,          -- True if carry-forward
    is_degraded          BOOLEAN NOT NULL,          -- True if < min sources
    quality_score        NUMERIC(5,4) NOT NULL,     -- 0.0000 to 1.0000
    source_count         INTEGER NOT NULL,          -- Number of sources used
    sources_used         TEXT[] NOT NULL,           -- ['binance', 'coinbase']
    source_details_json  JSONB NOT NULL             -- Per-source breakdown
);
-- Converted to TimescaleDB hypertable:
SELECT create_hypertable('canonical_ticks', 'ts_event',
    chunk_time_interval => INTERVAL '1 day');
```

**Indexes:**
```sql
CREATE UNIQUE INDEX idx_canonical_ticks_id ON canonical_ticks (tick_id, ts_event);
CREATE INDEX idx_canonical_ticks_ts ON canonical_ticks (ts_event DESC);
```

**Basis Values:**
| Value | Description |
|-------|-------------|
| `median_trade` | Median of trade prices |
| `median_mixed` | Median including midpoints |
| `single_trade` | Only one trade source |
| `single_midpoint` | Only midpoint available |
| `carry_forward` | No fresh data, carried |

**Retention:** 1 day (configurable, set via `canonical_retention_days` or TimescaleDB retention policy)

**Growth Rate:** ~5,000-10,000 rows/day

---

### snapshots_1s

Immutable 1-second price snapshots. **Primary table for settlement queries.**

```sql
CREATE TABLE snapshots_1s (
    ts_second               TIMESTAMPTZ NOT NULL,     -- Second boundary (hypertable time column)
    canonical_symbol        TEXT NOT NULL,
    canonical_price         NUMERIC(20,8) NOT NULL,
    basis                   TEXT NOT NULL,
    is_stale                BOOLEAN NOT NULL,
    is_degraded             BOOLEAN NOT NULL,
    quality_score           NUMERIC(5,4) NOT NULL,
    source_count            INTEGER NOT NULL,
    sources_used            TEXT[] NOT NULL,
    source_details_json     JSONB NOT NULL,
    last_event_exchange_ts  TIMESTAMPTZ,              -- Latest event in window
    finalized_at            TIMESTAMPTZ NOT NULL,     -- When snapshot was created
    UNIQUE (ts_second)
);
-- Converted to TimescaleDB hypertable:
SELECT create_hypertable('snapshots_1s', 'ts_second',
    chunk_time_interval => INTERVAL '1 day');
```

**Key Properties:**
- `UNIQUE` constraint on `ts_second` ensures one row per second
- Rows are **never updated** after creation (insert uses `ON CONFLICT DO NOTHING`)
- `finalized_at` is typically 250ms after `ts_second`

**Compression:**
```sql
ALTER TABLE snapshots_1s SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'canonical_symbol',
    timescaledb.compress_orderby = 'ts_second DESC');
SELECT add_compression_policy('snapshots_1s', INTERVAL '1 day');
```

**Growth Rate:** 86,400 rows/day (fixed)

**Retention:** 365 days (configurable)

**Example Query - Settlement Price:**
```sql
SELECT canonical_price, basis, quality_score, sources_used
FROM snapshots_1s
WHERE ts_second = '2026-03-19T09:10:00Z';
```

---

### feed_health

Current health status of each exchange feed. Updated frequently.

```sql
CREATE TABLE feed_health (
    source              TEXT PRIMARY KEY,          -- 'binance', 'coinbase', 'kraken'
    conn_state          TEXT NOT NULL,             -- 'connected', 'connecting', 'disconnected'
    last_message_ts     TIMESTAMPTZ,               -- Last message received
    last_trade_ts       TIMESTAMPTZ,               -- Last trade received
    last_heartbeat_ts   TIMESTAMPTZ,               -- Last heartbeat/ping
    reconnect_count_1h  INTEGER DEFAULT 0,         -- Reconnects in last hour
    consecutive_errors  INTEGER DEFAULT 0,         -- Current error streak
    median_lag_ms       INTEGER,                   -- Median latency
    stale               BOOLEAN DEFAULT FALSE,     -- Is feed stale?
    details_json        JSONB DEFAULT '{}',        -- Additional details
    updated_at          TIMESTAMPTZ DEFAULT NOW()
);
```

**Row Count:** Fixed (3 rows, one per source)

---

## Data Types

### NUMERIC Precision

| Field | Precision | Range |
|-------|-----------|-------|
| `price` | NUMERIC(20,8) | Up to $999,999,999,999.99999999 |
| `size` | NUMERIC(28,12) | Handles tiny fractions |
| `quality_score` | NUMERIC(5,4) | 0.0000 to 1.0000 |

### TIMESTAMPTZ

All timestamps are stored as `TIMESTAMPTZ` (timestamp with time zone). They are:
- Stored internally as UTC
- Returned in UTC by default
- Can be queried with any timezone

### TEXT[]

PostgreSQL arrays for `sources_used`:
```sql
-- Query snapshots where binance was used
SELECT * FROM snapshots_1s WHERE 'binance' = ANY(sources_used);
```

### JSONB

Structured data stored as JSONB:
- `source_details_json` — Per-source breakdown
- `details_json` — Additional feed health info

### BYTEA

Opaque byte payloads:
- `raw_payload` — Original exchange message bytes

**Example source_details_json:**
```json
[
  {
    "source": "binance",
    "ref_price": "70105.45",
    "basis": "trade",
    "event_ts": "2026-03-19T09:09:59.999Z",
    "age_ms": 35
  },
  {
    "source": "coinbase",
    "ref_price": "70106.12",
    "basis": "trade",
    "event_ts": "2026-03-19T09:09:59.950Z",
    "age_ms": 85
  }
]
```

---

## Indexes

### Current Indexes

| Table | Index | Type | Purpose |
|-------|-------|------|---------|
| raw_ticks | `idx_raw_ticks_symbol_ts` | B-tree | Query by symbol + time |
| raw_ticks | `idx_raw_ticks_source_trade` | Unique partial | Deduplication (`source, trade_id, exchange_ts`) |
| canonical_ticks | `idx_canonical_ticks_id` | Unique | PK equivalent (`tick_id, ts_event`) |
| canonical_ticks | `idx_canonical_ticks_ts` | B-tree | Time-range queries |
| snapshots_1s | `UNIQUE (ts_second)` | B-tree | Direct timestamp lookup |

### Recommended Additional Indexes

```sql
-- For quality-based queries
CREATE INDEX idx_snapshots_quality ON snapshots_1s (quality_score) 
    WHERE quality_score < 0.5;

-- For degraded snapshot monitoring
CREATE INDEX idx_snapshots_degraded ON snapshots_1s (ts_second) 
    WHERE is_degraded = TRUE;
```

---

## Hypertables & Compression

All time-series tables are TimescaleDB hypertables (required).

| Table | Chunk Interval | Compression Segmentby | Compression Orderby | Compress After |
|-------|----------------|----------------------|--------------------|---------|
| `raw_ticks` | 1 hour | `source, symbol_canonical` | `exchange_ts DESC` | 2 hours |
| `canonical_ticks` | 1 day | — | — | None (short retention) |
| `snapshots_1s` | 1 day | `canonical_symbol` | `ts_second DESC` | 1 day |

## Continuous Aggregates

### ohlcv_1m

1-minute OHLCV candles per source, computed from `raw_ticks` trades.

```sql
CREATE MATERIALIZED VIEW ohlcv_1m
WITH (timescaledb.continuous, timescaledb.compress = true) AS
SELECT
    time_bucket('1 minute', exchange_ts) AS bucket,
    source,
    symbol_canonical,
    first(price, exchange_ts) AS open,
    max(price) AS high,
    min(price) AS low,
    last(price, exchange_ts) AS close,
    sum(size) AS volume,
    count(*) AS trade_count
FROM raw_ticks
WHERE event_type = 'trade' AND price IS NOT NULL
GROUP BY bucket, source, symbol_canonical
WITH NO DATA;
```

**Policies:** Refresh every 1 minute (1-hour start offset, 1-minute end offset). Compressed after 2 hours.

### snapshot_rollups_1h

Hourly rollups of 1-second snapshots with quality and availability metrics.

```sql
CREATE MATERIALIZED VIEW snapshot_rollups_1h
WITH (timescaledb.continuous, timescaledb.compress = true) AS
SELECT
    time_bucket('1 hour', ts_second) AS bucket,
    canonical_symbol,
    first(canonical_price, ts_second) AS open,
    max(canonical_price) AS high,
    min(canonical_price) AS low,
    last(canonical_price, ts_second) AS close,
    avg(quality_score) AS avg_quality_score,
    avg(source_count)::NUMERIC(10,4) AS avg_source_count,
    count(*) AS snapshot_count,
    count(*) FILTER (WHERE is_stale) AS stale_count,
    count(*) FILTER (WHERE is_degraded) AS degraded_count
FROM snapshots_1s
GROUP BY bucket, canonical_symbol
WITH NO DATA;
```

**Policies:** Refresh every 5 minutes (7-day start offset, 5-minute end offset). Compressed after 14 days.

### snapshot_rollups_1d

Daily rollups of hourly snapshot aggregates for long-range dashboards.

```sql
CREATE MATERIALIZED VIEW snapshot_rollups_1d
WITH (timescaledb.continuous, timescaledb.compress = true) AS
SELECT
    time_bucket('1 day', bucket) AS bucket,
    canonical_symbol,
    first(open, bucket) AS open,
    max(high) AS high,
    min(low) AS low,
    last(close, bucket) AS close,
    avg(avg_quality_score) AS avg_quality_score,
    avg(avg_source_count)::NUMERIC(10,4) AS avg_source_count,
    sum(snapshot_count) AS snapshot_count,
    sum(stale_count) AS stale_count,
    sum(degraded_count) AS degraded_count
FROM snapshot_rollups_1h
GROUP BY bucket, canonical_symbol
WITH NO DATA;
```

**Policies:** Refresh every 1 hour (30-day start offset, 1-hour end offset). Compressed after 30 days. No retention policy by default.

---

## Retention Policies

### TimescaleDB Retention (Active)

```sql
SELECT add_retention_policy('raw_ticks', INTERVAL '1 day');
SELECT add_retention_policy('canonical_ticks', INTERVAL '1 day');
SELECT add_retention_policy('snapshots_1s', INTERVAL '365 days');
```

### Manual Cleanup (Fallback)

The retention pruner (`internal/storage/pruner.go`) auto-detects TimescaleDB and becomes a no-op when native retention policies are available. Without TimescaleDB, it performs batched deletes:

```sql
-- Batched delete (10k rows per iteration)
DELETE FROM raw_ticks WHERE ctid IN (
    SELECT ctid FROM raw_ticks WHERE exchange_ts < $cutoff LIMIT 10000
);
```

---

## Storage Estimates

### Per Day

| Table | Rows/Day | Size/Day |
|-------|----------|----------|
| raw_ticks | 2-5M | 1-2.5 GB |
| canonical_ticks | 5-10K | 5-10 MB |
| snapshots_1s | 86,400 | 50-100 MB |

### Per Month (Uncompressed)

| Table | Size |
|-------|------|
| raw_ticks | 30-75 GB |
| canonical_ticks | 150-300 MB |
| snapshots_1s | 1.5-3 GB |

### With TimescaleDB Compression

| Table | Compression Ratio | Compressed Size/Month |
|-------|-------------------|----------------------|
| raw_ticks | 10-20x | 3-5 GB |
| canonical_ticks | 5-10x | 30-50 MB |
| snapshots_1s | 5x | 300-600 MB |

---

## Useful Queries

### Recent Prices
```sql
SELECT ts_second, canonical_price, source_count
FROM snapshots_1s
ORDER BY ts_second DESC
LIMIT 10;
```

### Price at Specific Time
```sql
SELECT * FROM snapshots_1s
WHERE ts_second = '2026-03-19T09:10:00Z';
```

### Prices in Time Range
```sql
SELECT ts_second, canonical_price
FROM snapshots_1s
WHERE ts_second BETWEEN '2026-03-19T09:00:00Z' AND '2026-03-19T09:10:00Z'
ORDER BY ts_second;
```

### Source Availability
```sql
SELECT 
    date_trunc('hour', ts_second) AS hour,
    AVG(source_count) AS avg_sources,
    COUNT(*) FILTER (WHERE is_degraded) AS degraded_count
FROM snapshots_1s
WHERE ts_second > NOW() - INTERVAL '24 hours'
GROUP BY 1
ORDER BY 1;
```

### Per-Source Trade Counts
```sql
SELECT 
    source,
    COUNT(*) AS trade_count,
    MIN(exchange_ts) AS first_trade,
    MAX(exchange_ts) AS last_trade
FROM raw_ticks
WHERE event_type = 'trade'
  AND exchange_ts > NOW() - INTERVAL '1 hour'
GROUP BY source;
```

### Quality Score Distribution
```sql
SELECT 
    CASE 
        WHEN quality_score >= 0.9 THEN 'excellent'
        WHEN quality_score >= 0.7 THEN 'good'
        WHEN quality_score >= 0.5 THEN 'fair'
        ELSE 'poor'
    END AS quality,
    COUNT(*) AS count,
    ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER (), 2) AS pct
FROM snapshots_1s
WHERE ts_second > NOW() - INTERVAL '24 hours'
GROUP BY 1
ORDER BY 2 DESC;
```

---

## Migrations

Migrations are stored in `/migrations/` and run automatically on startup when `run_migrations: true` in config. The migration loader searches for `migrations/*.sql` files in common relative paths and executes all files in sorted order.

### Running Manually

```bash
# Using psql
psql $DATABASE_URL -f migrations/001_init.sql

# Using Railway
railway run psql -f migrations/001_init.sql
```

### Adding New Migrations

1. Create file: `migrations/002_add_index.sql`
2. Add idempotent DDL: `CREATE INDEX IF NOT EXISTS ...`
3. Use `DO $$ ... EXCEPTION WHEN OTHERS ... END $$;` for non-idempotent DDL
4. Restart service with `run_migrations: true`
