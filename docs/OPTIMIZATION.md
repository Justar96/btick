# Streaming & TimescaleDB Optimization

## 1. Architecture Assessment

See CLAUDE.md for the full pipeline diagram. Key throughput characteristics:

### Component data rates

| Component | Throughput | Notes |
|-----------|-----------|-------|
| Adapters (3 venues) | ~30-100 msg/s combined | Binance highest volume; trades + book tickers |
| Normalizer | ~30-100 evt/s in, same out | Dedup ring buffer (100k entries), UUID v7 assignment |
| Writer | 2000-row batches / 100ms flush | `pgx.Batch` with `INSERT ... ON CONFLICT DO NOTHING` |
| SnapshotEngine | 1 snapshot/s + irregular ticks | 250ms watermark delay, 10-concurrent finalization cap |
| WebSocket Hub | 1-2 broadcast/s per client | Per-type subscription filtering, 256-slot send buffer |

### Current configuration (from `config.yaml.example`)

| Parameter | Value | Code default |
|-----------|-------|-------------|
| `batch_insert_max_rows` | 2000 | 2000 |
| `batch_insert_max_delay_ms` | 100 | 100ms |
| `trade_freshness_window_ms` | 2000 | 2000ms |
| `late_arrival_grace_ms` | 250 | 250ms |
| `carry_forward_max_seconds` | 10 | 10s |
| `database.ingest_max_conns` | 12 | 12 |
| `database.query_max_conns` | 8 | 8 |
| `ws.send_buffer_size` | 256 | 256 |

### Bottlenecks already addressed

These are already implemented in the current codebase:

- **Adapter read-deadline batching**: deadlines refreshed every 15s instead of per-message (`readLoop` in `base.go`).
- **Connection context cancellation**: blocked reads terminate promptly on shutdown (`runAdapter` in `base.go`).
- **Canonical tick batching**: snapshot engine batches ticks (128 rows / 100ms) before DB write (`flushTickBatch` in `snapshot.go`).
- **Pipeline latency histogram**: minute-window `recv_ts - exchange_ts` histogram logged periodically (`logLatencyStats` in `snapshot.go`).
- **Raw writer simplification**: staging-table DDL removed; pgx `SendBatch` with statement caching (`flushBatch` in `writer.go`).
- **Writer batch slice reuse**: raw writer reuses `[]RawEvent` buffers via `sync.Pool` and clears entries before reuse (`writer.go`).
- **Snapshot DB batching**: finalized snapshots are queued for 10-row / 10s batched persistence without delaying WS broadcast (`snapshot.go`, `persistence.go`, `queries.go`).
- **Snapshot writer shutdown draining**: snapshot/tick DB writers now drain after in-flight finalizers complete so batched writes are not lost on shutdown (`snapshot.go`, `persistence.go`).
- **Normalizer dedup lock sharding**: duplicate detection is now partitioned by source so each shard has its own mutex and bounded ring buffer (`normalizer.go`).
- **Raw tick COPY staging path**: the writer now uses a temp staging table plus `CopyFrom`/merge into `raw_ticks`, keeping `ON CONFLICT DO NOTHING` semantics while reducing row-by-row insert overhead (`writer.go`).
- **Separate pgx pools**: startup now provisions distinct ingest and query pools, with write-heavy components bound to ingest and API handlers bound to query (`postgres.go`, `main.go`, `config.go`).
- **Compression timing**: `raw_ticks` compressed after 2 hours (was 30 min); `canonical_ticks` compression removed (1-day retention makes it wasteful).
- **Continuous aggregates**: `ohlcv_1m` (per-source 1-min candles) and `snapshot_rollups_1h` (hourly rollups) with Direct Compress.
- **Raw tick schema cleanup**: `ingest_partition_date` is removed from the base schema/write path and `idx_raw_ticks_symbol_ts` is dropped in the migration (`001_init.sql`, `writer.go`).
- **Continuous aggregate retention**: `ohlcv_1m` keeps 30 days and `snapshot_rollups_1h` keeps 365 days (`001_init.sql`).
- **`ohlcv_1m` refresh coverage**: continuous aggregate refresh `start_offset` widened from 1 hour to 3 hours to cover late data inside the compression window (`001_init.sql`).

### Remaining bottlenecks

| Area | Issue | Impact |
|------|-------|--------|
| **No exported metrics** | Channel drops, flush latency, snapshot backlog only logged, not metered | No alerting, no dashboards, no capacity planning |

---

## 2. Streaming Layer Optimizations

### 2.1 Quick wins (hours, low risk)

