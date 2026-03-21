package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/justar9/btick/internal/config"
	"github.com/justar9/btick/internal/domain"
)

// =============================================================================
// Carry-Forward and Staleness Tests
// =============================================================================

func TestSnapshotEngine_CarryForward_WhenNoFreshData(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  2,
		TradeFreshnessWindowMs: 500, // 500ms freshness - data goes stale quickly
		QuoteFreshnessWindowMs: 300,
		CarryForwardMaxSeconds: 10,
		LateArrivalGraceMs:     50,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	// Send initial trade
	eng.updateVenueState(domain.RawEvent{
		Source:          "binance",
		SymbolCanonical: "BTC/USD",
		EventType:       "trade",
		ExchangeTS:      time.Now(),
		RecvTS:          time.Now(),
		Price:           decimal.NewFromFloat(84100.00),
		TradeID:         "1",
	})

	// Drain tick
	select {
	case <-eng.TickCh():
	case <-time.After(100 * time.Millisecond):
	}

	// Wait for data to become stale
	time.Sleep(600 * time.Millisecond)

	// Get venue refs - should be empty since data is stale
	eng.mu.RLock()
	refs := eng.computeVenueRefs(time.Now())
	lastCanonical := eng.lastCanonical
	eng.mu.RUnlock()

	if len(refs) != 0 {
		t.Errorf("expected 0 refs (stale data), got %d", len(refs))
	}

	// lastCanonical should still have the old price for carry-forward
	if !lastCanonical.Equal(decimal.NewFromFloat(84100.00)) {
		t.Errorf("expected lastCanonical 84100, got %s", lastCanonical)
	}
}

func TestSnapshotEngine_CarryForward_MaxDurationWarning(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  1,
		TradeFreshnessWindowMs: 100,
		CarryForwardMaxSeconds: 1, // Very short for testing
		LateArrivalGraceMs:     10,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	// Set up initial state
	eng.mu.Lock()
	eng.lastCanonical = decimal.NewFromFloat(84100.00)
	eng.carryStart = time.Now().Add(-2 * time.Second) // Started carrying 2s ago
	eng.mu.Unlock()

	// computeQuality should reflect the decay
	refs := []domain.VenueRefPrice{}
	quality := eng.computeQuality(refs, true, 2) // 2 seconds of carry

	// Quality should be very low (near zero) after exceeding max
	if quality.GreaterThan(decimal.NewFromFloat(0.1)) {
		t.Errorf("quality should be near zero after exceeding carry max, got %s", quality)
	}
}

func TestSnapshotEngine_StalenessDetection_PerVenue(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  2,
		TradeFreshnessWindowMs: 1000,
		QuoteFreshnessWindowMs: 500,
		CarryForwardMaxSeconds: 10,
		LateArrivalGraceMs:     50,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	now := time.Now()

	// Fresh trade from binance
	eng.updateVenueState(domain.RawEvent{
		Source:     "binance",
		EventType:  "trade",
		ExchangeTS: now,
		Price:      decimal.NewFromFloat(84100.00),
		TradeID:    "1",
	})

	// Stale trade from coinbase (1.5s old, exceeds 1s freshness)
	eng.updateVenueState(domain.RawEvent{
		Source:     "coinbase",
		EventType:  "trade",
		ExchangeTS: now.Add(-1500 * time.Millisecond),
		Price:      decimal.NewFromFloat(84200.00),
		TradeID:    "2",
	})

	eng.mu.RLock()
	refs := eng.computeVenueRefs(now)
	eng.mu.RUnlock()

	// Should only have 1 ref (binance), coinbase is stale
	if len(refs) != 1 {
		t.Errorf("expected 1 ref (only fresh binance), got %d", len(refs))
	}
	if len(refs) > 0 && refs[0].Source != "binance" {
		t.Errorf("expected binance source, got %s", refs[0].Source)
	}
}

// =============================================================================
// Time Boundary Tests (Important for 24hr operation)
// =============================================================================

