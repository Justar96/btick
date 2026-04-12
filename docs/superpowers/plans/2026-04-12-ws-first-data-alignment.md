# WS-First Data Alignment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate price drift by making WebSocket the single real-time data source, restricting TanStack Query to historical/on-demand data.

**Architecture:** Backend adds two new WS message types (`source_price`, `source_status`). Frontend replaces all REST polling with WS-driven state, bridges WS data into TanStack Query cache, and moves chart backfill into proper cached queries.

**Tech Stack:** Go 1.23 (gorilla/websocket, shopspring/decimal), React 19, TanStack Query v5, TanStack Router, openapi-fetch, Vite

**Spec:** `docs/superpowers/specs/2026-04-12-ws-first-data-alignment-design.md`

---

## File Structure

### Backend (Go) — files to modify

| File | Responsibility |
|------|---------------|
| `internal/domain/types.go` | Add `SourcePriceEvent` and `SourceStatusEvent` types |
| `internal/engine/snapshot.go` | Add `sourcePriceCh` channel, per-source 500ms throttle in `updateVenueState` |
| `internal/engine/multi.go` | Merge `sourcePriceCh` channels, expose `SourcePriceCh()` |
| `internal/api/server.go` | Extend `Engine` interface with `SourcePriceCh()`, read in `broadcastLoop` |
| `internal/api/websocket.go` | Add `Source`/`ConnState`/`Stale` fields to `WSMessage`, add subscription types |
| `internal/adapter/base.go` | Add `OnStateChange` callback, fire on `setConnState` transitions |
| `cmd/btick/main.go` | Wire adapter state change callbacks to WS hub broadcast |

### Frontend (TypeScript/React) — files to modify

| File | Responsibility |
|------|---------------|
| `web/src/ws/types.ts` | Add new message types, `SourcePrice`, `SourceStatus` interfaces |
| `web/src/ws/context.tsx` | Expand state with source prices/status, new hooks, query cache bridge |
| `web/src/ws/useWebSocket.ts` | Subscribe to new message types on connect, add reconnect callback |
| `web/src/api/queries.ts` | Delete `rawTicksOptions`, fix query params, adjust stale times |
| `web/src/main.tsx` | Update `QueryClient` default `staleTime` |
| `web/src/components/SourcePanel.tsx` | Replace REST queries with WS hooks |
| `web/src/components/PriceChart.tsx` | Replace raw fetch with TanStack Query |
| `web/src/components/HealthTable.tsx` | Remove polling from feed health query |
| `web/src/routes/coin.$symbol.tsx` | Pass `symbol` prop to `SourcePanel` |

---

## Task 1: Add domain types for source price and status events

**Files:**
- Modify: `internal/domain/types.go:94` (after `LatestState`)

- [ ] **Step 1: Add `SourcePriceEvent` and `SourceStatusEvent` types**

Add at the end of `internal/domain/types.go` (after line 94):

```go
// SourcePriceEvent is emitted when a venue's latest trade price changes.
type SourcePriceEvent struct {
	Symbol string          `json:"symbol"`
	Source string          `json:"source"`
	Price  decimal.Decimal `json:"price"`
	TS     time.Time       `json:"ts"`
}

// SourceStatusEvent is emitted when a feed's connection state changes.
type SourceStatusEvent struct {
	Symbol    string `json:"symbol"`
	Source    string `json:"source"`
	ConnState string `json:"conn_state"`
	Stale     bool   `json:"stale"`
	TS        time.Time `json:"ts"`
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/jt-devnet/btick && go build ./internal/domain/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add internal/domain/types.go
git commit -m "feat: add SourcePriceEvent and SourceStatusEvent domain types"
```

---

## Task 2: Add source price channel with throttle to SnapshotEngine

**Files:**
- Modify: `internal/engine/snapshot.go:40-116` (struct + constructor), `internal/engine/snapshot.go:224-253` (`updateVenueState`)
- Test: `internal/engine/snapshot_source_price_test.go` (new)

- [ ] **Step 1: Write the failing test**

Create `internal/engine/snapshot_source_price_test.go`:

```go
package engine

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/justar9/btick/internal/config"
	"github.com/justar9/btick/internal/domain"
)

// testLogger() is already defined in integration_test.go — reused here.

func TestSnapshotEngine_SourcePriceCh_EmitsOnTrade(t *testing.T) {
	inCh := make(chan domain.RawEvent, 10)
	eng := NewSnapshotEngine(
		config.PricingConfig{
			Mode:                  "median",
			MinimumHealthySources: 1,
			TradeFreshnessWindowMs: 5000,
		},
		"BTC/USD",
		10*time.Second,
		nil,
		inCh,
		testLogger(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	// Send a trade event
	inCh <- domain.RawEvent{
		Source:          "binance",
		SymbolCanonical: "BTC/USD",
		EventType:       "trade",
		ExchangeTS:      time.Now().UTC(),
		RecvTS:          time.Now().UTC(),
		Price:           decimal.NewFromInt(84000),
		Size:            decimal.NewFromFloat(0.1),
		Side:            "buy",
		TradeID:         "t1",
	}

	// Should receive a source price event
	select {
	case evt := <-eng.SourcePriceCh():
		if evt.Source != "binance" {
			t.Errorf("expected source binance, got %s", evt.Source)
		}
		if !evt.Price.Equal(decimal.NewFromInt(84000)) {
			t.Errorf("expected price 84000, got %s", evt.Price)
		}
		if evt.Symbol != "BTC/USD" {
			t.Errorf("expected symbol BTC/USD, got %s", evt.Symbol)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for source price event")
	}
}

func TestSnapshotEngine_SourcePriceCh_ThrottlesPerSource(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)
	eng := NewSnapshotEngine(
		config.PricingConfig{
			Mode:                  "median",
			MinimumHealthySources: 1,
			TradeFreshnessWindowMs: 5000,
		},
		"BTC/USD",
		10*time.Second,
		nil,
		inCh,
		testLogger(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	now := time.Now().UTC()

	// Send 10 rapid trades from the same source
	for i := 0; i < 10; i++ {
		inCh <- domain.RawEvent{
			Source:          "binance",
			SymbolCanonical: "BTC/USD",
			EventType:       "trade",
			ExchangeTS:      now,
			RecvTS:          now,
			Price:           decimal.NewFromInt(int64(84000 + i)),
			Size:            decimal.NewFromFloat(0.1),
			Side:            "buy",
			TradeID:         "t" + string(rune('0'+i)),
		}
	}

	// Should get exactly 1 event (throttled at 500ms)
	select {
	case <-eng.SourcePriceCh():
		// good, got the first one
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first source price event")
	}

	// Should NOT get another immediately (throttled)
	select {
	case <-eng.SourcePriceCh():
		t.Fatal("should not receive another source price within throttle window")
	case <-time.After(100 * time.Millisecond):
		// good, throttled
	}
}

func TestSnapshotEngine_SourcePriceCh_DifferentSourcesNotThrottled(t *testing.T) {
	inCh := make(chan domain.RawEvent, 10)
	eng := NewSnapshotEngine(
		config.PricingConfig{
			Mode:                  "median",
			MinimumHealthySources: 1,
			TradeFreshnessWindowMs: 5000,
		},
		"BTC/USD",
		10*time.Second,
		nil,
		inCh,
		testLogger(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	now := time.Now().UTC()

	// Send trades from two different sources
	inCh <- domain.RawEvent{
		Source:          "binance",
		SymbolCanonical: "BTC/USD",
		EventType:       "trade",
		ExchangeTS:      now,
		RecvTS:          now,
		Price:           decimal.NewFromInt(84000),
		Size:            decimal.NewFromFloat(0.1),
		Side:            "buy",
		TradeID:         "t1",
	}
	inCh <- domain.RawEvent{
		Source:          "coinbase",
		SymbolCanonical: "BTC/USD",
		EventType:       "trade",
		ExchangeTS:      now,
		RecvTS:          now,
		Price:           decimal.NewFromInt(84010),
		Size:            decimal.NewFromFloat(0.2),
		Side:            "sell",
		TradeID:         "t2",
	}

	sources := make(map[string]bool)
	for i := 0; i < 2; i++ {
		select {
		case evt := <-eng.SourcePriceCh():
			sources[evt.Source] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for source price event %d", i+1)
		}
	}

	if !sources["binance"] || !sources["coinbase"] {
		t.Errorf("expected events from both sources, got: %v", sources)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/jt-devnet/btick && go test -run TestSnapshotEngine_SourcePriceCh ./internal/engine/ -v -count=1`