#### A. `sync.Pool` for writer batch slices

**Current**: `flushBatch` in `writer.go` allocates a new `[]RawEvent` slice of capacity `maxRows` each flush.

**Fix**: Use `sync.Pool` to recycle batch slices. Eliminates GC pressure on the hottest allocation path.

**Tradeoff**: Minor code complexity. Pool items may hold stale references — clear slice elements before returning to pool.

**Expected impact**: Reduces GC pause contribution from writer by ~50% under sustained load.

**Status**: Implemented in `internal/storage/writer.go`.

#### C. Snapshot insert batching

**Current**: `InsertSnapshot` in `queries.go` writes one row at a time with `Exec`.

**Fix**: Buffer snapshots (e.g., 10-30 rows / 10s flush) similar to the canonical tick writer pattern. Snapshots arrive at exactly 1/s, so a 10-second batch reduces round-trips 10x.

**Tradeoff**: Adds up to 10s latency for snapshot DB persistence (not for WS broadcast — that's already instant). Acceptable because snapshots are only queried historically.

**Expected impact**: ~10x fewer DB round-trips for snapshot writes.

**Status**: Implemented in `internal/engine/persistence.go`, `internal/engine/snapshot.go`, and `internal/storage/queries.go`.

### 2.2 Medium effort (days, moderate risk)

#### D. `CopyFrom` for raw tick ingestion

**Current**: `flushBatch` in `writer.go` uses `pgx.Batch` to queue individual `INSERT ... ON CONFLICT DO NOTHING` statements.

**Fix**: Replace with `pgx.CopyFrom` using `pgx.CopyFromSlice`. `CopyFrom` uses the PostgreSQL COPY protocol, which is 2-5x faster for bulk inserts.

**Caveat**: `COPY` does not support `ON CONFLICT`. Dedup must be handled by:
1. The normalizer's in-memory dedup (already in place, covers ~99.9% of duplicates).
2. The `idx_raw_ticks_source_trade` unique index (causes the COPY to fail on conflict).

**Mitigation**: Wrap `CopyFrom` in a savepoint or use a temp table + `INSERT ... SELECT ... ON CONFLICT DO NOTHING` merge pattern:
```sql
CREATE TEMP TABLE _staging (LIKE raw_ticks INCLUDING DEFAULTS) ON COMMIT DROP;
COPY _staging FROM STDIN;
INSERT INTO raw_ticks SELECT * FROM _staging ON CONFLICT DO NOTHING;
```

**Tradeoff**: Adds temp table DDL per flush (previously removed). But the COPY speed gain (2-5x) outweighs the DDL cost when batch sizes are ≥500 rows. For the current 2000-row batches, net improvement is substantial.

**Expected impact**: 2-3x reduction in raw tick write latency.

**Status**: Implemented in `internal/storage/writer.go` with a per-flush temp staging table, `CopyFrom`, and `INSERT ... SELECT ... ON CONFLICT DO NOTHING` merge.

#### E. Normalizer dedup lock sharding

**Current**: `isDuplicate` in `normalizer.go` holds a single `sync.Mutex` protecting a 100k-entry `map[string]struct{}` and ring buffer.

**Fix**: Shard the dedup map by source name (3 shards for 3 venues). Each shard gets its own mutex. Since events are naturally partitioned by source, contention drops to near-zero.

```go
type shardedDedup struct {
    shards [numShards]struct {
        mu    sync.Mutex
        seen  map[string]struct{}
        order []string
        head  int
    }
}
```

**Tradeoff**: Slightly more complex code. Eviction is per-shard, so effective dedup window varies by venue volume.

**Expected impact**: Eliminates dedup as a serialization bottleneck. Critical if adding more venues.

**Status**: Implemented in `internal/normalizer/normalizer.go` with per-source dedup shards created lazily and prewarmed for the configured venues.

#### F. Separate pgx pools for ingest vs. query

**Current**: startup provisions separate ingest/query pools, defaulting to `ingest_max_conns: 12` and `query_max_conns: 8`.

**Fix**: Create two pools from the same DSN:
- **Ingest pool**: 8-10 conns, optimized for write batches (higher `MaxConnLifetime`, prepared statements for INSERT/COPY).
- **Query pool**: 5-8 conns, optimized for reads (lower `MaxConnLifetime`, `PreferSimpleProtocol` for ad-hoc queries).

Wire the writer and snapshot engine to the ingest pool; wire API handlers to the query pool.

**Tradeoff**: Doubles connection count. Requires config changes and plumbing two `*DB` through constructors.

**Expected impact**: Eliminates write-spike starvation of API queries. Settlement query P99 latency improves.

**Status**: Implemented in `internal/storage/postgres.go`, `internal/config/config.go`, `cmd/btick/main.go`, and config/docs surfaces.

### 2.3 Long-term (weeks, higher risk)

#### G. Prometheus metrics export

Export counters, gauges, and histograms for:
- `btick_channel_drops_total{channel="writer|engine|normalizer|tick_writer|snapshot_writer"}` — backpressure visibility across fan-out and DB write buffers
- `btick_writer_flush_duration_seconds` — histogram of batch flush times
- `btick_writer_batch_size` — histogram of rows per flush
- `btick_snapshot_finalize_lag_seconds` — gauge of `time.Now() - snapshotSecond`
- `btick_pipeline_latency_ms` — histogram of `recv_ts - exchange_ts`
- `btick_ws_clients` — gauge of connected WS clients
- `btick_ws_drops_total` — counter of dropped WS messages

**Tradeoff**: Adds a lightweight in-process metrics registry and `/metrics` endpoint. This keeps
the hot path additive without introducing a new external dependency in the current build
environment.

**Expected impact**: Enables alerting, capacity planning, and regression detection.

**Status**: Implemented in `internal/metrics/`, `internal/api/server.go`, `internal/api/websocket.go`,
`internal/storage/writer.go`, `internal/engine/snapshot.go`, `internal/normalizer/normalizer.go`,
and `cmd/btick/main.go`.

#### H. Lock-free fan-out ring buffer

**Current**: The fan-out goroutine in `main.go` copies each event to `writerCh` and `engineCh` with non-blocking sends.

**Fix**: Replace with a single-producer multi-consumer ring buffer using atomic operations. Consumers read at their own pace; producer never blocks.

**Tradeoff**: Significant complexity. Only worthwhile at >10k events/s. Current throughput (~100 evt/s) does not justify this.

**Recommendation**: Defer unless adding 5+ venues or processing L2 order book data.

#### I. WAL/CDC-based downstream fan-out

Replace in-process channel fan-out with PostgreSQL logical replication or CDC (e.g., Debezium) for durability guarantees beyond process lifetime.

**Tradeoff**: Massive complexity increase. Only relevant for multi-instance deployments or regulatory audit requirements.

**Recommendation**: Not needed for single-instance BTC oracle. Revisit if multi-region deployment is required.

---

## 3. TimescaleDB Optimizations

> **Note**: This repository now targets TimescaleDB 2.24.0+. Items marked **(2.24.0)** rely on that baseline explicitly.

### 3.1 Schema improvements

#### A. Drop `ingest_partition_date` column from `raw_ticks`

**Current**: `ingest_partition_date DATE NOT NULL DEFAULT CURRENT_DATE` — populated on every insert.

**Problem**: Completely redundant with hypertable chunking on `exchange_ts`. Wastes 4 bytes/row, adds a `DEFAULT` expression evaluation per insert, and is never used in any query or index.

**Fix**: Remove from table definition, writer code, and insert queries.

**Impact**: Marginal per-row savings; cleaner schema.

**Status**: Implemented in `migrations/001_init.sql` and `internal/storage/writer.go`.

#### B. Move `raw_payload` off JSONB

**Current**: `raw_ticks.raw_payload` is now stored as `BYTEA`, and existing JSONB columns are converted in-place during migration after decompressing any compressed `raw_ticks` chunks.

**Problem**: JSONB is parsed and validated on insert. Large payloads are TOASTed, adding I/O. With 2-5M rows/day and 1-day retention, this data is written, compressed, then dropped — rarely if ever queried.

**Options**:
1. **Change to `BYTEA`**: Store raw bytes without JSONB parsing overhead. Saves ~10-20% insert CPU. Loses JSON query capability (rarely needed on raw_ticks).
2. **Change to `TEXT`**: Cheapest storage, no parsing. Same loss of JSON queryability.
3. **Move to separate table**: `raw_ticks_payload(event_id UUID, exchange_ts TIMESTAMPTZ, payload JSONB)` with shorter retention (hours). Keeps audit trail without bloating the hot insert path.
4. **Keep as-is**: If audit queries on `raw_payload` are expected, the JSONB overhead is the cost of queryability.

**Recommendation**: Option 1 (`BYTEA`) unless JSON path queries on raw payloads are a known requirement. The raw payload is already stored as `[]byte` in Go (`RawEvent.RawPayload`).

**Status**: Implemented in `migrations/001_init.sql`. New installs create `BYTEA`, and existing JSONB columns are migrated with a fail-fast decompress-then-convert path using `raw_payload::text` → `BYTEA`.

#### C. Drop `idx_raw_ticks_symbol_ts` index

**Current**: `CREATE INDEX idx_raw_ticks_symbol_ts ON raw_ticks (symbol_canonical, exchange_ts DESC)`

**Problem**: btick only tracks `BTC/USD`. The `symbol_canonical` prefix has zero selectivity — every row matches. The index degenerates to `(exchange_ts DESC)` with extra key overhead. TimescaleDB already creates a default index on `exchange_ts DESC`. Additionally, compressed chunks don't use B-tree indexes — they use segment/orderby metadata for filtering.

**Fix**: Drop the index. If multi-symbol support is added later, re-evaluate.

**Impact**: Eliminates index maintenance overhead on the highest-volume table. Saves ~20% of raw_ticks index write amplification.

**Status**: Implemented in `migrations/001_init.sql`.

### 3.2 Compression setting refinements

**raw_ticks — add bloom filter (2.20+)**:
```sql
ALTER TABLE raw_ticks SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'source',
    timescaledb.compress_orderby = 'exchange_ts DESC',
    timescaledb.compress_bloomfilter = 'trade_id'
);
```
**Why**: `trade_id` is high-cardinality and sparse (NULL for ticker events). Bloom filter enables efficient exact-match lookups on compressed chunks without full decompression. Useful for `QueryRawTicks` debug queries filtering by trade.

**Status**: Implemented in `migrations/001_init.sql` as part of the raw tick compression settings on `raw_ticks`.

#### Lightning-fast recompression (2.24.0)

**Current**: If a late-arriving raw tick insert targets an already-compressed chunk, the insert silently fails (`ON CONFLICT DO NOTHING`). The data is lost.

**2.24.0 behavior**: Inserts into compressed chunks are handled via the new recompression path — the affected segment is decompressed, the row is inserted, and the segment is recompressed. This is 100x faster than the old full-chunk decompression cycle.

**Action**: After upgrading to 2.24.0, test that late-arriving raw ticks (within the 2-hour compression window) are correctly inserted. No schema change needed — this is automatic. Monitor via:
```sql
SELECT * FROM timescaledb_information.job_stats
WHERE job_id IN (
    SELECT job_id FROM timescaledb_information.jobs
    WHERE proc_name = 'policy_recompression'
);
```

### 3.3 Continuous aggregate improvements

#### A. Direct Compress at creation time (2.24.0)

**Current**: `001_init.sql` now creates `ohlcv_1m`, `snapshot_rollups_1h`, and `snapshot_rollups_1d` with `timescaledb.compress = true` in the `WITH` clause, while retaining protected `ALTER MATERIALIZED VIEW ... SET (timescaledb.compress = true)` statements for idempotent reruns.

**2.24.0 improvement**: Use `timescaledb.compress = true` in the `WITH` clause at creation:
```sql
CREATE MATERIALIZED VIEW ohlcv_1m
WITH (timescaledb.continuous, timescaledb.compress = true) AS
...
```
This eliminates the intermediate materialized hypertable step and is functionally equivalent but cleaner.

**Status**: Implemented in `migrations/001_init.sql`. New deployments get direct-compress creation, while existing deployments keep the same end state via the preserved `ALTER` steps.

#### B. Add `snapshot_rollups_1d` hierarchical aggregate

**Current**: `snapshot_rollups_1h` provides hourly OHLC + quality metrics. No daily rollup exists.

**Recommended**:
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
GROUP BY 1, canonical_symbol
WITH NO DATA;

SELECT add_continuous_aggregate_policy('snapshot_rollups_1d',
    start_offset => INTERVAL '30 days',
    end_offset => INTERVAL '1 hour',
    schedule_interval => INTERVAL '1 hour',
    if_not_exists => TRUE);
SELECT add_compression_policy('snapshot_rollups_1d', INTERVAL '30 days', if_not_exists => TRUE);
```
**Why**: Dashboard queries spanning weeks/months hit the 1h aggregate (168 rows/week). A daily rollup reduces this to 7 rows/week. Compression after 30 days keeps storage minimal.

**Status**: Implemented in `migrations/001_init.sql` as a compressed continuous aggregate layered on `snapshot_rollups_1h`.

#### C. Add retention policies for continuous aggregates

**Problem**: `ohlcv_1m` and `snapshot_rollups_1h` have no retention. `ohlcv_1m` is built from `raw_ticks` (1-day retention), so its source data disappears but the aggregate persists. This is intentional — aggregates preserve derived data. But without explicit retention, they grow unbounded.

**Recommended**:
```sql
-- ohlcv_1m: keep 30 days (enough for charting)
SELECT add_retention_policy('ohlcv_1m', INTERVAL '30 days', if_not_exists => TRUE);

-- snapshot_rollups_1h: keep 1 year (matches snapshots_1s retention)
SELECT add_retention_policy('snapshot_rollups_1h', INTERVAL '365 days', if_not_exists => TRUE);

-- snapshot_rollups_1d: keep forever (tiny — ~365 rows/year)
-- No retention policy needed.
```
**Impact**: Prevents silent storage growth. `ohlcv_1m` at 3 sources × 1440 min/day × 30 days = ~130k rows. Negligible.

**Status**: Implemented in `migrations/001_init.sql`.

#### D. Validate `ohlcv_1m` refresh timing

**Current**: refresh `start_offset => INTERVAL '1 hour'`, `end_offset => INTERVAL '1 minute'`, `schedule_interval => INTERVAL '1 minute'`.

**Concern**: `raw_ticks` has 2-hour compression. The refresh start_offset of 1 hour means the aggregate only re-processes the last hour. If raw data is corrected or late-arriving within the 2-hour window, the 1-hour lookback may miss it.

**Fix**: Increase `start_offset` to `INTERVAL '3 hours'` to cover the full compression window plus margin:
```sql
SELECT add_continuous_aggregate_policy('ohlcv_1m',
    start_offset => INTERVAL '3 hours',
    end_offset => INTERVAL '1 minute',
    schedule_interval => INTERVAL '1 minute',
    if_not_exists => TRUE);
```
**Tradeoff**: Each refresh re-materializes 3 hours instead of 1 hour. At 1-minute granularity × 3 sources, that's ~540 rows re-processed per minute. Negligible cost.

**Status**: Implemented in `migrations/001_init.sql` by recreating the continuous aggregate policy with a 3-hour start offset.

### 3.4 Index strategy

#### A. Compressed chunk query patterns

Indexes are **not used** on compressed chunks. TimescaleDB uses:
1. **Chunk exclusion** — time range from query predicate prunes chunks.
2. **Segment filtering** — `compress_segmentby` columns filter segments within a chunk.
3. **Orderby optimization** — `compress_orderby` enables efficient range scans.

**Implication**: Indexes on `raw_ticks` only help during the 2-hour uncompressed window. After compression, `segmentby = 'source'` and `orderby = 'exchange_ts DESC'` govern query performance.

#### B. Recommended index changes

| Action | Index | Reason |
|--------|-------|--------|
| **Drop** | `idx_raw_ticks_symbol_ts` | Single-symbol system; zero selectivity on `symbol_canonical` prefix; default `exchange_ts` index suffices |
| **Keep** | `idx_raw_ticks_source_trade` | Dedup enforcement; partial index (WHERE trade_id IS NOT NULL) is efficient |
| **Keep** | `idx_canonical_ticks_id` | PK equivalent for tick lookups |
| **Keep** | `idx_canonical_ticks_ts` | Time-range queries on canonical ticks |
| **Keep** | `UNIQUE (ts_second)` on snapshots_1s | Settlement point lookups; conflict detection |
| **Consider** | Partial index on `snapshots_1s` for monitoring | `CREATE INDEX idx_snapshots_degraded ON snapshots_1s (ts_second DESC) WHERE is_degraded = TRUE` — useful only if monitoring queries are frequent |

### 3.5 Connection pool tuning

**Recommended pgx pool configuration** (two-pool model):

```yaml
database:
  dsn: "${DATABASE_URL}"
  ingest_max_conns: 12
  query_max_conns: 8
  run_migrations: true
```

**PostgreSQL server-side**:
```sql
-- Verify shared_buffers is ≥25% of RAM
SHOW shared_buffers;

-- Increase WAL buffers for write-heavy ingest
-- Default is 16MB; 64MB is appropriate for this workload
ALTER SYSTEM SET wal_buffers = '64MB';

-- Increase checkpoint_completion_target for smoother I/O
ALTER SYSTEM SET checkpoint_completion_target = 0.9;

-- Autovacuum tuning for hypertables (high insert rate)
ALTER TABLE raw_ticks SET (autovacuum_vacuum_scale_factor = 0.01);
ALTER TABLE raw_ticks SET (autovacuum_analyze_scale_factor = 0.005);
```

### 3.6 Query tuning

#### Settlement query (`QuerySnapshotAt`)

```sql
-- Current query (already optimal for point lookup):
SELECT ... FROM snapshots_1s WHERE ts_second = $1;

-- Verify with:
EXPLAIN (ANALYZE, BUFFERS) SELECT * FROM snapshots_1s WHERE ts_second = '2026-03-19T09:10:00Z';

-- Expected plan: Index Scan on UNIQUE constraint, 1 row, <1ms
-- If plan shows Seq Scan, check that chunk exclusion is working
```

#### Snapshot range query (`QuerySnapshots`)

```sql
-- Current query:
SELECT ... FROM snapshots_1s WHERE ts_second >= $1 AND ts_second <= $2 ORDER BY ts_second ASC;

-- For large ranges (>1 day), this hits compressed chunks.
-- Verify segment filtering is effective:
EXPLAIN (ANALYZE, BUFFERS, VERBOSE)
SELECT * FROM snapshots_1s
WHERE ts_second >= '2026-03-18T00:00:00Z' AND ts_second <= '2026-03-19T00:00:00Z'
ORDER BY ts_second ASC;

-- Expected: Chunk exclusion prunes to 1-2 chunks, segment scan within.
-- If slow: check that compress_orderby matches the query ORDER BY.
-- Current orderby is DESC but query is ASC — this means TimescaleDB reads
-- the segment in reverse. For range queries, this is usually fine (segment
-- is fully decompressed anyway).
```

#### Raw tick debug query (`QueryRawTicks`)

```sql
-- Current query builds dynamic WHERE clause.
-- Most common pattern: filter by source + time range + LIMIT.
-- On compressed chunks, segmentby='source, symbol_canonical' handles source filter.
-- On uncompressed chunks (last 2 hours), idx_raw_ticks_source_trade helps if
-- filtering by trade_id; otherwise default exchange_ts index is used.

-- Benchmark:
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM raw_ticks
WHERE source = 'binance'
  AND exchange_ts >= NOW() - INTERVAL '1 hour'
ORDER BY exchange_ts DESC
LIMIT 100;
```

---

## 4. End-to-End Latency Analysis

### Latency budget (exchange event → WS client delivery)

| Stage | Typical | P99 | Bottleneck |
|-------|---------|-----|-----------|
| Exchange → adapter recv | 10-50ms | 100-200ms | Network, exchange WS infrastructure |
| Adapter → rawCh | <1μs | <10μs | Channel send (non-blocking) |
| rawCh → normalizer | <1μs | <10μs | Channel recv + dedup map lookup |
| normalizer → normalizedCh | <1μs | <10μs | Channel send (non-blocking) |
| fan-out → engineCh | <1μs | <10μs | Channel send (non-blocking) |
| engineCh → SnapshotEngine.updateVenueState | 1-5μs | 50μs | Mutex lock on venueStates |
| SnapshotEngine → emitTick (on trade) | 5-20μs | 100μs | computeVenueRefs + computeCanonical + json.Marshal |
| emitTick → tickCh → broadcastLoop | <1μs | <10μs | Channel send (non-blocking) |
| broadcastLoop → WSHub.Broadcast | 1-5μs | 50μs | json.Marshal + RWMutex + client iteration |
| WSHub → client sendCh → conn.WriteMessage | 10-50μs | 200μs | Buffered channel + kernel write |
| **Total (tick path)** | **~30-130ms** | **~200-500ms** | **Dominated by network to exchange** |

### Latency for snapshot path

| Stage | Typical | Notes |
|-------|---------|-------|
| 1-second ticker fires | 0ms | Aligned to second boundary |
| + watermark delay | 250ms | Configurable `late_arrival_grace_ms` |
| + finalizeSnapshot computation | <1ms | Read lock on venueStates, median computation |
| + DB insert | 1-5ms | Single `INSERT` for snapshots_1s |
| + WS broadcast | <1ms | Same as tick path |
| **Total** | **~255ms after second boundary** | **Watermark dominates** |

### Reduction opportunities

| Change | Latency saved | Where |
|--------|--------------|-------|
| Reduce `late_arrival_grace_ms` from 250 → 100ms | ~150ms on snapshot path | Config change only; risk: late-arriving trades from slow venues miss the window |
| Pre-allocate `VenueRefPrice` slice | ~1-2μs | Avoid allocation in `computeVenueRefs` hot path |
| `CopyFrom` for raw ticks | No latency impact on streaming | Only affects DB write latency |

**Key insight**: End-to-end tick latency is dominated by network transit from exchange to adapter (10-200ms). Application-level optimizations yield microsecond improvements. The biggest controllable lever is the 250ms snapshot watermark delay.

---

## 5. Step-by-Step Optimization Plan

### Phase 1: Quick wins (1-2 days)

| # | Change | Impact | Risk | Files | Status |
|---|--------|--------|------|-------|--------|
| 1 | Drop `ingest_partition_date` from `raw_ticks` | Cleaner schema, marginal insert speedup | Low (unused column) | `001_init.sql`, `writer.go`, `domain/types.go` | Done |
| 2 | Drop `idx_raw_ticks_symbol_ts` | ~20% less index write amplification on raw_ticks | Low (single-symbol; revert is trivial) | `001_init.sql` | Done |
| 3 | Add retention policies to continuous aggregates | Prevent unbounded storage growth | None | `001_init.sql` | Done |
| 4 | Increase `ohlcv_1m` refresh `start_offset` to 3h | Ensure late data is captured in aggregates | None | `001_init.sql` | Done |
| 5 | Batch snapshot inserts (10-row / 10s buffer) | 10x fewer DB round-trips for snapshots | Low (adds seconds of persistence latency) | `snapshot.go`, `queries.go` | Done |

### Phase 2: Medium effort (3-5 days)

| # | Change | Impact | Risk | Files | Status |
|---|--------|--------|------|-------|--------|
| 6 | Normalizer dedup lock sharding | Eliminates serialization bottleneck | Medium (logic change) | `normalizer.go` | Done |
| 7 | `CopyFrom` or staging-table merge for raw ticks | 2-3x faster raw tick ingestion | Medium (conflict handling) | `writer.go` | Done |
| 8 | Separate ingest/query pgx pools | Write-spike isolation, better query P99 | Medium (plumbing) | `postgres.go`, `config.go`, `main.go` | Done |
| 9 | Add `snapshot_rollups_1d` aggregate | Faster long-range dashboard queries | Low | `001_init.sql` or new migration | Done |
| 10 | Bloom filter on `raw_ticks.trade_id` (2.20+) | Faster compressed-chunk trade lookups | Low (requires TimescaleDB support) | `001_init.sql` | Done |

### Phase 3: Longer-term (1-2 weeks)

| # | Change | Impact | Risk | Files | Status |
|---|--------|--------|------|-------|--------|
| 11 | Prometheus metrics export | Observability, alerting, capacity planning | Low (additive) | `internal/metrics/`, `server.go`, `websocket.go`, `writer.go`, `snapshot.go`, `normalizer.go`, `main.go` | Done |
| 12 | `raw_payload` → `BYTEA` | ~10-20% insert CPU savings | Medium (loses JSONB queryability) | `001_init.sql`, `writer.go`, `domain/types.go` | Done |
| 13 | Direct Compress at creation (2.24.0) | Cleaner migration, no functional change | Low (new deployments only) | `001_init.sql` | Done |
| 14 | PostgreSQL server-side tuning | Smoother WAL writes, better vacuum | Medium (requires DB access) | DB config | Pending |

---

## 6. Metrics, Benchmarks & Validation

### Application-level metrics to track

| Metric | Type | Source | Target |
|--------|------|--------|--------|
| `btick_pipeline_latency_ms` | Histogram | `snapshot.go` latency stats | P50 < 50ms, P99 < 250ms |
| `btick_writer_flush_duration_seconds` | Histogram | `writer.go` flush timing | P99 < 0.05s |
| `btick_writer_batch_size` | Histogram | `writer.go` batch length | Avg > 500 |
| `btick_channel_drops_total` | Counter | fan-out, normalizer, engine write buffers | 0 under normal load |
| `btick_snapshot_finalize_lag_seconds` | Gauge | `snapshot.go` `time.Now() - snapshotSecond` | < 0.5s |
| `btick_ws_clients` | Gauge | `websocket.go` client count | Informational |
| `btick_ws_drops_total` | Counter | `websocket.go` dropCount | < 0.1% of messages |

### Database-level metrics

```sql
-- Compression ratio
SELECT
    hypertable_name,
    pg_size_pretty(before_compression_total_bytes) AS before,
    pg_size_pretty(after_compression_total_bytes) AS after,
    ROUND(before_compression_total_bytes::numeric / NULLIF(after_compression_total_bytes, 0), 1) AS ratio
FROM hypertable_compression_stats('raw_ticks')
UNION ALL
SELECT * FROM hypertable_compression_stats('snapshots_1s');

-- Chunk count and sizes
SELECT
    hypertable_name,
    chunk_name,
    is_compressed,
    pg_size_pretty(total_bytes) AS size,
    range_start,
    range_end
FROM timescaledb_information.chunks
WHERE hypertable_name IN ('raw_ticks', 'snapshots_1s', 'canonical_ticks')
ORDER BY hypertable_name, range_start DESC
LIMIT 20;

-- Continuous aggregate freshness
SELECT
    view_name,
    materialization_hypertable_name,
    completed_threshold
FROM timescaledb_information.continuous_aggregates;

-- Job health
SELECT
    job_id,
    application_name,
    schedule_interval,
    last_run_status,
    last_run_duration,
    next_start
FROM timescaledb_information.job_stats
ORDER BY job_id;
```

### Go benchmark targets (to be written)

```bash
# Normalizer dedup throughput
go test -bench=BenchmarkDedup -benchmem ./internal/normalizer/...

# Writer flush performance (mock DB)
go test -bench=BenchmarkFlush -benchmem ./internal/storage/...

# Snapshot engine tick emission rate
go test -bench=BenchmarkEmitTick -benchmem ./internal/engine/...

# Full pipeline integration (adapter mock → WS output)
go test -bench=BenchmarkPipeline -benchmem -timeout 60s ./internal/engine/...
```

### Validation checklist

- [x] `go vet ./...`
- [x] `go test ./...`
- [x] `go test -race ./...`
- [x] Targeted engine/storage validation for the batching changes
- [ ] `EXPLAIN ANALYZE` on settlement query returns Index Scan, <1ms
- [ ] `EXPLAIN ANALYZE` on 1-hour snapshot range query returns chunk exclusion + segment scan
- [ ] Compression ratio on `raw_ticks` is ≥10x
- [ ] Compression ratio on `snapshots_1s` is ≥5x
- [ ] No channel drops logged during 24-hour normal operation
- [ ] Pipeline latency P99 < 250ms (exchange_ts → recv_ts)
- [ ] Writer flush P99 < 50ms at 2000-row batches
- [ ] Continuous aggregate `completed_threshold` within expected offset of real time
- [ ] All TimescaleDB jobs show `last_run_status = 'Success'`

---

<!-- TIMESCALEDB_IMPLEMENTATION:START -->
## TimescaleDB Implementation Summary

### Hypertables

| Table | Chunk Interval | Space Partitions | Compression |
|-------|----------------|------------------|-------------|
| raw_ticks | 1 hour | None | Yes |
| canonical_ticks | 1 day | None | No |
| snapshots_1s | 1 day | None | Yes |

### Compression Settings

| Table | Segment By | Order By | Bloom Filter |
|-------|------------|----------|--------------|
| raw_ticks | source | exchange_ts DESC | trade_id |
| snapshots_1s | _(none)_ | ts_second DESC | None |

### Continuous Aggregates

| Aggregate | Source | Interval | Compression | Hierarchy |
|-----------|--------|----------|-------------|-----------|
| ohlcv_1m | raw_ticks | 1 minute | Yes (2h) | Base |
| snapshot_rollups_1h | snapshots_1s | 1 hour | Yes (14d) | Base |
| snapshot_rollups_1d | snapshot_rollups_1h | 1 day | Yes (30d) | L2 |

### Policies

| Table/Aggregate | Compression | Retention | Refresh |
|-----------------|-------------|-----------|---------|
| raw_ticks | 2 hours | 1 day | N/A |
| canonical_ticks | None | 1 day | N/A |
| snapshots_1s | 1 day | 365 days | N/A |
| ohlcv_1m | 2 hours | 30 days | 1 min (start: 3h) |
| snapshot_rollups_1h | 14 days | 365 days | 5 min (start: 7d) |
| snapshot_rollups_1d | 30 days | Never | 1 hour (start: 30d) |

### TimescaleDB 2.24.0 Features Used

- [ ] Lightning-fast recompression — automatic after upgrade; test late-arriving inserts
- [x] Direct Compress with continuous aggregates — `ohlcv_1m`, `snapshot_rollups_1h`, and `snapshot_rollups_1d` use `timescaledb.compress = true` in WITH clause
- [ ] UUIDv7 in continuous aggregates — not needed (no UUID columns in aggregates)
- [x] Bloom filter sparse indexes — enabled for `raw_ticks.trade_id` (available in TimescaleDB 2.20+)

**TimescaleDB Version:** 2.24.0+ required
**Verified At:** 2026-03-22
<!-- TIMESCALEDB_IMPLEMENTATION:END -->