func TestSnapshotEngine_SecondBoundaryAlignment(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  1,
		TradeFreshnessWindowMs: 5000,
		LateArrivalGraceMs:     100,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	ctx, cancel := context.WithCancel(context.Background())

	// Collect snapshots
	var snapshots []domain.Snapshot1s
	var mu sync.Mutex
	go func() {
		for {
			select {
			case s := <-eng.SnapshotCh():
				mu.Lock()
				snapshots = append(snapshots, s)
				mu.Unlock()
			case <-ctx.Done():
				return
			}
		}
	}()

	go eng.Run(ctx)

	// Feed some data
	for i := 0; i < 5; i++ {
		inCh <- domain.RawEvent{
			Source:          "binance",
			SymbolCanonical: "BTC/USD",
			EventType:       "trade",
			ExchangeTS:      time.Now(),
			RecvTS:          time.Now(),
			Price:           decimal.NewFromFloat(84100.00 + float64(i)),
			TradeID:         string(rune('A' + i)),
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Wait for snapshots
	time.Sleep(2 * time.Second)
	cancel()

	mu.Lock()
	defer mu.Unlock()

	// Verify snapshots are on second boundaries
	for _, s := range snapshots {
		if s.TSSecond.Nanosecond() != 0 {
			t.Errorf("snapshot not on second boundary: %v", s.TSSecond)
		}
	}
}

func TestSnapshotEngine_MidnightCrossover(t *testing.T) {
	// Simulate events around midnight boundary
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  1,
		TradeFreshnessWindowMs: 5000,
		LateArrivalGraceMs:     50,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	// Create timestamps around midnight
	midnight := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	beforeMidnight := midnight.Add(-500 * time.Millisecond)
	afterMidnight := midnight.Add(500 * time.Millisecond)

	// Event before midnight
	eng.updateVenueState(domain.RawEvent{
		Source:     "binance",
		EventType:  "trade",
		ExchangeTS: beforeMidnight,
		Price:      decimal.NewFromFloat(84100.00),
		TradeID:    "before",
	})

	// Event after midnight
	eng.updateVenueState(domain.RawEvent{
		Source:     "coinbase",
		EventType:  "trade",
		ExchangeTS: afterMidnight,
		Price:      decimal.NewFromFloat(84200.00),
		TradeID:    "after",
	})

	// Both should be available for a snapshot at midnight + 1s
	eng.mu.RLock()
	refs := eng.computeVenueRefs(midnight.Add(time.Second))
	eng.mu.RUnlock()

	if len(refs) != 2 {
		t.Errorf("expected 2 refs around midnight, got %d", len(refs))
	}
}

// =============================================================================
// Settlement Price Edge Cases
// =============================================================================

func TestSnapshotEngine_FiveMinuteBoundary(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  2,
		TradeFreshnessWindowMs: 5000,
		LateArrivalGraceMs:     50,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	// Simulate 5-minute boundary at 09:10:00
	boundary := time.Date(2026, 3, 19, 9, 10, 0, 0, time.UTC)

	// Events just before settlement
	eng.updateVenueState(domain.RawEvent{
		Source:     "binance",
		EventType:  "trade",
		ExchangeTS: boundary.Add(-100 * time.Millisecond),
		Price:      decimal.NewFromFloat(84100.00),
		TradeID:    "b1",
	})
	eng.updateVenueState(domain.RawEvent{
		Source:     "coinbase",
		EventType:  "trade",
		ExchangeTS: boundary.Add(-50 * time.Millisecond),
		Price:      decimal.NewFromFloat(84150.00),
		TradeID:    "c1",
	})
	eng.updateVenueState(domain.RawEvent{
		Source:     "kraken",
		EventType:  "trade",
		ExchangeTS: boundary.Add(-200 * time.Millisecond),
		Price:      decimal.NewFromFloat(84120.00),
		TradeID:    "k1",
	})

	eng.mu.RLock()
	refs := eng.computeVenueRefs(boundary)
	eng.mu.RUnlock()

	if len(refs) != 3 {
		t.Errorf("expected 3 refs at settlement boundary, got %d", len(refs))
	}

	// Verify median calculation
	price, basis, isDegraded, _ := eng.computeCanonical(refs)

	// Median of 84100, 84120, 84150 = 84120
	if !price.Equal(decimal.NewFromFloat(84120.00)) {
		t.Errorf("expected settlement price 84120, got %s", price)
	}
	if basis != "median_trade" {
		t.Errorf("expected median_trade, got %s", basis)
	}
	if isDegraded {
		t.Error("should not be degraded with 3 sources")
	}
}

// =============================================================================
// High Throughput / Stress Tests
// =============================================================================

func TestSnapshotEngine_HighThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping high throughput test in short mode")
	}

	inCh := make(chan domain.RawEvent, 10000)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  1,
		TradeFreshnessWindowMs: 5000,
		LateArrivalGraceMs:     50,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	go eng.Run(ctx)

	// Drain ticks
	var tickCount int64
	go func() {
		for range eng.TickCh() {
			atomic.AddInt64(&tickCount, 1)
		}
	}()

	// Simulate high-frequency trading: 1000 events/sec for 5 seconds
	sources := []string{"binance", "coinbase", "kraken"}
	eventsPerSecond := 1000
	durationSec := 5

	start := time.Now()
	var wg sync.WaitGroup

	for _, source := range sources {
		wg.Add(1)
		go func(src string) {
			defer wg.Done()
			for i := 0; i < eventsPerSecond*durationSec/len(sources); i++ {
				inCh <- domain.RawEvent{
					Source:          src,
					SymbolCanonical: "BTC/USD",
					EventType:       "trade",
					ExchangeTS:      time.Now(),
					RecvTS:          time.Now(),
					Price:           decimal.NewFromFloat(84100.00 + float64(i%100)),
					TradeID:         src + "-" + string(rune('A'+i%26)),
				}
				time.Sleep(time.Millisecond)
			}
		}(source)
	}

	wg.Wait()
	elapsed := time.Since(start)
	cancel()

	count := atomic.LoadInt64(&tickCount)
	t.Logf("Processed %d ticks in %v (%.0f ticks/sec)", count, elapsed, float64(count)/elapsed.Seconds())

	// Verify latest state is valid
	state := eng.LatestState()
	if state == nil {
		t.Error("latest state should not be nil after high throughput")
	}
}