Expected: FAIL — `eng.SourcePriceCh` undefined

- [ ] **Step 3: Add source price channel and throttle to SnapshotEngine**

In `internal/engine/snapshot.go`, add to the struct fields (after line 72, the `snapshotsToWrite` field):

```go
	// Source price broadcast (throttled per source)
	sourcePriceCh     chan domain.SourcePriceEvent
	sourceThrottleMu  sync.Mutex
	sourceLastEmit    map[string]time.Time // per-source last emit time
```

In `NewSnapshotEngine` (around line 101), add to the returned struct (after `snapshotsToWrite`):

```go
		sourcePriceCh:  make(chan domain.SourcePriceEvent, 100),
		sourceLastEmit: make(map[string]time.Time),
```

Add the accessor method after `TickCh()` (after line 122):

```go
// SourcePriceCh returns the channel for throttled per-source price updates.
func (e *SnapshotEngine) SourcePriceCh() <-chan domain.SourcePriceEvent { return e.sourcePriceCh }
```

In `updateVenueState` (line 224), add source price emission after the trade case block. Replace the entire function:

```go
func (e *SnapshotEngine) updateVenueState(evt domain.RawEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()

	vs, ok := e.venueStates[evt.Source]
	if !ok {
		vs = &venueState{}
		e.venueStates[evt.Source] = vs
	}

	switch evt.EventType {
	case "trade":
		vs.lastTrade = &tradeInfo{
			price:   evt.Price,
			ts:      evt.ExchangeTS,
			tradeID: evt.TradeID,
		}
		// Emit canonical tick on each trade event
		e.emitTick(evt)

		// Emit throttled source price update
		e.emitSourcePrice(evt)

	case "ticker":
		vs.lastQuote = &quoteInfo{
			bid: evt.Bid,
			ask: evt.Ask,
			ts:  evt.ExchangeTS,
		}
	}

	e.recordPipelineLatency(evt)
}
```

Add the `emitSourcePrice` method after `updateVenueState`:

```go
const sourceThrottleInterval = 500 * time.Millisecond

func (e *SnapshotEngine) emitSourcePrice(evt domain.RawEvent) {
	e.sourceThrottleMu.Lock()
	last, ok := e.sourceLastEmit[evt.Source]
	now := time.Now()
	if ok && now.Sub(last) < sourceThrottleInterval {
		e.sourceThrottleMu.Unlock()
		return
	}
	e.sourceLastEmit[evt.Source] = now
	e.sourceThrottleMu.Unlock()

	sp := domain.SourcePriceEvent{
		Symbol: e.canonicalSymbol,
		Source: evt.Source,
		Price:  evt.Price,
		TS:     evt.ExchangeTS,
	}

	select {
	case e.sourcePriceCh <- sp:
	default:
		// drop if channel full — non-blocking
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/jt-devnet/btick && go test -run TestSnapshotEngine_SourcePriceCh ./internal/engine/ -v -count=1`
Expected: all 3 tests PASS

- [ ] **Step 5: Run full engine test suite to check for regressions**

Run: `cd /home/jt-devnet/btick && go test ./internal/engine/ -count=1`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/engine/snapshot.go internal/engine/snapshot_source_price_test.go
git commit -m "feat: add throttled source price channel to SnapshotEngine"
```

---

## Task 3: Merge source price channels in MultiEngine

**Files:**
- Modify: `internal/engine/multi.go:11-80`

- [ ] **Step 1: Write the failing test**

Create `internal/engine/multi_source_price_test.go`:

```go
package engine

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/justar9/btick/internal/config"
	"github.com/justar9/btick/internal/domain"
)

