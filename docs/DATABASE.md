# Database Schema

## Overview

BTC Price Tick uses PostgreSQL 14+ for persistent storage. The schema is designed for:
- High-throughput event ingestion
- Fast time-range queries
- Immutable audit trail
- Optional TimescaleDB support

---

## Tables

### raw_ticks

Stores every normalized event from exchange feeds. Primary source of truth for auditing.

```sql
CREATE TABLE raw_ticks (
    event_id              UUID PRIMARY KEY,        -- UUID v7 (time-ordered)
    source                TEXT NOT NULL,           -- 'binance', 'coinbase', 'kraken'
    symbol_native         TEXT NOT NULL,           -- Exchange symbol: 'btcusdt', 'BTC-USD'
    symbol_canonical      TEXT NOT NULL,           -- Canonical: 'BTC/USD'
    event_type            TEXT NOT NULL,           -- 'trade' or 'ticker'
    exchange_ts           TIMESTAMPTZ NOT NULL,    -- Timestamp from exchange
    recv_ts               TIMESTAMPTZ NOT NULL,    -- When we received it
    price                 NUMERIC(20,8),           -- Trade price (nullable for ticker)
    size                  NUMERIC(28,12),          -- Trade size
    side                  TEXT,                    -- 'buy' or 'sell'
    trade_id              TEXT,                    -- Exchange trade ID
    sequence              TEXT,                    -- Sequence number if provided
    bid                   NUMERIC(20,8),           -- Best bid (ticker only)
    ask                   NUMERIC(20,8),           -- Best ask (ticker only)
    raw_payload           JSONB NOT NULL,          -- Original message for audit
    ingest_partition_date DATE DEFAULT CURRENT_DATE
);
```

**Indexes:**
```sql
-- Query by symbol and time
CREATE INDEX idx_raw_ticks_symbol_ts ON raw_ticks (symbol_canonical, exchange_ts DESC);

-- Query by source and time
CREATE INDEX idx_raw_ticks_source_ts ON raw_ticks (source, exchange_ts DESC);

-- Deduplication by trade ID
CREATE UNIQUE INDEX idx_raw_ticks_source_trade ON raw_ticks (source, trade_id) 
    WHERE trade_id IS NOT NULL;
```

**Row Size:** ~500 bytes average (depends on raw_payload)

**Growth Rate:** ~1-5 million rows/day

**Retention:** 14 days (configurable)

---

### canonical_ticks

Stores every canonical price change. More compact than raw_ticks.

```sql
CREATE TABLE canonical_ticks (
    tick_id              UUID PRIMARY KEY,         -- UUID v7
    ts_event             TIMESTAMPTZ NOT NULL,     -- Timestamp of price change
    canonical_symbol     TEXT NOT NULL,            -- 'BTC/USD'
    canonical_price      NUMERIC(20,8) NOT NULL,   -- Computed canonical price
    basis                TEXT NOT NULL,            -- How price was computed
    is_stale             BOOLEAN NOT NULL,         -- True if carry-forward
    is_degraded          BOOLEAN NOT NULL,         -- True if < min sources
    quality_score        NUMERIC(5,4) NOT NULL,    -- 0.0000 to 1.0000
    source_count         INTEGER NOT NULL,         -- Number of sources used
    sources_used         TEXT[] NOT NULL,          -- ['binance', 'coinbase']
    source_details_json  JSONB NOT NULL            -- Per-source breakdown
);
```

**Indexes:**
```sql
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

**Growth Rate:** ~5,000-10,000 rows/day

---

### snapshots_1s

Immutable 1-second price snapshots. **Primary table for settlement queries.**

```sql
CREATE TABLE snapshots_1s (
    ts_second               TIMESTAMPTZ PRIMARY KEY,  -- Second boundary
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
    finalized_at            TIMESTAMPTZ NOT NULL      -- When snapshot was created
);
```

**Key Properties:**
- Primary key on `ts_second` ensures one row per second
- Rows are **never updated** after creation
- `finalized_at` is typically 250ms after `ts_second`

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
- `raw_payload` — Original exchange message
- `source_details_json` — Per-source breakdown
- `details_json` — Additional feed health info

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
| raw_ticks | `idx_raw_ticks_source_ts` | B-tree | Query by source + time |
| raw_ticks | `idx_raw_ticks_source_trade` | Unique partial | Deduplication |
| canonical_ticks | `idx_canonical_ticks_ts` | B-tree | Time-range queries |
| snapshots_1s | PRIMARY KEY | B-tree | Direct timestamp lookup |

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

## Partitioning

### Option 1: Native PostgreSQL Partitioning

```sql
CREATE TABLE raw_ticks (
    ...
) PARTITION BY RANGE (exchange_ts);

CREATE TABLE raw_ticks_2026_03 PARTITION OF raw_ticks
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
```

### Option 2: TimescaleDB (Recommended for Production)

```sql
-- Convert to hypertable
SELECT create_hypertable('raw_ticks', 'exchange_ts', 
    chunk_time_interval => INTERVAL '1 day',
    if_not_exists => TRUE
);

-- Enable compression for older data
ALTER TABLE raw_ticks SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'source'
);

-- Add compression policy
SELECT add_compression_policy('raw_ticks', INTERVAL '7 days');
```

---

## Retention Policies

### Manual Cleanup

```sql
-- Delete raw ticks older than 14 days
DELETE FROM raw_ticks 
WHERE exchange_ts < NOW() - INTERVAL '14 days';

-- Delete canonical ticks older than 30 days
DELETE FROM canonical_ticks 
WHERE ts_event < NOW() - INTERVAL '30 days';
```

### TimescaleDB Retention

```sql
SELECT add_retention_policy('raw_ticks', INTERVAL '14 days');
SELECT add_retention_policy('canonical_ticks', INTERVAL '30 days');
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

Migrations are stored in `/migrations/` and run automatically on startup when `run_migrations: true` in config.

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
3. Restart service with `run_migrations: true`
