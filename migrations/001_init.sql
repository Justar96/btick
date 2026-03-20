-- BTC Price Tick Service schema
-- Requires PostgreSQL 14+ with TimescaleDB

CREATE EXTENSION IF NOT EXISTS timescaledb;

-- Raw normalized events from exchange feeds
CREATE TABLE IF NOT EXISTS raw_ticks (
    event_id              UUID NOT NULL,
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

SELECT create_hypertable('raw_ticks', 'exchange_ts',
    chunk_time_interval => INTERVAL '1 hour',
    if_not_exists => TRUE);

CREATE INDEX IF NOT EXISTS idx_raw_ticks_symbol_ts
    ON raw_ticks (symbol_canonical, exchange_ts DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_raw_ticks_source_trade
    ON raw_ticks (source, trade_id, exchange_ts)
    WHERE trade_id IS NOT NULL;

ALTER TABLE raw_ticks SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'source, symbol_canonical',
    timescaledb.compress_orderby = 'exchange_ts DESC');
SELECT add_compression_policy('raw_ticks', INTERVAL '30 minutes', if_not_exists => TRUE);
SELECT add_retention_policy('raw_ticks', INTERVAL '1 day', if_not_exists => TRUE);

-- Irregular canonical price changes
CREATE TABLE IF NOT EXISTS canonical_ticks (
    tick_id              UUID NOT NULL,
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

SELECT create_hypertable('canonical_ticks', 'ts_event',
    chunk_time_interval => INTERVAL '1 day',
    if_not_exists => TRUE);

CREATE UNIQUE INDEX IF NOT EXISTS idx_canonical_ticks_id
    ON canonical_ticks (tick_id, ts_event);

CREATE INDEX IF NOT EXISTS idx_canonical_ticks_ts
    ON canonical_ticks (ts_event DESC);

ALTER TABLE canonical_ticks SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'canonical_symbol',
    timescaledb.compress_orderby = 'ts_event DESC');
SELECT add_compression_policy('canonical_ticks', INTERVAL '30 minutes', if_not_exists => TRUE);
SELECT add_retention_policy('canonical_ticks', INTERVAL '1 day', if_not_exists => TRUE);

-- Immutable 1-second snapshots
CREATE TABLE IF NOT EXISTS snapshots_1s (
    ts_second               TIMESTAMPTZ NOT NULL,
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
    finalized_at            TIMESTAMPTZ NOT NULL,
    UNIQUE (ts_second)
);

SELECT create_hypertable('snapshots_1s', 'ts_second',
    chunk_time_interval => INTERVAL '1 day',
    if_not_exists => TRUE);

ALTER TABLE snapshots_1s SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'canonical_symbol',
    timescaledb.compress_orderby = 'ts_second DESC');
SELECT add_compression_policy('snapshots_1s', INTERVAL '1 day', if_not_exists => TRUE);
SELECT add_retention_policy('snapshots_1s', INTERVAL '365 days', if_not_exists => TRUE);

-- Per-source feed health (regular table, small)
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

-- Continuous aggregate: 1-minute OHLCV candles per source
CREATE MATERIALIZED VIEW IF NOT EXISTS ohlcv_1m
WITH (timescaledb.continuous) AS
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

ALTER MATERIALIZED VIEW ohlcv_1m SET (timescaledb.compress = true);
ALTER MATERIALIZED VIEW ohlcv_1m SET (timescaledb.materialized_only = false);
SELECT add_continuous_aggregate_policy('ohlcv_1m',
    start_offset => INTERVAL '1 hour',
    end_offset => INTERVAL '1 minute',
    schedule_interval => INTERVAL '1 minute',
    if_not_exists => TRUE);
SELECT add_compression_policy('ohlcv_1m', INTERVAL '2 hours', if_not_exists => TRUE);