func TestSnapshotEngine_BurstTraffic(t *testing.T) {
	inCh := make(chan domain.RawEvent, 10000)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  1,
		TradeFreshnessWindowMs: 5000,
		LateArrivalGraceMs:     50,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	// Drain ticks
	go func() {
		for range eng.TickCh() {
		}
	}()

	// Send burst of events
	burstSize := 1000
	for i := 0; i < burstSize; i++ {
		eng.updateVenueState(domain.RawEvent{
			Source:          "binance",
			SymbolCanonical: "BTC/USD",
			EventType:       "trade",
			ExchangeTS:      time.Now(),
			RecvTS:          time.Now(),
			Price:           decimal.NewFromFloat(84100.00 + float64(i)),
			TradeID:         string(rune(i)),
		})
	}

	// Verify no panics and state is accessible
	state := eng.LatestState()
	if state == nil {
		t.Error("state should be set after burst")
	}
}

// =============================================================================
// Outlier Edge Cases
// =============================================================================

func TestSnapshotEngine_AllOutliers(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  1,
		TradeFreshnessWindowMs: 5000,
		OutlierRejectPct:       0.1, // Very tight 0.1% threshold
		LateArrivalGraceMs:     50,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	now := time.Now()

	// All prices are within 0.1% of each other
	eng.updateVenueState(domain.RawEvent{
		Source:     "binance",
		EventType:  "trade",
		ExchangeTS: now,
		Price:      decimal.NewFromFloat(84100.00),
		TradeID:    "1",
	})
	eng.updateVenueState(domain.RawEvent{
		Source:     "coinbase",
		EventType:  "trade",
		ExchangeTS: now,
		Price:      decimal.NewFromFloat(84105.00), // Within 0.1%
		TradeID:    "2",
	})
	eng.updateVenueState(domain.RawEvent{
		Source:     "kraken",
		EventType:  "trade",
		ExchangeTS: now,
		Price:      decimal.NewFromFloat(84110.00), // Within 0.1%
		TradeID:    "3",
	})

	eng.mu.RLock()
	refs := eng.computeVenueRefs(now.Add(100 * time.Millisecond))
	eng.mu.RUnlock()

	_, _, _, rejected := eng.computeCanonical(refs)

	// None should be rejected since all are within threshold
	if rejected != 0 {
		t.Errorf("expected 0 rejected, got %d", rejected)
	}
}

