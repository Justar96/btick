# Streaming & TimescaleDB Optimization

## Current Architecture Assessment

### Streaming pipeline

`binance|coinbase|kraken adapters -> rawCh -> normalizer -> normalizedCh -> writer + snapshot engine -> REST/WebSocket`

### Observed bottlenecks

- Adapter read loop refreshed the WebSocket read deadline on every message and performed an extra context poll before every `ReadMessage` call.
- Canonical tick persistence used a single-row write path, turning high-frequency price changes into avoidable database round-trips.
- Raw event persistence paid per-flush temp table DDL overhead before each merge into `raw_ticks`.
- Pipeline latency existed implicitly in `recv_ts - exchange_ts` but was not surfaced as an operational signal.

### TimescaleDB findings

- `raw_ticks` compressed too early relative to its write pattern, increasing the chance of churn on hot chunks.
- `canonical_ticks` compressed despite a 1-day retention window, which spends CPU on data that is dropped quickly.
- `raw_ticks` dedup relied on `(source, trade_id, exchange_ts)`, which weakens duplicate suppression when the same trade arrives with timestamp skew.
- Historical hourly rollups from `snapshots_1s` had no continuous aggregate path.

## Changes Implemented

### Streaming path

- Reduced adapter read-loop syscall pressure by refreshing read deadlines every 15 seconds instead of every message.
- Closed WebSocket connections on adapter context cancellation so blocked reads terminate promptly.
- Batched canonical tick writes in the snapshot engine with a short flush interval and bounded batch size.
- Added minute-window pipeline latency histogram logging derived from `recv_ts - exchange_ts`.
- Replaced per-flush raw-writer staging-table DDL with batched repeated inserts, allowing pgx statement caching to do the heavy lifting.

### TimescaleDB path

- Applied all TimescaleDB optimizations in `migrations/001_init.sql` (single migration file).
- Delayed `raw_ticks` compression from 30 minutes to 2 hours while keeping 1-hour chunks.
- Removed `canonical_ticks` compression policy while preserving 1-day retention.
- The `raw_ticks` trade dedup index uses `(source, trade_id, exchange_ts)` for hypertable compatibility.
- Added `snapshot_rollups_1h` as a compressed continuous aggregate over `snapshots_1s`.

## Prioritized Follow-up Work

### Highest value next

- Add explicit exported metrics for channel drops, flush latency, and snapshot backlog.
- Rework normalizer dedup locking with sharding or a lock-free structure.
- Audit WebSocket fan-out contention under load and move any expensive work outside shared locks.

### Medium effort

- Tune pgx pool settings separately for ingestion and read workloads.
- Benchmark adapter JSON parsing alternatives on the hottest venue feeds.
- Add query-level validation with `EXPLAIN ANALYZE` on retention-sensitive and aggregate-heavy paths.

### Long-term architecture

- Consider separate DB pools for write-heavy ingestion and query-serving paths.
- Consider WAL/CDC-based downstream fan-out for durability beyond in-process channels.

<!-- TIMESCALEDB_IMPLEMENTATION:START -->
## TimescaleDB Implementation Summary

### Hypertables

| Table | Chunk Interval | Space Partitions | Compression |
|-------|----------------|------------------|-------------|
| raw_ticks | 1 hour | None | Yes |
| canonical_ticks | 1 day | None | No |
| snapshots_1s | 1 day | None | Yes |

### Compression Settings

| Table | Segment By | Order By |
|-------|------------|----------|
| raw_ticks | source, symbol_canonical | exchange_ts DESC |
| snapshots_1s | canonical_symbol | ts_second DESC |

### Continuous Aggregates

| Aggregate | Source | Interval | Compression |
|-----------|--------|----------|-------------|
| ohlcv_1m | raw_ticks | 1 minute | Yes |
| snapshot_rollups_1h | snapshots_1s | 1 hour | Yes (14d) |

### Policies

| Table/Aggregate | Compression | Retention | Refresh |
|-----------------|-------------|-----------|---------|
| raw_ticks | 2 hours | 1 day | N/A |
| canonical_ticks | None | 1 day | N/A |
| snapshots_1s | 1 day | 365 days | N/A |
| ohlcv_1m | 2 hours | None | 1 minute |
| snapshot_rollups_1h | 14 days | None | 5 minutes |

### TimescaleDB 2.24.0 Features Used

- [ ] Lightning-fast recompression
- [x] Direct Compress with continuous aggregates
- [ ] UUIDv7 in continuous aggregates
**TimescaleDB Version:** 2.24.0 (deployed)
**Verified At:** implementation time
<!-- TIMESCALEDB_IMPLEMENTATION:END -->
