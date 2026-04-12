# Event-Time Snapshot Engine Hard-Cut Plan

> **For agentic workers:** execute this plan in order. Keep commits tiny. Do not preserve the old snapshot path behind flags, wrappers, or compatibility shims.

**Goal:** replace the current latest-state snapshot finalization with a bounded event-time engine that produces truly second-consistent snapshots across uneven provider latency.

**Primary files:**
- `internal/engine/snapshot.go`
- `internal/engine/snapshot_test.go`
- `internal/engine/integration_test.go`
- `internal/metrics/metrics.go`
- `docs/ARCHITECTURE.md`

**References:**
- Current snapshot engine: `internal/engine/snapshot.go`
- Pricing defaults: `config.yaml.example`
- Existing architecture notes: `docs/ARCHITECTURE.md`

---

## Hard-Cut Policy

- [ ] **No legacy snapshot shim.** Do not keep both `latest venue state` snapshot logic and `event-time bucket` snapshot logic alive at the same time.
- [ ] **No wrapper adapter layer.** Do not add a translation layer that emulates the old model on top of the new one.
- [ ] **No feature flags for internal migration.** This is an internal engine correction, not a user-facing experiment.
- [ ] **Delete obsolete state immediately.** When the new snapshot state lands, remove superseded fields, helpers, and dead branches in the same change set.
- [ ] **One authoritative path.** Snapshot generation must read only from event-time committed state plus open bounded buckets.
- [ ] **Prefer simpler ownership over more abstraction.** Use a single-owner goroutine and bounded structs. Avoid interface-heavy design, callback wrappers, and cross-goroutine mutation of snapshot state.

### Engineering Rules

- [ ] **Single writer for event-time state.** The snapshot engine run loop owns mutable source-window state.
- [ ] **Bounded memory only.** Use a fixed ring or tiny bounded map keyed by second, never an unbounded append-only event log.
- [ ] **Deterministic tie-breaks.** Resolve same-second updates by exchange timestamp, then receive timestamp, then sequence or UUID.
- [ ] **Explicit watermark semantics.** A second closes at `second + 1s + late_arrival_grace_ms`; events after that are too late.
- [ ] **Keep fast and settlement paths intentionally separate.** `snapshot_1s` becomes event-time correct; `latest_price` may remain arrival-time driven unless a later task explicitly changes product semantics.

---

## Target Architecture

### Replace this

- `SnapshotEngine` stores a single `lastTrade` and `lastQuote` per venue.
- `finalizeSnapshot()` reconstructs a second close from whichever per-venue value happens to be latest when finalization runs.

### With this

- Per source, keep:
  - one committed trade state
  - one committed quote state
  - a bounded set of open second buckets
- Each second bucket stores only the latest trade and latest quote that belong to that second by `ExchangeTS`.
- On watermark close for second `S`, promote bucket `S` into committed source state, then compute the snapshot from committed state as of the end of `S`.
- Reject or count events that arrive after their second is sealed.

### Internal Model

Implement concrete internal types, not adapter wrappers:

- `sourceWindow`
- `secondBucket`
- `tradePoint`
- `quotePoint`
- `eventTimeState`

Recommended shape:

```go
type secondBucket struct {
	second time.Time
	trade  *tradePoint
	quote  *quotePoint
}

type sourceWindow struct {
	committedTrade *tradePoint
	committedQuote *quotePoint
	buckets        []secondBucket // fixed-size ring or bounded indexed ring
}
```

The engine should own `map[string]*sourceWindow` and mutate it only inside the run loop goroutine.

---

## Commit Plan

## Commit 1: Lock in the failing behavior with tests

**Commit message:** `test: codify event-time snapshot semantics`

**Files:**
- `internal/engine/snapshot_test.go`
- `internal/engine/integration_test.go`

- [ ] Add a regression test proving a future-second event cannot erase the previous second's contribution.
- [ ] Add a test for late arrival within watermark grace being included.
- [ ] Add a test for late arrival after watermark being excluded.
- [ ] Add a test for same-second tie-break ordering.

**Required test cases:**

1. `TestSnapshotEngine_FinalizeSnapshot_FutureEventDoesNotOverwritePreviousSecond`
2. `TestSnapshotEngine_FinalizeSnapshot_LateArrivalWithinGraceIncluded`
3. `TestSnapshotEngine_FinalizeSnapshot_LateArrivalAfterWatermarkDropped`
4. `TestSnapshotEngine_FinalizeSnapshot_SameSecondDeterministicTieBreak`

**Verification:**

```bash
go test ./internal/engine -run 'TestSnapshotEngine_FinalizeSnapshot|TestSnapshotEngine_EndToEnd'
```

---

## Commit 2: Introduce bounded event-time state primitives

**Commit message:** `refactor: add bounded event-time source windows`

**Files:**
- `internal/engine/snapshot.go`

- [ ] Add `tradePoint`, `quotePoint`, `secondBucket`, and `sourceWindow` types.
- [ ] Add helpers for bucket lookup, bucket reset, point comparison, and event classification.
- [ ] Add explicit helper rules for:
  - too-late event
  - future-skew event
  - bucket promotion into committed state
- [ ] Keep these helpers concrete and local to the engine package.

**Rules:**

- Buckets are keyed by event second from `ExchangeTS.Truncate(time.Second)`.
- Buffer must be bounded to a small fixed window based on watermark and freshness.
- No generic container abstraction.

**Verification:**

```bash
go test ./internal/engine
```

---

## Commit 3: Hard-cut snapshots to event-time committed state