func TestSnapshotEngine_SingleOutlier(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  1,
		TradeFreshnessWindowMs: 5000,
		OutlierRejectPct:       1.0, // 1% threshold
		LateArrivalGraceMs:     50,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	now := time.Now()

	eng.updateVenueState(domain.RawEvent{
		Source:     "binance",
		EventType:  "trade",
		ExchangeTS: now,
		Price:      decimal.NewFromFloat(84100.00),
		TradeID:    "1",
	})
	eng.updateVenueState(domain.RawEvent{
		Source:     "coinbase",
		EventType:  "trade",
		ExchangeTS: now,
		Price:      decimal.NewFromFloat(84150.00),
		TradeID:    "2",
	})
	eng.updateVenueState(domain.RawEvent{
		Source:     "kraken",
		EventType:  "trade",
		ExchangeTS: now,
		Price:      decimal.NewFromFloat(100000.00), // >1% outlier
		TradeID:    "3",
	})

	eng.mu.RLock()
	refs := eng.computeVenueRefs(now.Add(100 * time.Millisecond))
	eng.mu.RUnlock()

	price, _, _, rejected := eng.computeCanonical(refs)

	if rejected != 1 {
		t.Errorf("expected 1 rejected outlier, got %d", rejected)
	}

	// Median of 84100, 84150 = 84125
	expected := decimal.NewFromFloat(84125.00)
	if !price.Equal(expected) {
		t.Errorf("expected price %s, got %s", expected, price)
	}
}

// =============================================================================
// Graceful Shutdown Tests
// =============================================================================

func TestSnapshotEngine_GracefulShutdown(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  1,
		TradeFreshnessWindowMs: 5000,
		LateArrivalGraceMs:     50,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	ctx, cancel := context.WithCancel(context.Background())

	// Start engine
	engineDone := make(chan struct{})
	go func() {
		eng.Run(ctx)
		close(engineDone)
	}()

	// Send some events
	for i := 0; i < 10; i++ {
		inCh <- domain.RawEvent{
			Source:          "binance",
			SymbolCanonical: "BTC/USD",
			EventType:       "trade",
			ExchangeTS:      time.Now(),
			RecvTS:          time.Now(),
			Price:           decimal.NewFromFloat(84100.00),
			TradeID:         string(rune('A' + i)),
		}
	}

	// Wait a bit
	time.Sleep(100 * time.Millisecond)

	// Cancel and verify graceful shutdown
	cancel()

	select {
	case <-engineDone:
		// Good, engine shut down
	case <-time.After(2 * time.Second):
		t.Fatal("engine did not shut down within timeout")
	}
}

func TestSnapshotEngine_ShutdownDrainsTickWriter(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  1,
		TradeFreshnessWindowMs: 5000,
		LateArrivalGraceMs:     50,
	}

	// No DB - will queue ticks but not write them
	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	ctx, cancel := context.WithCancel(context.Background())

	go eng.Run(ctx)

	// Send events rapidly
	for i := 0; i < 100; i++ {
		inCh <- domain.RawEvent{
			Source:          "binance",
			SymbolCanonical: "BTC/USD",
			EventType:       "trade",
			ExchangeTS:      time.Now(),
			RecvTS:          time.Now(),
			Price:           decimal.NewFromFloat(84100.00 + float64(i)),
			TradeID:         string(rune(i)),
		}
	}

	// Quick shutdown
	cancel()

	// Should not hang
	time.Sleep(500 * time.Millisecond)
}

// =============================================================================
// Empty / Zero State Tests
// =============================================================================

func TestSnapshotEngine_NoDataAtStart(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  2,
		TradeFreshnessWindowMs: 5000,
		LateArrivalGraceMs:     50,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	// No data sent - check computeVenueRefs
	eng.mu.RLock()
	refs := eng.computeVenueRefs(time.Now())
	eng.mu.RUnlock()

	if len(refs) != 0 {
		t.Errorf("expected 0 refs with no data, got %d", len(refs))
	}

	// computeCanonical should handle empty refs
	price, basis, isDegraded, _ := eng.computeCanonical(refs)

	if !price.IsZero() {
		t.Errorf("expected zero price with no data, got %s", price)
	}
	if basis != "none" {
		t.Errorf("expected basis 'none', got %s", basis)
	}
	if !isDegraded {
		t.Error("should be degraded with no sources")
	}
}

