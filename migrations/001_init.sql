-- BTC Price Tick Service schema
-- Requires PostgreSQL 14+ (TimescaleDB optional)

-- Raw normalized events from exchange feeds
CREATE TABLE IF NOT EXISTS raw_ticks (
    event_id              UUID PRIMARY KEY,
    source                TEXT NOT NULL,
    symbol_native         TEXT NOT NULL,
    symbol_canonical      TEXT NOT NULL,
    event_type            TEXT NOT NULL,
    exchange_ts           TIMESTAMPTZ NOT NULL,
    recv_ts               TIMESTAMPTZ NOT NULL,
    price                 NUMERIC(20,8),
    size                  NUMERIC(28,12),
    side                  TEXT,
    trade_id              TEXT,
    sequence              TEXT,
    bid                   NUMERIC(20,8),
    ask                   NUMERIC(20,8),
    raw_payload           JSONB NOT NULL,
    ingest_partition_date DATE NOT NULL DEFAULT CURRENT_DATE
);

CREATE INDEX IF NOT EXISTS idx_raw_ticks_symbol_ts
    ON raw_ticks (symbol_canonical, exchange_ts DESC);

CREATE INDEX IF NOT EXISTS idx_raw_ticks_source_ts
    ON raw_ticks (source, exchange_ts DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_raw_ticks_source_trade
    ON raw_ticks (source, trade_id)
    WHERE trade_id IS NOT NULL;

-- Optional: convert to TimescaleDB hypertable
-- SELECT create_hypertable('raw_ticks', 'exchange_ts', if_not_exists => TRUE, migrate_data => TRUE);

-- Irregular canonical price changes
CREATE TABLE IF NOT EXISTS canonical_ticks (
    tick_id              UUID PRIMARY KEY,
    ts_event             TIMESTAMPTZ NOT NULL,
    canonical_symbol     TEXT NOT NULL,
    canonical_price      NUMERIC(20,8) NOT NULL,
    basis                TEXT NOT NULL,
    is_stale             BOOLEAN NOT NULL,
    is_degraded          BOOLEAN NOT NULL,
    quality_score        NUMERIC(5,4) NOT NULL,
    source_count         INTEGER NOT NULL,
    sources_used         TEXT[] NOT NULL,
    source_details_json  JSONB NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_canonical_ticks_ts
    ON canonical_ticks (ts_event DESC);

-- Immutable 1-second snapshots
CREATE TABLE IF NOT EXISTS snapshots_1s (
    ts_second               TIMESTAMPTZ PRIMARY KEY,
    canonical_symbol        TEXT NOT NULL,
    canonical_price         NUMERIC(20,8) NOT NULL,
    basis                   TEXT NOT NULL,
    is_stale                BOOLEAN NOT NULL,
    is_degraded             BOOLEAN NOT NULL,
    quality_score           NUMERIC(5,4) NOT NULL,
    source_count            INTEGER NOT NULL,
    sources_used            TEXT[] NOT NULL,
    source_details_json     JSONB NOT NULL,
    last_event_exchange_ts  TIMESTAMPTZ,
    finalized_at            TIMESTAMPTZ NOT NULL
);

-- Per-source feed health
CREATE TABLE IF NOT EXISTS feed_health (
    source              TEXT PRIMARY KEY,
    conn_state          TEXT NOT NULL,
    last_message_ts     TIMESTAMPTZ,
    last_trade_ts       TIMESTAMPTZ,
    last_heartbeat_ts   TIMESTAMPTZ,
    reconnect_count_1h  INTEGER NOT NULL DEFAULT 0,
    consecutive_errors  INTEGER NOT NULL DEFAULT 0,
    median_lag_ms       INTEGER,
    stale               BOOLEAN NOT NULL DEFAULT FALSE,
    details_json        JSONB NOT NULL DEFAULT '{}',
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