**Commit message:** `refactor!: finalize snapshots from event-time committed state`

**Files:**
- `internal/engine/snapshot.go`

- [ ] Remove snapshot dependence on `venueStates` latest-trade/latest-quote mutation.
- [ ] Route incoming events into per-source second buckets by event time.
- [ ] At watermark close, promote the sealed second into committed source state.
- [ ] Make snapshot venue refs derive only from committed source state at the sealed cutoff.
- [ ] Preserve current median, outlier rejection, quality, and carry-forward semantics.

**Delete in this commit:**

- Snapshot logic that infers second-close state from the latest cross-second venue value.
- Any helper that exists only to support that old path.

**Verification:**

```bash
go test ./internal/engine -run 'TestSnapshotEngine_FinalizeSnapshot|TestSnapshotEngine_VenueRefs|TestSnapshotEngine_EndToEnd'
```

---

## Commit 4: Move event-time state ownership to one goroutine

**Commit message:** `refactor: make snapshot state single-owner in run loop`

**Files:**
- `internal/engine/snapshot.go`

- [ ] Reduce shared mutable snapshot state behind `mu`.
- [ ] Keep only externally published state behind a lock, such as `latestState` if still needed for API reads.
- [ ] Ensure source-window mutation, bucket promotion, and watermark sealing happen only in the run loop goroutine.
- [ ] Keep DB writer goroutines separate; they are output consumers, not state owners.

**Goal:** remove accidental cross-goroutine mutation from the core event-time path.

**Verification:**

```bash
go test ./internal/engine
go test -race ./internal/engine
```

---

## Commit 5: Add event-time observability and rejection metrics

**Commit message:** `feat: add event-time watermark and rejection metrics`

**Files:**
- `internal/metrics/metrics.go`
- `internal/engine/snapshot.go`
- optional tests in `internal/metrics/metrics_test.go`

- [ ] Add counters for:
  - accepted buffered events
  - too-late dropped events
  - future-skew dropped events
  - late arrivals included before watermark
- [ ] Add gauges or histograms for:
  - open bucket count
  - sealed-second lag
  - buffered event age at promotion
- [ ] Emit these metrics directly from the event-time path.

**Do not:** add a secondary metrics shim that mirrors old counters.

**Verification:**

```bash
go test ./internal/metrics ./internal/engine
```

---

## Commit 6: Align canonical tick path with the new state boundary rules

**Commit message:** `refactor: make tick computation explicit about arrival-time semantics`

**Files:**
- `internal/engine/snapshot.go`
- `internal/engine/integration_test.go`

- [ ] Keep `latest_price` intentionally fast and arrival-time based unless product requirements change.
- [ ] Make that separation explicit in code comments and tests so it is not mistaken for legacy behavior.
- [ ] Ensure `snapshot_1s` remains the only settlement-grade event-time stream.

**Purpose:** avoid accidental partial convergence where both paths look similar but mean different things.

**Verification:**

```bash
go test ./internal/engine -run 'TestSnapshotEngine_EndToEnd|TestSnapshotEngine_FinalizeSnapshot'
```

---

## Commit 7: Remove dead code and stale architectural description

**Commit message:** `refactor: delete obsolete snapshot state and update docs`

**Files:**
- `internal/engine/snapshot.go`
- `docs/ARCHITECTURE.md`
- optional: `docs/OPTIMIZATION.md`

- [ ] Delete dead fields and helpers left behind by the old snapshot model.
- [ ] Update architecture docs to describe:
  - event-time buckets
  - committed source state
  - watermark sealing
  - bounded reorder buffer
- [ ] Update wording that still claims the engine is simply `latest state per venue`.

**Verification:**

```bash
go test ./...
```

---

## Detailed Execution Notes

### Bucket sizing

- [ ] Size the per-source bucket window from configuration, not a magic number.
- [ ] Minimum recommended retention:
  - current second
  - previous open second
  - one sealed-but-not-yet-evicted second
  - freshness headroom
- [ ] A small fixed ring of 6 to 8 seconds per source is usually sufficient with current defaults.

### Tie-break rules

- [ ] Prefer larger `ExchangeTS`.
- [ ] If equal, prefer larger `RecvTS`.
- [ ] If equal and sequence is present, prefer larger sequence.
- [ ] Final fallback: stable UUID/event order.

### Watermark rules

- [ ] A second is open until `second + 1s + late_arrival_grace_ms`.
- [ ] After sealing, events for that second must not mutate committed state.
- [ ] Count and discard them.

### Carry-forward rules

- [ ] Carry-forward must operate from committed event-time state only.
- [ ] Do not revive a source with an event that belongs to a later second while sealing an older second.

---

## Acceptance Criteria

- [ ] Snapshot generation is based on event time, not latest mutated venue state.
- [ ] A future-second event cannot overwrite a prior-second snapshot contribution.
- [ ] Late arrivals inside the watermark are included.
- [ ] Late arrivals after sealing are rejected and counted.
- [ ] Memory remains bounded per source.
- [ ] The old snapshot path is deleted, not hidden.
- [ ] `go test ./...` passes.
- [ ] `go test -race ./internal/engine` passes.

---

## Explicit Non-Goals

- [ ] Do not build a generic stream-processing framework.
- [ ] Do not add Kafka-style replay or an unbounded in-memory event log.
- [ ] Do not preserve the old snapshot implementation for rollback convenience.
- [ ] Do not introduce a compatibility interface whose only purpose is to keep outdated internals alive.