func TestSnapshotEngine_LatestStateNilBeforeFirstEvent(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  1,
		TradeFreshnessWindowMs: 5000,
		LateArrivalGraceMs:     50,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	state := eng.LatestState()
	if state != nil {
		t.Error("latest state should be nil before first event")
	}
}

// =============================================================================
// Quality Score Edge Cases
// =============================================================================

func TestSnapshotEngine_QualityScore_MaxSources(t *testing.T) {
	eng := &SnapshotEngine{
		cfg: config.PricingConfig{
			CarryForwardMaxSeconds: 10,
		},
	}

	// 3 sources (max) with fresh data should give highest quality
	refs := []domain.VenueRefPrice{
		{Source: "binance", Basis: "trade", AgeMs: 50},
		{Source: "coinbase", Basis: "trade", AgeMs: 50},
		{Source: "kraken", Basis: "trade", AgeMs: 50},
	}

	score := eng.computeQuality(refs, false, 0)

	// Should be high quality (>0.9)
	if score.LessThan(decimal.NewFromFloat(0.9)) {
		t.Errorf("3 fresh trade sources should have quality >= 0.9, got %s", score)
	}
}

func TestSnapshotEngine_QualityScore_MidpointPenalty(t *testing.T) {
	eng := &SnapshotEngine{
		cfg: config.PricingConfig{
			CarryForwardMaxSeconds: 10,
		},
	}

	// Same setup but with midpoints instead of trades
	tradeRefs := []domain.VenueRefPrice{
		{Source: "binance", Basis: "trade", AgeMs: 50},
		{Source: "coinbase", Basis: "trade", AgeMs: 50},
	}

	midpointRefs := []domain.VenueRefPrice{
		{Source: "binance", Basis: "midpoint", AgeMs: 50},
		{Source: "coinbase", Basis: "midpoint", AgeMs: 50},
	}

	tradeScore := eng.computeQuality(tradeRefs, false, 0)
	midpointScore := eng.computeQuality(midpointRefs, false, 0)

	if midpointScore.GreaterThanOrEqual(tradeScore) {
		t.Errorf("midpoint score %s should be less than trade score %s", midpointScore, tradeScore)
	}
}

func TestSnapshotEngine_QualityScore_AgePenalty(t *testing.T) {
	eng := &SnapshotEngine{
		cfg: config.PricingConfig{
			CarryForwardMaxSeconds: 10,
		},
	}

	freshRefs := []domain.VenueRefPrice{
		{Source: "binance", Basis: "trade", AgeMs: 50},
	}

	oldRefs := []domain.VenueRefPrice{
		{Source: "binance", Basis: "trade", AgeMs: 1500},
	}

	freshScore := eng.computeQuality(freshRefs, false, 0)
	oldScore := eng.computeQuality(oldRefs, false, 0)

	if oldScore.GreaterThanOrEqual(freshScore) {
		t.Errorf("old data score %s should be less than fresh score %s", oldScore, freshScore)
	}
}

// =============================================================================
// Semaphore / Backpressure Tests
// =============================================================================

func TestSnapshotEngine_FinalizeSemaphore_Backpressure(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  1,
		TradeFreshnessWindowMs: 5000,
		LateArrivalGraceMs:     10,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	// Fill the semaphore
	for i := 0; i < 10; i++ {
		eng.finalizeSem <- struct{}{}
	}

	// Next acquire should fail (non-blocking check in actual code)
	select {
	case eng.finalizeSem <- struct{}{}:
		t.Fatal("should not be able to acquire when semaphore is full")
	default:
		// Expected
	}

	// Drain semaphore
	for i := 0; i < 10; i++ {
		<-eng.finalizeSem
	}
}

func TestSnapshotEngine_TickWriteBuffer_Backpressure(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  1,
		TradeFreshnessWindowMs: 5000,
		LateArrivalGraceMs:     10,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	// Fill the tick write buffer
	for i := 0; i < 500; i++ {
		eng.ticksToWrite <- domain.CanonicalTick{}
	}

	// Next write should be dropped (non-blocking)
	select {
	case eng.ticksToWrite <- domain.CanonicalTick{}:
		t.Fatal("should not be able to write when buffer is full")
	default:
		// Expected
	}

	// Drain buffer
	for i := 0; i < 500; i++ {
		<-eng.ticksToWrite
	}
}