func TestMultiEngine_SourcePriceCh_MergesSymbols(t *testing.T) {
	inCh1 := make(chan domain.RawEvent, 10)
	inCh2 := make(chan domain.RawEvent, 10)

	cfg := config.PricingConfig{
		Mode:                   "median",
		MinimumHealthySources:  1,
		TradeFreshnessWindowMs: 5000,
	}

	eng1 := NewSnapshotEngine(cfg, "BTC/USD", 10*time.Second, nil, inCh1, testLogger())
	eng2 := NewSnapshotEngine(cfg, "ETH/USD", 10*time.Second, nil, inCh2, testLogger())

	multi := NewMultiEngine(
		map[string]*SnapshotEngine{"BTC/USD": eng1, "ETH/USD": eng2},
		[]string{"BTC/USD", "ETH/USD"},
	)

	// Manually push a source price event into eng1's channel
	eng1.sourcePriceCh <- domain.SourcePriceEvent{
		Symbol: "BTC/USD",
		Source: "binance",
		Price:  decimal.NewFromInt(84000),
		TS:     time.Now().UTC(),
	}

	select {
	case evt := <-multi.SourcePriceCh():
		if evt.Symbol != "BTC/USD" {
			t.Errorf("expected BTC/USD, got %s", evt.Symbol)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for merged source price event")
	}

	// Push from eng2
	eng2.sourcePriceCh <- domain.SourcePriceEvent{
		Symbol: "ETH/USD",
		Source: "coinbase",
		Price:  decimal.NewFromInt(3200),
		TS:     time.Now().UTC(),
	}

	select {
	case evt := <-multi.SourcePriceCh():
		if evt.Symbol != "ETH/USD" {
			t.Errorf("expected ETH/USD, got %s", evt.Symbol)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for merged source price event from eng2")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/jt-devnet/btick && go test -run TestMultiEngine_SourcePriceCh ./internal/engine/ -v -count=1`
Expected: FAIL — `multi.SourcePriceCh` undefined

- [ ] **Step 3: Add source price merging to MultiEngine**

Replace `internal/engine/multi.go` entirely:

```go
package engine

import (
	"sync"

	"github.com/justar9/btick/internal/domain"
)

// MultiEngine aggregates multiple per-symbol SnapshotEngines behind a single
// interface that the API server can consume.
type MultiEngine struct {
	engines map[string]*SnapshotEngine // keyed by canonical symbol
	symbols []string                   // ordered list of symbols

	snapshotCh    chan domain.Snapshot1s
	tickCh        chan domain.CanonicalTick
	sourcePriceCh chan domain.SourcePriceEvent
}

// NewMultiEngine creates a MultiEngine from a set of per-symbol engines.
// It merges their snapshot, tick, and source price channels into single output channels.
func NewMultiEngine(engines map[string]*SnapshotEngine, symbols []string) *MultiEngine {
	m := &MultiEngine{
		engines:       engines,
		symbols:       symbols,
		snapshotCh:    make(chan domain.Snapshot1s, 100*len(engines)),
		tickCh:        make(chan domain.CanonicalTick, 1000*len(engines)),
		sourcePriceCh: make(chan domain.SourcePriceEvent, 100*len(engines)),
	}

	// Merge per-engine channels into the combined output channels.
	var wg sync.WaitGroup
	for _, eng := range engines {
		e := eng
		wg.Add(3)
		go func() {
			defer wg.Done()
			for snap := range e.SnapshotCh() {
				m.snapshotCh <- snap
			}
		}()
		go func() {
			defer wg.Done()
			for tick := range e.TickCh() {
				m.tickCh <- tick
			}
		}()
		go func() {
			defer wg.Done()
			for sp := range e.SourcePriceCh() {
				m.sourcePriceCh <- sp
			}
		}()
	}

	// Close merged channels once all sources are done.
	go func() {
		wg.Wait()
		close(m.snapshotCh)
		close(m.tickCh)
		close(m.sourcePriceCh)
	}()

	return m
}

// LatestState returns the latest price state for the given symbol.
// Returns nil if symbol is unknown or no data yet.
func (m *MultiEngine) LatestState(symbol string) *domain.LatestState {
	if eng, ok := m.engines[symbol]; ok {
		return eng.LatestState()
	}
	return nil
}

// Symbols returns the ordered list of configured canonical symbols.
func (m *MultiEngine) Symbols() []string {
	return m.symbols
}

// SnapshotCh returns the merged snapshot channel for all symbols.
func (m *MultiEngine) SnapshotCh() <-chan domain.Snapshot1s {
	return m.snapshotCh
}

// TickCh returns the merged tick channel for all symbols.
func (m *MultiEngine) TickCh() <-chan domain.CanonicalTick {
	return m.tickCh
}

// SourcePriceCh returns the merged source price channel for all symbols.
func (m *MultiEngine) SourcePriceCh() <-chan domain.SourcePriceEvent {
	return m.sourcePriceCh
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/jt-devnet/btick && go test -run TestMultiEngine_SourcePriceCh ./internal/engine/ -v -count=1`
Expected: PASS

- [ ] **Step 5: Run full engine test suite**

Run: `cd /home/jt-devnet/btick && go test ./internal/engine/ -count=1`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/engine/multi.go internal/engine/multi_source_price_test.go
git commit -m "feat: merge source price channels in MultiEngine"
```

---

## Task 4: Extend WSMessage and subscriptions for new types

**Files:**
- Modify: `internal/api/websocket.go:19-75`

- [ ] **Step 1: Write the failing test**

Create `internal/api/websocket_source_test.go`:

```go
package api

import (
	"testing"
)

func TestSubscriptions_SourcePriceDefaultOff(t *testing.T) {
	subs := newSubscriptions()
	if subs.wants("source_price") {
		t.Error("source_price should be off by default")
	}
}

func TestSubscriptions_SourceStatusDefaultOff(t *testing.T) {
	subs := newSubscriptions()
	if subs.wants("source_status") {
		t.Error("source_status should be off by default")
	}
}

func TestSubscriptions_SubscribeSourcePrice(t *testing.T) {
	subs := newSubscriptions()
	subs.set("source_price", true)
	if !subs.wants("source_price") {
		t.Error("source_price should be on after subscribe")
	}
}

func TestSubscriptions_SubscribeSourceStatus(t *testing.T) {
	subs := newSubscriptions()
	subs.set("source_status", true)
	if !subs.wants("source_status") {
		t.Error("source_status should be on after subscribe")
	}
}

func TestWSMessage_SourceFields(t *testing.T) {
	msg := WSMessage{
		Type:      "source_price",
		Source:    "binance",
		ConnState: "",
	}
	if msg.Source != "binance" {
		t.Errorf("expected source binance, got %s", msg.Source)
	}

	msg2 := WSMessage{
		Type:      "source_status",
		Source:    "kraken",
		ConnState: "disconnected",
		Stale:     true,
	}
	if msg2.ConnState != "disconnected" {
		t.Errorf("expected conn_state disconnected, got %s", msg2.ConnState)
	}
	if !msg2.Stale {
		t.Error("expected stale true")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/jt-devnet/btick && go test -run "TestSubscriptions_Source|TestWSMessage_Source" ./internal/api/ -v -count=1`
Expected: FAIL — `Source` field undefined on `WSMessage`

- [ ] **Step 3: Add Source, ConnState, Stale to WSMessage and new subscription types**

In `internal/api/websocket.go`, update the `WSMessage` struct (lines 19-31):

```go
// WSMessage is a message broadcast to WebSocket clients.
type WSMessage struct {
	Type         string   `json:"type"`
	Seq          uint64   `json:"seq,omitempty"`
	Symbol       string   `json:"symbol,omitempty"`
	TS           string   `json:"ts"`
	Price        string   `json:"price,omitempty"`
	Basis        string   `json:"basis,omitempty"`
	IsStale      bool     `json:"is_stale,omitempty"`
	QualityScore string   `json:"quality_score,omitempty"`
	SourceCount  int      `json:"source_count,omitempty"`
	SourcesUsed  []string `json:"sources_used,omitempty"`
	Message      string   `json:"message,omitempty"`
	Source       string   `json:"source,omitempty"`
	ConnState    string   `json:"conn_state,omitempty"`
	Stale        bool     `json:"stale,omitempty"`
}
```

Update the `subscriptions` struct (lines 34-39):

```go
// subscriptions tracks which message types a client wants.
type subscriptions struct {
	mu           sync.RWMutex
	snapshot1s   bool
	latestPrice  bool
	heartbeat    bool
	sourcePrice  bool
	sourceStatus bool
}
```

`newSubscriptions` stays the same — `sourcePrice` and `sourceStatus` default to `false` (zero value).

Update `wants` method (lines 49-62):

```go
func (s *subscriptions) wants(msgType string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	switch msgType {
	case "snapshot_1s":
		return s.snapshot1s
	case "latest_price":
		return s.latestPrice
	case "heartbeat":
		return s.heartbeat
	case "source_price":
		return s.sourcePrice
	case "source_status":
		return s.sourceStatus
	default:
		return true // welcome, unknown types always pass
	}
}
```

Update `set` method (lines 64-75):

```go
func (s *subscriptions) set(msgType string, val bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch msgType {
	case "snapshot_1s":
		s.snapshot1s = val
	case "latest_price":
		s.latestPrice = val
	case "heartbeat":
		s.heartbeat = val
	case "source_price":
		s.sourcePrice = val
	case "source_status":
		s.sourceStatus = val
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/jt-devnet/btick && go test -run "TestSubscriptions_Source|TestWSMessage_Source" ./internal/api/ -v -count=1`
Expected: all PASS

- [ ] **Step 5: Run full API test suite**

Run: `cd /home/jt-devnet/btick && go test ./internal/api/ -count=1`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/api/websocket.go internal/api/websocket_source_test.go
git commit -m "feat: extend WSMessage and subscriptions for source_price and source_status"
```

---

## Task 5: Wire source price broadcast into Server.broadcastLoop

**Files:**
- Modify: `internal/api/server.go:16-31` (Engine interface), `internal/api/server.go:141-178` (broadcastLoop)
- Modify: `internal/api/handlers_test.go:58-79` (mockEngine)

- [ ] **Step 1: Write the failing test**

Add to `internal/api/handlers_test.go`, after `TestBroadcastLoop_TickBroadcast` (after line 899):

```go
func TestBroadcastLoop_SourcePriceBroadcast(t *testing.T) {
	eng := newMockEngine(nil)
	s := testServer(nil, eng)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go s.broadcastLoop(ctx)

	eng.sourcePriceCh <- domain.SourcePriceEvent{
		Symbol: "BTC/USD",
		Source: "binance",
		Price:  decimal.NewFromInt(84150),
		TS:     refTime,
	}

	// Give the broadcastLoop time to process
	time.Sleep(50 * time.Millisecond)
	cancel()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/jt-devnet/btick && go test -run TestBroadcastLoop_SourcePriceBroadcast ./internal/api/ -v -count=1`
Expected: FAIL — `sourcePriceCh` not a field of mockEngine

- [ ] **Step 3: Update Engine interface, mockEngine, and broadcastLoop**

In `internal/api/server.go`, update the `Engine` interface (lines 26-31):

```go
// Engine abstracts the snapshot engine used by API handlers.
type Engine interface {
	LatestState(symbol string) *domain.LatestState
	Symbols() []string
	SnapshotCh() <-chan domain.Snapshot1s
	TickCh() <-chan domain.CanonicalTick
	SourcePriceCh() <-chan domain.SourcePriceEvent
}
```

In `internal/api/handlers_test.go`, update `mockEngine` struct (line 58-69):

```go
type mockEngine struct {
	latestState   *domain.LatestState
	snapshotCh    chan domain.Snapshot1s
	tickCh        chan domain.CanonicalTick
	sourcePriceCh chan domain.SourcePriceEvent
}

func newMockEngine(state *domain.LatestState) *mockEngine {
	return &mockEngine{
		latestState:   state,
		snapshotCh:    make(chan domain.Snapshot1s, 10),
		tickCh:        make(chan domain.CanonicalTick, 10),
		sourcePriceCh: make(chan domain.SourcePriceEvent, 10),
	}
}

func (m *mockEngine) LatestState(_ string) *domain.LatestState { return m.latestState }
func (m *mockEngine) Symbols() []string                        { return []string{"BTC/USD"} }
func (m *mockEngine) SnapshotCh() <-chan domain.Snapshot1s     { return m.snapshotCh }
func (m *mockEngine) TickCh() <-chan domain.CanonicalTick      { return m.tickCh }
func (m *mockEngine) SourcePriceCh() <-chan domain.SourcePriceEvent { return m.sourcePriceCh }
```

In `internal/api/server.go`, update `broadcastLoop` (lines 141-178):

```go
func (s *Server) broadcastLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case snap, ok := <-s.engine.SnapshotCh():
			if !ok {
				return
			}
			s.wsHub.Broadcast(WSMessage{
				Type:         "snapshot_1s",
				Symbol:       snap.CanonicalSymbol,
				TS:           snap.TSSecond.Format(time.RFC3339Nano),
				Price:        snap.CanonicalPrice.String(),
				Basis:        snap.Basis,
				IsStale:      snap.IsStale,
				QualityScore: snap.QualityScore.String(),
				SourceCount:  snap.SourceCount,
				SourcesUsed:  snap.SourcesUsed,
			})
		case tick, ok := <-s.engine.TickCh():
			if !ok {
				return
			}
			s.wsHub.Broadcast(WSMessage{
				Type:         "latest_price",
				Symbol:       tick.CanonicalSymbol,
				TS:           tick.TSEvent.Format(time.RFC3339Nano),
				Price:        tick.CanonicalPrice.String(),
				Basis:        tick.Basis,
				IsStale:      tick.IsStale,
				QualityScore: tick.QualityScore.String(),
				SourceCount:  tick.SourceCount,
				SourcesUsed:  tick.SourcesUsed,
			})
		case sp, ok := <-s.engine.SourcePriceCh():
			if !ok {
				return
			}
			s.wsHub.Broadcast(WSMessage{
				Type:   "source_price",
				Symbol: sp.Symbol,
				Source: sp.Source,
				TS:     sp.TS.Format(time.RFC3339Nano),
				Price:  sp.Price.String(),
			})
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/jt-devnet/btick && go test -run TestBroadcastLoop ./internal/api/ -v -count=1`
Expected: all PASS

- [ ] **Step 5: Run full API test suite**

Run: `cd /home/jt-devnet/btick && go test ./internal/api/ -count=1`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/api/server.go internal/api/handlers_test.go
git commit -m "feat: wire source price broadcast into broadcastLoop"
```

---

## Task 6: Add adapter state change callback for source_status

**Files:**
- Modify: `internal/adapter/base.go:15-82`
- Test: `internal/adapter/base_state_callback_test.go` (new)

- [ ] **Step 1: Write the failing test**

Create `internal/adapter/base_state_callback_test.go`:

```go
package adapter

import (
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"
)

func TestBaseAdapter_OnStateChange_CalledOnTransition(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	a := NewBaseAdapter("test", "ws://invalid", 30*time.Second, 0, logger)

	var mu sync.Mutex
	var transitions []string

	a.SetOnStateChange(func(name, oldState, newState string) {
		mu.Lock()
		transitions = append(transitions, oldState+"->"+newState)
		mu.Unlock()
	})

	// Simulate state changes
	a.setConnState("connecting")
	a.setConnState("connected")
	a.setConnState("disconnected")

	mu.Lock()
	defer mu.Unlock()

	if len(transitions) != 3 {
		t.Fatalf("expected 3 transitions, got %d: %v", len(transitions), transitions)
	}
	if transitions[0] != "disconnected->connecting" {
		t.Errorf("expected disconnected->connecting, got %s", transitions[0])
	}
	if transitions[1] != "connecting->connected" {
		t.Errorf("expected connecting->connected, got %s", transitions[1])
	}
	if transitions[2] != "connected->disconnected" {
		t.Errorf("expected connected->disconnected, got %s", transitions[2])
	}
}

func TestBaseAdapter_OnStateChange_NotCalledForSameState(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	a := NewBaseAdapter("test", "ws://invalid", 30*time.Second, 0, logger)

	callCount := 0
	a.SetOnStateChange(func(name, oldState, newState string) {
		callCount++
	})

	a.setConnState("disconnected") // same as initial state
	if callCount != 0 {
		t.Errorf("expected 0 calls for same state, got %d", callCount)
	}
}

func TestBaseAdapter_OnStateChange_IncludesName(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	a := NewBaseAdapter("binance", "ws://invalid", 30*time.Second, 0, logger)

	var gotName string
	a.SetOnStateChange(func(name, oldState, newState string) {
		gotName = name
	})

	a.setConnState("connecting")
	if gotName != "binance" {
		t.Errorf("expected name binance, got %s", gotName)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/jt-devnet/btick && go test -run TestBaseAdapter_OnStateChange ./internal/adapter/ -v -count=1`
Expected: FAIL — `SetOnStateChange` undefined

- [ ] **Step 3: Add state change callback to BaseAdapter**

In `internal/adapter/base.go`, add a field to the struct (after line 25, the `onConnected` field):

```go
	onStateChange   func(name, oldState, newState string)
```

Add the setter after `SetOnConnected` (after line 50):

```go
func (a *BaseAdapter) SetOnStateChange(h func(name, oldState, newState string)) {
	a.onStateChange = h
}
```

Update `setConnState` (lines 78-82) to fire the callback on state transitions:

```go
func (a *BaseAdapter) setConnState(state string) {
	a.mu.Lock()
	old := a.connState
	if state == old {
		a.mu.Unlock()
		return
	}
	a.connState = state
	cb := a.onStateChange
	a.mu.Unlock()

	if cb != nil {
		cb(a.name, old, state)
	}
}
```

Also update the direct assignment in `connectAndRead` (line 133) to use `setConnState` instead. Replace lines 131-136:

```go
	a.mu.Lock()
	a.conn = conn
	a.consecutiveErrs = 0
	a.reconnectCount++
	a.mu.Unlock()
	a.setConnState("connected")
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/jt-devnet/btick && go test -run TestBaseAdapter_OnStateChange ./internal/adapter/ -v -count=1`
Expected: all 3 tests PASS

- [ ] **Step 5: Run full adapter test suite**

Run: `cd /home/jt-devnet/btick && go test ./internal/adapter/ -count=1`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/adapter/base.go internal/adapter/base_state_callback_test.go
git commit -m "feat: add state change callback to BaseAdapter for source_status events"
```

---

## Task 7: Wire adapter state changes to WS hub in main.go

**Files:**
- Modify: `cmd/btick/main.go:146-155` (adapter startup), `internal/api/server.go` (expose hub broadcast)

- [ ] **Step 1: Expose a method on Server to broadcast source status**

Add to `internal/api/server.go`, after the `broadcastLoop` method:

```go
// BroadcastSourceStatus broadcasts a source_status message to WS clients.
func (s *Server) BroadcastSourceStatus(symbol, source, connState string, stale bool) {
	s.wsHub.Broadcast(WSMessage{
		Type:      "source_status",
		Symbol:    symbol,
		Source:    source,
		TS:        time.Now().UTC().Format(time.RFC3339Nano),
		ConnState: connState,
		Stale:     stale,
	})
}
```

- [ ] **Step 2: Wire adapter state changes in main.go**

In `cmd/btick/main.go`, the adapters are started inside `startAdapter` (line 267). We need to pass the broadcast function down. Update the `startAdapter` signature and the call site.

Update the call site (around line 152):

```go
			s := src
			safeGo(&wg, cancel, logger, "adapter-"+canonical+"-"+s.Name, func() {
				startAdapter(ctx, s, canonical, rawCh, onAdapterStateChange, symLogger)
			})
```

Add the `onAdapterStateChange` variable before the adapter loop (after line 127, before the `for _, sym := range cfg.Symbols` loop). This variable will be set after the server is created, so we need to restructure slightly. Instead, use a closure that captures the server reference. Add a shared callback channel approach:

Actually, since the server is created after engines (line 249), and adapters start before that, we need a channel-based approach. Add a shared channel before the symbol loop:

After line 111 (the `writerCh` declaration), add:

```go
	// Channel for adapter state changes → WS broadcast.
	stateChangeCh := make(chan domain.SourceStatusEvent, 100)
```

Update the adapter start call site (around line 152) — pass a callback that sends to the channel:

```go
			s := src
			sym := canonical
			safeGo(&wg, cancel, logger, "adapter-"+canonical+"-"+s.Name, func() {
				startAdapter(ctx, s, sym, rawCh, stateChangeCh, symLogger)
			})
```

Update `startAdapter` function signature and body (line 267). For each adapter case, add the state change callback. Here's the updated function:

```go
func startAdapter(ctx context.Context, src config.SourceConfig, symbol string, outCh chan<- domain.RawEvent, stateChangeCh chan<- domain.SourceStatusEvent, logger *slog.Logger) {
	var base *adapter.BaseAdapter

	switch src.Name {
	case "binance":
		a := adapter.NewBinanceAdapter(
			src.WSURL,
			src.NativeSymbol,
			src.PingInterval(),
			src.MaxConnLifetime(),
			outCh,
			logger,
		)
		base = a.Base()
		a.Run(ctx)

	case "coinbase":
		a := adapter.NewCoinbaseAdapter(
			src.WSURL,
			src.NativeSymbol,
			src.PingInterval(),
			outCh,
			logger,
		)
		base = a.Base()
		a.Run(ctx)

	case "kraken":
		a := adapter.NewKrakenAdapter(
			src.WSURL,
			src.NativeSymbol,
			src.UseTickerFallback,
			src.PingInterval(),
			outCh,
			logger,
		)
		base = a.Base()
		a.Run(ctx)

	case "okx":
		a := adapter.NewOKXAdapter(
			src.WSURL,
			src.NativeSymbol,
			src.PingInterval(),
			outCh,
			logger,
		)
		base = a.Base()
		a.Run(ctx)

	default:
		logger.Error("unknown source", "name", src.Name)
		return
	}

	// This won't run because a.Run(ctx) blocks, so we need to set up the callback
	// BEFORE calling Run. Let me restructure.
	_ = base
}
```

Wait — `a.Run(ctx)` blocks. We need to set the callback BEFORE calling Run. Restructure:

```go
func startAdapter(ctx context.Context, src config.SourceConfig, symbol string, outCh chan<- domain.RawEvent, stateChangeCh chan<- domain.SourceStatusEvent, logger *slog.Logger) {
	stateCallback := func(name, oldState, newState string) {
		evt := domain.SourceStatusEvent{
			Symbol:    symbol,
			Source:    name,
			ConnState: newState,
			Stale:     false, // staleness is detected elsewhere
			TS:        time.Now().UTC(),
		}
		select {
		case stateChangeCh <- evt:
		default:
		}
	}

	switch src.Name {
	case "binance":
		a := adapter.NewBinanceAdapter(
			src.WSURL,
			src.NativeSymbol,
			src.PingInterval(),
			src.MaxConnLifetime(),
			outCh,
			logger,
		)
		a.SetOnStateChange(stateCallback)
		a.Run(ctx)

	case "coinbase":
		a := adapter.NewCoinbaseAdapter(
			src.WSURL,
			src.NativeSymbol,
			src.PingInterval(),
			outCh,
			logger,
		)
		a.SetOnStateChange(stateCallback)
		a.Run(ctx)

	case "kraken":
		a := adapter.NewKrakenAdapter(
			src.WSURL,
			src.NativeSymbol,
			src.UseTickerFallback,
			src.PingInterval(),
			outCh,
			logger,
		)
		a.SetOnStateChange(stateCallback)
		a.Run(ctx)

	case "okx":
		a := adapter.NewOKXAdapter(
			src.WSURL,
			src.NativeSymbol,
			src.PingInterval(),
			outCh,
			logger,
		)
		a.SetOnStateChange(stateCallback)
		a.Run(ctx)

	default:
		logger.Error("unknown source", "name", src.Name)
	}
}
```

This requires each adapter type to expose a `Base()` method. Check if they already do — if not, add it. The adapters likely embed `BaseAdapter` or hold a reference to it. We'll need to verify during implementation and add `Base() *BaseAdapter` if missing.

After the server is created (after line 249), add a goroutine to drain the state change channel:

```go
	// Broadcast adapter state changes over WebSocket.
	safeGo(&wg, cancel, logger, "state-change-broadcaster", func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-stateChangeCh:
				if !ok {
					return
				}
				srv.BroadcastSourceStatus(evt.Symbol, evt.Source, evt.ConnState, evt.Stale)
			}
		}
	})
```

- [ ] **Step 3: Verify it compiles**

Note: All adapters embed `*BaseAdapter`, so `SetOnStateChange` is promoted — no `Base()` accessor method needed.

Run: `cd /home/jt-devnet/btick && go build ./...`
Expected: no errors

- [ ] **Step 4: Run full test suite**

Run: `cd /home/jt-devnet/btick && go test ./... -count=1`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/btick/main.go internal/api/server.go internal/adapter/*.go
git commit -m "feat: wire adapter state changes to WS hub for source_status broadcast"
```

---

## Task 8: Frontend — Expand WS types

**Files:**
- Modify: `web/src/ws/types.ts`

- [ ] **Step 1: Update WS types**

Replace `web/src/ws/types.ts` entirely:

```typescript
export interface WSMessage {
  type: "welcome" | "latest_price" | "snapshot_1s" | "heartbeat" | "source_price" | "source_status";
  seq?: number;
  symbol?: string;
  ts?: string;
  price?: string;
  basis?: string;
  is_stale?: boolean;
  is_degraded?: boolean;
  quality_score?: string;
  source_count?: number;
  sources_used?: string[];
  message?: string;
  source?: string;
  conn_state?: string;
  stale?: boolean;
}

export interface WSClientAction {
  action: "subscribe" | "unsubscribe";
  types: string[];
}

export interface PriceState {
  symbol: string;
  price: string;
  ts: string;
  basis: string;
  isStale: boolean;
  isDegraded: boolean;
  qualityScore: number;
  sourceCount: number;
  sourcesUsed: string[];
}

export interface SourcePrice {
  source: string;
  price: string;
  ts: string;
}

export interface SourceStatus {
  source: string;
  connState: string;
  stale: boolean;
  ts: string;
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/jt-devnet/btick/web && npx tsc --noEmit`
Expected: no errors (or only pre-existing errors unrelated to types)

- [ ] **Step 3: Commit**

```bash
git add web/src/ws/types.ts
git commit -m "feat: add source_price and source_status WS message types"
```

---

## Task 9: Frontend — Expand WS context with source prices, status, and query cache bridge

**Files:**
- Modify: `web/src/ws/context.tsx`
- Modify: `web/src/ws/useWebSocket.ts`
- Modify: `web/src/routes/__root.tsx`

- [ ] **Step 1: Update useWebSocket to subscribe to new types and add reconnect callback**

Replace `web/src/ws/useWebSocket.ts` entirely:

```typescript
import { useRef, useEffect, useCallback } from "react";
import type { WSMessage, WSClientAction } from "./types";

const RECONNECT_BASE = 1000;
const RECONNECT_MAX = 30000;

interface UseWebSocketOptions {
  url: string;
  onMessage: (msg: WSMessage) => void;
  onStatusChange?: (connected: boolean) => void;
  onReconnect?: () => void;
}

export function useWebSocket({ url, onMessage, onStatusChange, onReconnect }: UseWebSocketOptions) {
  const wsRef = useRef<WebSocket | null>(null!);
  const attemptRef = useRef(0);
  const timerRef = useRef<ReturnType<typeof setTimeout>>(undefined!);
  const onMessageRef = useRef(onMessage);
  const onStatusRef = useRef(onStatusChange);
  const onReconnectRef = useRef(onReconnect);
  const wasConnectedRef = useRef(false);

  onMessageRef.current = onMessage;
  onStatusRef.current = onStatusChange;
  onReconnectRef.current = onReconnect;

  const connect = useCallback(() => {
    if (wsRef.current?.readyState === WebSocket.OPEN) return;

    const ws = new WebSocket(url);
    wsRef.current = ws;

    ws.onopen = () => {
      const isReconnect = wasConnectedRef.current;
      attemptRef.current = 0;
      wasConnectedRef.current = true;
      onStatusRef.current?.(true);

      const sub: WSClientAction = {
        action: "subscribe",
        types: ["latest_price", "snapshot_1s", "source_price", "source_status"],
      };
      ws.send(JSON.stringify(sub));

      if (isReconnect) {
        onReconnectRef.current?.();
      }
    };

    ws.onmessage = (e) => {
      try {
        const msg: WSMessage = JSON.parse(e.data);
        onMessageRef.current(msg);
      } catch {
        // ignore malformed messages
      }
    };

    ws.onclose = () => {
      onStatusRef.current?.(false);
      const delay = Math.min(RECONNECT_BASE * 2 ** attemptRef.current, RECONNECT_MAX);
      attemptRef.current++;
      timerRef.current = setTimeout(connect, delay);
    };

    ws.onerror = () => {
      ws.close();
    };
  }, [url]);

  useEffect(() => {
    connect();
    return () => {
      clearTimeout(timerRef.current);
      wsRef.current?.close();
    };
  }, [connect]);
}
```

- [ ] **Step 2: Update WebSocketProvider to move inside QueryClientProvider and handle new message types**

First, update `web/src/routes/__root.tsx` to move `WebSocketProvider` inside `QueryClientProvider`. Since `QueryClientProvider` is in `main.tsx` which wraps `RouterProvider`, and `__root.tsx` renders inside the router, `WebSocketProvider` already has access to `QueryClientProvider`. No change needed to `__root.tsx`.

Now replace `web/src/ws/context.tsx` entirely:

```typescript
import {
  createContext,
  useContext,
  useRef,
  useState,
  useCallback,
  type ReactNode,
} from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useWebSocket } from "./useWebSocket";
import type { WSMessage, PriceState, SourcePrice, SourceStatus } from "./types";

interface WSContextValue {
  prices: Record<string, PriceState>;
  connected: boolean;
  sourcePrices: Record<string, Record<string, SourcePrice>>;
  sourceStatus: Record<string, Record<string, SourceStatus>>;
}

const WSContext = createContext<WSContextValue>({
  prices: {},
  connected: false,
  sourcePrices: {},
  sourceStatus: {},
});

export function usePrice(symbol: string): PriceState | undefined {
  return useContext(WSContext).prices[symbol];
}

export function useWSConnected(): boolean {
  return useContext(WSContext).connected;
}

export function useSourcePrices(
  symbol: string,
): Record<string, SourcePrice> {
  return useContext(WSContext).sourcePrices[symbol] ?? {};
}

export function useSourceStatus(
  symbol: string,
): Record<string, SourceStatus> {
  return useContext(WSContext).sourceStatus[symbol] ?? {};
}

const MAX_LIVE_SNAPSHOTS = 3600;

interface SnapshotPoint {
  ts_second: string;
  price: string;
}

export function WebSocketProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const [connected, setConnected] = useState(false);
  const [prices, setPrices] = useState<Record<string, PriceState>>({});
  const [sourcePrices, setSourcePrices] = useState<
    Record<string, Record<string, SourcePrice>>
  >({});
  const [sourceStatus, setSourceStatus] = useState<
    Record<string, Record<string, SourceStatus>>
  >({});

  const wsUrl = (import.meta.env.VITE_API_URL ?? "")
    .replace(/^http/, "ws")
    + "/ws/price";

  const url = import.meta.env.VITE_API_URL
    ? wsUrl
    : `ws://${window.location.host}/ws/price`;

  const handleMessage = useCallback(
    (msg: WSMessage) => {
      if (msg.type === "latest_price" && msg.symbol && msg.price) {
        const state: PriceState = {
          symbol: msg.symbol,
          price: msg.price,
          ts: msg.ts ?? "",
          basis: msg.basis ?? "",
          isStale: msg.is_stale ?? false,
          isDegraded: msg.is_degraded ?? false,
          qualityScore: parseFloat(msg.quality_score ?? "0"),
          sourceCount: msg.source_count ?? 0,
          sourcesUsed: msg.sources_used ?? [],
        };

        setPrices((prev) => ({ ...prev, [msg.symbol!]: state }));

        // Bridge to TanStack Query cache
        queryClient.setQueryData(["price", "latest", msg.symbol], {
          symbol: msg.symbol,
          ts: msg.ts,
          price: msg.price,
          basis: msg.basis,
          is_stale: msg.is_stale,
          is_degraded: msg.is_degraded,
          quality_score: state.qualityScore,
          source_count: msg.source_count,
          sources_used: msg.sources_used,
        });
      }

      if (msg.type === "snapshot_1s" && msg.symbol && msg.price && msg.ts) {
        // Append to live snapshots query cache
        const point: SnapshotPoint = {
          ts_second: msg.ts,
          price: msg.price,
        };

        queryClient.setQueryData<SnapshotPoint[]>(
          ["price", "snapshots", "live", msg.symbol],
          (old) => {
            const arr = old ?? [];
            const updated = [...arr, point];
            if (updated.length > MAX_LIVE_SNAPSHOTS) {
              return updated.slice(updated.length - MAX_LIVE_SNAPSHOTS);
            }
            return updated;
          },
        );
      }

      if (msg.type === "source_price" && msg.symbol && msg.source && msg.price) {
        setSourcePrices((prev) => ({
          ...prev,
          [msg.symbol!]: {
            ...(prev[msg.symbol!] ?? {}),
            [msg.source!]: {
              source: msg.source!,
              price: msg.price!,
              ts: msg.ts ?? "",
            },
          },
        }));
      }

      if (msg.type === "source_status" && msg.symbol && msg.source) {
        setSourceStatus((prev) => ({
          ...prev,
          [msg.symbol!]: {
            ...(prev[msg.symbol!] ?? {}),
            [msg.source!]: {
              source: msg.source!,
              connState: msg.conn_state ?? "unknown",
              stale: msg.stale ?? false,
              ts: msg.ts ?? "",
            },
          },
        }));
      }
    },
    [queryClient],
  );

  const handleReconnect = useCallback(() => {
    queryClient.invalidateQueries({ queryKey: ["price", "snapshots"] });
  }, [queryClient]);

  useWebSocket({
    url,
    onMessage: handleMessage,
    onStatusChange: setConnected,
    onReconnect: handleReconnect,
  });

  return (
    <WSContext.Provider value={{ prices, connected, sourcePrices, sourceStatus }}>
      {children}
    </WSContext.Provider>
  );
}
```

- [ ] **Step 3: Verify it compiles**

Run: `cd /home/jt-devnet/btick/web && npx tsc --noEmit`
Expected: no errors (or only pre-existing unrelated errors). There may be errors in `SourcePanel.tsx` and `PriceChart.tsx` since they still reference old APIs — those will be fixed in subsequent tasks.

- [ ] **Step 4: Commit**

```bash
git add web/src/ws/context.tsx web/src/ws/useWebSocket.ts
git commit -m "feat: expand WS context with source prices, status, and query cache bridge"
```

---

## Task 10: Frontend — Clean up TanStack Query definitions

**Files:**
- Modify: `web/src/api/queries.ts`
- Modify: `web/src/main.tsx`

- [ ] **Step 1: Update queries.ts**

Replace `web/src/api/queries.ts` entirely:

```typescript
import { queryOptions } from "@tanstack/react-query";
import { api } from "./client";

export function latestPriceOptions(symbol: string) {
  return queryOptions({
    queryKey: ["price", "latest", symbol],
    queryFn: async () => {
      const { data, error } = await api.GET("/v1/price/latest", {
        params: { query: { symbol } },
      });
      if (error) throw error;
      return data;
    },
    staleTime: 30_000,
  });
}

export function snapshotsOptions(start: string, end?: string) {
  return queryOptions({
    queryKey: ["price", "snapshots", start, end],
    queryFn: async () => {
      const { data, error } = await api.GET("/v1/price/snapshots", {
        params: { query: { start, end } },
      });
      if (error) throw error;
      return data;
    },
    staleTime: Infinity,
  });
}

export function ticksOptions(limit?: number) {
  return queryOptions({
    queryKey: ["price", "ticks", limit],
    queryFn: async () => {
      const { data, error } = await api.GET("/v1/price/ticks", {
        params: { query: { limit } },
      });
      if (error) throw error;
      return data;
    },
    staleTime: 2000,
  });
}

export function healthOptions(symbol?: string) {
  return queryOptions({
    queryKey: ["health", symbol],
    queryFn: async () => {
      const { data, error } = await api.GET("/v1/health");
      if (error) throw error;
      return data;
    },
    staleTime: 3000,
  });
}

export function feedHealthOptions() {
  return queryOptions({
    queryKey: ["health", "feeds"],
    queryFn: async () => {
      const { data, error } = await api.GET("/v1/health/feeds");
      if (error) throw error;
      return data;
    },
    staleTime: 30_000,
  });
}

export function symbolsOptions() {
  return queryOptions({
    queryKey: ["symbols"],
    queryFn: async () => {
      const resp = await fetch(
        (import.meta.env.VITE_API_URL ?? "") + "/v1/symbols",
      );
      if (!resp.ok) throw new Error("Failed to fetch symbols");
      return resp.json() as Promise<string[]>;
    },
    staleTime: 60_000,
  });
}
```

Key changes:
- Deleted `rawTicksOptions` entirely
- Removed `refetchInterval` from `feedHealthOptions`, added `staleTime: 30_000`
- Fixed `latestPriceOptions` to pass `symbol` param to API call
- Bumped `latestPriceOptions` staleTime to `30_000`

- [ ] **Step 2: Update main.tsx default staleTime**

In `web/src/main.tsx`, change `staleTime: 5000` to `staleTime: 30_000`:

```typescript
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      refetchOnWindowFocus: true,
      retry: 2,
    },
  },
});
```

- [ ] **Step 3: Verify it compiles**

Run: `cd /home/jt-devnet/btick/web && npx tsc --noEmit`
Expected: errors in `SourcePanel.tsx` referencing deleted `rawTicksOptions` — expected, fixed in next task

- [ ] **Step 4: Commit**

```bash
git add web/src/api/queries.ts web/src/main.tsx
git commit -m "feat: clean up TanStack Query - remove polling, fix params, bump stale times"
```

---

## Task 11: Frontend — Rewire SourcePanel to use WS hooks

**Files:**
- Modify: `web/src/components/SourcePanel.tsx`
- Modify: `web/src/routes/coin.$symbol.tsx`

- [ ] **Step 1: Pass symbol prop to SourcePanel**

In `web/src/routes/coin.$symbol.tsx`, update the SourcePanel usage (line 66):

Change:
```tsx
<SourcePanel price={price} />
```
To:
```tsx
<SourcePanel price={price} symbol={symbol} />
```

- [ ] **Step 2: Rewrite SourcePanel to use WS hooks**

Replace `web/src/components/SourcePanel.tsx` entirely:

```tsx
import { useRef, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useSourcePrices, useSourceStatus } from "@/ws/context";
import { feedHealthOptions } from "@/api/queries";
import type { PriceState } from "@/ws/types";
import styles from "./SourcePanel.module.css";

const EXCHANGES = ["binance", "coinbase", "kraken", "okx"] as const;

const EXCHANGE_META: Record<string, { color: string; abbr: string; label: string }> = {
  binance: { color: "#f0b90b", abbr: "BN", label: "Binance" },
  coinbase: { color: "#0052ff", abbr: "CB", label: "Coinbase" },
  kraken: { color: "#5741d9", abbr: "KR", label: "Kraken" },
  okx: { color: "#000000", abbr: "OX", label: "OKX" },
};

interface Props {
  price: PriceState | undefined;
  symbol: string;
}

export function SourcePanel({ price, symbol }: Props) {
  const sources = useSourcePrices(symbol);
  const statuses = useSourceStatus(symbol);
  const { data: feeds } = useQuery(feedHealthOptions());

  // Feed health per source (for latency only)
  const lagMap = new Map<string, number>();
  if (feeds) {
    for (const f of feeds) {
      const s = f.source ?? "";
      if (s && f.median_lag_ms) {
        lagMap.set(s, f.median_lag_ms);
      }
    }
  }

  const medianPrice = price ? parseFloat(price.price) : 0;

  return (
    <div id="sources" className={styles.wrap}>
      {/* Summary bar */}
      {price && (
        <div className={styles.summary}>
          <div className={styles.summaryItem}>
            <span className={styles.summaryLabel}>Basis</span>
            <span className={styles.summaryValue}>{formatBasis(price.basis)}</span>
          </div>
          <span className={styles.summaryDot} />
          <div className={styles.summaryItem}>
            <span className={styles.summaryLabel}>Quality</span>
            <span className={styles.summaryValue}>{price.qualityScore.toFixed(2)}</span>
          </div>
          <span className={styles.summaryDot} />
          <div className={styles.summaryItem}>
            <span className={styles.summaryLabel}>Sources</span>
            <span className={styles.summaryValue}>{price.sourceCount} active</span>
          </div>
        </div>
      )}

      {/* Source rows */}
      <div className={styles.table}>
        <div className={styles.tableHeader}>
          <span className={styles.colName}>Source</span>
          <span className={styles.colPrice}>Price</span>
          <span className={styles.colDelta}>Delta</span>
          <span className={styles.colLag}>Latency</span>
          <span className={styles.colStatus}>Status</span>
        </div>
        {EXCHANGES.map((name) => {
          const meta = EXCHANGE_META[name];
          const sp = sources[name];
          const p = sp ? parseFloat(sp.price) : null;
          const delta = p !== null && medianPrice ? p - medianPrice : null;
          const status = statuses[name];
          const connected = status ? status.connState === "connected" : false;
          const stale = status?.stale ?? false;

          return (
            <SourceRow
              key={name}
              meta={meta}
              price={p}
              delta={delta}
              lag={lagMap.get(name)}
              connected={connected}
              stale={stale}
            />
          );
        })}
      </div>
    </div>
  );
}

interface SourceRowProps {
  meta: { color: string; abbr: string; label: string };
  price: number | null;
  delta: number | null;
  lag: number | undefined;
  connected: boolean;
  stale: boolean;
}

function SourceRow({ meta, price, delta, lag, connected, stale }: SourceRowProps) {
  const prevPrice = useRef<number | null>(null);
  const [flash, setFlash] = useState<"up" | "down" | null>(null);

  useEffect(() => {
    if (prevPrice.current !== null && price !== null && prevPrice.current !== price) {
      setFlash(price > prevPrice.current ? "up" : "down");
      const t = setTimeout(() => setFlash(null), 500);
      return () => clearTimeout(t);
    }
    prevPrice.current = price;
  }, [price]);

  const statusColor = stale ? "var(--yellow)" : connected ? "var(--green)" : "var(--text-ghost)";

  return (
    <div className={styles.row}>
      <div className={styles.colName}>
        <span className={styles.icon} style={{ backgroundColor: meta.color }}>
          {meta.abbr}
        </span>
        <span className={styles.name}>{meta.label}</span>
      </div>
      <span className={`${styles.colPrice} ${flash === "up" ? styles.flashUp : ""} ${flash === "down" ? styles.flashDown : ""}`}>
        {price !== null
          ? price.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 })
          : "\u2014"}
      </span>
      <span className={`${styles.colDelta} ${delta !== null && delta > 0 ? styles.deltaUp : ""} ${delta !== null && delta < 0 ? styles.deltaDown : ""}`}>
        {delta !== null
          ? `${delta >= 0 ? "+" : ""}${delta.toFixed(2)}`
          : "\u2014"}
      </span>
      <span className={styles.colLag}>
        {lag !== undefined ? `${lag}ms` : "\u2014"}
      </span>
      <span className={styles.colStatus}>
        <span className={styles.statusDot} style={{ backgroundColor: statusColor }} />
      </span>
    </div>
  );
}

function formatBasis(basis: string) {
  return basis.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
}
```

- [ ] **Step 3: Verify it compiles**

Run: `cd /home/jt-devnet/btick/web && npx tsc --noEmit`
Expected: no errors related to SourcePanel

- [ ] **Step 4: Commit**

```bash
git add web/src/components/SourcePanel.tsx web/src/routes/coin.\$symbol.tsx
git commit -m "feat: rewire SourcePanel to use WS source prices and status"
```

---

## Task 12: Frontend — Rewire PriceChart to use TanStack Query

**Files:**
- Modify: `web/src/components/PriceChart.tsx`

- [ ] **Step 1: Rewrite PriceChart to use TanStack Query for backfill + live cache**

Replace `web/src/components/PriceChart.tsx` entirely:

```tsx
import { useState, useCallback, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { Liveline } from "liveline";
import type { LivelinePoint } from "liveline";
import { snapshotsOptions } from "@/api/queries";
import styles from "./PriceChart.module.css";

const WINDOWS = [
  { label: "1m", secs: 60 },
  { label: "5m", secs: 300 },
  { label: "1h", secs: 3600 },
];

interface Props {
  symbol: string;
}

interface SnapshotRow {
  ts_second: string;
  price: string;
}

export function PriceChart({ symbol }: Props) {
  const [windowSecs, setWindowSecs] = useState(300);

  // Compute time range for backfill query
  const start = useMemo(() => {
    const d = new Date(Date.now() - windowSecs * 1000);
    return d.toISOString();
  }, [windowSecs]);

  // Backfill from REST via TanStack Query
  const { data: backfillData } = useQuery(snapshotsOptions(start));

  // Live snapshots from WS (appended by WebSocketProvider via setQueryData)
  // useQuery subscribes to cache updates so the component re-renders on new points
  const { data: liveData } = useQuery<SnapshotRow[]>({
    queryKey: ["price", "snapshots", "live", symbol],
    queryFn: () => [],
    staleTime: Infinity,
    refetchOnMount: false,
    refetchOnWindowFocus: false,
  });

  // Merge backfill + live, deduplicate by rounded timestamp
  const merged = useMemo(() => {
    const backfillPoints: LivelinePoint[] = (backfillData ?? []).map(
      (r: SnapshotRow) => ({
        time: new Date(r.ts_second).getTime() / 1000,
        value: parseFloat(r.price),
      }),
    );

    const livePoints: LivelinePoint[] = (liveData ?? []).map(
      (r: SnapshotRow) => ({
        time: new Date(r.ts_second).getTime() / 1000,
        value: parseFloat(r.price),
      }),
    );

    const seen = new Set<number>();
    const result: LivelinePoint[] = [];
    for (const pt of [...backfillPoints, ...livePoints]) {
      const key = Math.round(pt.time);
      if (!seen.has(key)) {
        seen.add(key);
        result.push(pt);
      }
    }
    result.sort((a, b) => a.time - b.time);
    return result;
  }, [backfillData, liveData]);

  const latest = merged.length > 0 ? merged[merged.length - 1].value : 0;

  // Compute range stats from visible data
  const range = useMemo(() => {
    if (merged.length === 0) return null;
    const now = Date.now() / 1000;
    const cutoff = now - windowSecs;
    const visible = merged.filter((p) => p.time >= cutoff);
    if (visible.length === 0) return null;

    let high = -Infinity;
    let low = Infinity;
    const open = visible[0].value;
    const close = visible[visible.length - 1].value;

    for (const p of visible) {
      if (p.value > high) high = p.value;
      if (p.value < low) low = p.value;
    }

    const change = close - open;
    const changePct = open !== 0 ? (change / open) * 100 : 0;

    return { high, low, open, close, change, changePct };
  }, [merged, windowSecs]);

  const handleWindowChange = useCallback((secs: number) => {
    setWindowSecs(secs);
  }, []);

  const fmt = (v: number) =>
    v.toLocaleString("en-US", {
      minimumFractionDigits: 2,
      maximumFractionDigits: 2,
    });

  return (
    <div id="chart" className={styles.wrap}>
      {merged.length > 0 ? (
        <>
          <Liveline
            data={merged}
            value={latest}
            window={windowSecs}
            windows={WINDOWS}
            windowStyle="rounded"
            onWindowChange={handleWindowChange}
            fill
            grid
            badge
            momentum
            showValue
            formatValue={fmt}
            style={{ height: 220 }}
          />
          {range && (
            <div className={styles.range}>
              <div className={styles.rangeStat}>
                <span className={styles.rangeLabel}>H</span>
                <span className={styles.rangeValue}>{fmt(range.high)}</span>
              </div>
              <div className={styles.rangeStat}>
                <span className={styles.rangeLabel}>L</span>
                <span className={styles.rangeValue}>{fmt(range.low)}</span>
              </div>
              <div className={styles.rangeStat}>
                <span className={styles.rangeLabel}>O</span>
                <span className={styles.rangeValue}>{fmt(range.open)}</span>
              </div>
              <div className={styles.rangeStat}>
                <span className={styles.rangeLabel}>C</span>
                <span className={styles.rangeValue}>{fmt(range.close)}</span>
              </div>
              <div className={styles.rangeStat}>
                <span className={styles.rangeLabel}>Chg</span>
                <span
                  className={`${styles.rangeValue} ${range.change >= 0 ? styles.rangeUp : styles.rangeDown}`}
                >
                  {range.change >= 0 ? "+" : ""}{fmt(range.change)}
                  <span className={styles.rangePct}>
                    {" "}({range.changePct >= 0 ? "+" : ""}{range.changePct.toFixed(2)}%)
                  </span>
                </span>
              </div>
            </div>
          )}
        </>
      ) : (
        <div className={styles.empty}>Waiting for data...</div>
      )}
    </div>
  );
}
```

Key changes:
- Removed raw `fetch()` and `backfill` state
- Removed `fetchedRef` tracking
- Uses `useQuery(snapshotsOptions(start))` for backfill
- Reads live snapshots from query cache via `queryClient.getQueryData`
- `start` is a `useMemo` that recomputes when window changes — triggers new query
- Dedup logic preserved but reads from query cache instead of context state
- Window switch triggers new `start` → new query key → cache miss fetches from REST (or cache hit if previously viewed)

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/jt-devnet/btick/web && npx tsc --noEmit`
Expected: no errors related to PriceChart

- [ ] **Step 3: Commit**

```bash
git add web/src/components/PriceChart.tsx
git commit -m "feat: rewire PriceChart to use TanStack Query for backfill + live cache"
```

---

## Task 13: Frontend — Remove polling from HealthTable

**Files:**
- Modify: `web/src/components/HealthTable.tsx`

- [ ] **Step 1: Verify HealthTable now uses the updated feedHealthOptions (no polling)**

The `HealthTable` component uses `useQuery(feedHealthOptions())`. Since we already removed `refetchInterval` from `feedHealthOptions` in Task 10, the component now fetches on mount only with a 30s stale time. No code change needed in `HealthTable.tsx` itself.

Verify by reading the file:

Run: `cd /home/jt-devnet/btick/web && npx tsc --noEmit`
Expected: no errors

- [ ] **Step 2: Commit (skip if no changes needed)**

If no changes were needed, skip this commit. The feed health query change was already committed in Task 10.

---

## Task 14: Full build and integration verification

**Files:** None (verification only)

- [ ] **Step 1: Run full Go test suite**

Run: `cd /home/jt-devnet/btick && go test ./... -count=1`
Expected: all PASS

- [ ] **Step 2: Build Go binary**

Run: `cd /home/jt-devnet/btick && go build -o btick ./cmd/btick`
Expected: no errors

- [ ] **Step 3: Run frontend type check**

Run: `cd /home/jt-devnet/btick/web && npx tsc --noEmit`
Expected: no errors

- [ ] **Step 4: Build frontend**

Run: `cd /home/jt-devnet/btick/web && npx vite build`
Expected: build succeeds

- [ ] **Step 5: Start dev server and verify in browser**

Run: `cd /home/jt-devnet/btick/web && npx vite dev`

Verify in browser:
- PriceDisplay shows canonical price updating in real-time
- SourcePanel shows per-source prices with deltas updating via WS (no 2s lag)
- Status dots reflect connection state from WS `source_status` messages
- PriceChart loads historical data and appends live points smoothly
- Switching time windows (1m/5m/1h) works, switching back shows cached data instantly
- No `refetchInterval` polling visible in browser DevTools network tab (except initial REST fetches)
- WS messages in DevTools show `source_price` and `source_status` types

- [ ] **Step 6: Final commit if any fixes were needed**

```bash
git add -A
git commit -m "fix: integration fixes from full build verification"
```
