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
// Long-Running / 24-Hour Simulation Tests
// These tests simulate real-world scenarios over extended periods
// =============================================================================

func TestSnapshotEngine_SimulatedHour(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long-running test in short mode")
	}

	inCh := make(chan domain.RawEvent, 10000)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  2,
		TradeFreshnessWindowMs: 2000,
		QuoteFreshnessWindowMs: 1000,
		OutlierRejectPct:       1.0,
		CarryForwardMaxSeconds: 10,
		LateArrivalGraceMs:     250,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 3*time.Second, nil, inCh, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go eng.Run(ctx)

	// Metrics
	var (
		ticksEmitted     int64
		snapshotsEmitted int64
		eventsProcessed  int64
	)

	// Drain ticks and snapshots
	go func() {
		for range eng.TickCh() {
			atomic.AddInt64(&ticksEmitted, 1)
		}
	}()
	go func() {
		for range eng.SnapshotCh() {
			atomic.AddInt64(&snapshotsEmitted, 1)
		}
	}()

	// Simulate 1 minute of trading (scaled down from 1 hour)
	// Real scenario: ~50 trades/sec across 3 exchanges
	// Test: 10 trades/sec for 60 seconds
	testDuration := 60 * time.Second
	tradesPerSecond := 10
	sources := []string{"binance", "coinbase", "kraken"}

	start := time.Now()
	ticker := time.NewTicker(time.Second / time.Duration(tradesPerSecond))
	defer ticker.Stop()

	basePrice := 84000.0
	priceVar := 0.0

loop:
	for range ticker.C {
		if time.Since(start) >= testDuration {
			break loop
		}

		// Random walk price
		priceVar += (float64(time.Now().UnixNano()%100) - 50) / 10.0
		price := basePrice + priceVar

		// Pick source
		source := sources[time.Now().UnixNano()%3]

		inCh <- domain.RawEvent{
			Source:          source,
			SymbolCanonical: "BTC/USD",
			EventType:       "trade",
			ExchangeTS:      time.Now(),
			RecvTS:          time.Now(),
			Price:           decimal.NewFromFloat(price),
			TradeID:         string(rune(atomic.LoadInt64(&eventsProcessed))),
		}
		atomic.AddInt64(&eventsProcessed, 1)
	}

	cancel()
	time.Sleep(500 * time.Millisecond)

	ticks := atomic.LoadInt64(&ticksEmitted)
	snapshots := atomic.LoadInt64(&snapshotsEmitted)
	events := atomic.LoadInt64(&eventsProcessed)

	t.Logf("Simulated %.0f seconds of trading:", time.Since(start).Seconds())
	t.Logf("  Events processed: %d", events)
	t.Logf("  Ticks emitted: %d", ticks)
	t.Logf("  Snapshots emitted: %d", snapshots)
	t.Logf("  Tick rate: %.1f%%", float64(ticks)/float64(events)*100)

	// Validate
	if ticks == 0 {
		t.Error("should have emitted ticks")
	}
	if snapshots == 0 {
		t.Error("should have emitted snapshots")
	}

	// Verify state
	state := eng.LatestState()
	if state == nil {
		t.Error("latest state should not be nil")
	}
}

func TestSnapshotEngine_IntermittentSource(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long-running test in short mode")
	}

	inCh := make(chan domain.RawEvent, 1000)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  2,
		TradeFreshnessWindowMs: 1000,
		QuoteFreshnessWindowMs: 500,
		CarryForwardMaxSeconds: 10,
		LateArrivalGraceMs:     100,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 3*time.Second, nil, inCh, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go eng.Run(ctx)

	// Drain channels
	go func() {
		for range eng.TickCh() {
		}
	}()
	go func() {
		for range eng.SnapshotCh() {
		}
	}()

	// Simulate intermittent Kraken (goes offline every 5s for 2s)
	var wg sync.WaitGroup

	// Binance - always online
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			select {
			case <-ctx.Done():
				return
			default:
				inCh <- domain.RawEvent{
					Source:     "binance",
					EventType:  "trade",
					ExchangeTS: time.Now(),
					Price:      decimal.NewFromFloat(84100.00),
					TradeID:    "b" + string(rune(i)),
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}()

	// Coinbase - always online
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			select {
			case <-ctx.Done():
				return
			default:
				inCh <- domain.RawEvent{
					Source:     "coinbase",
					EventType:  "trade",
					ExchangeTS: time.Now(),
					Price:      decimal.NewFromFloat(84150.00),
					TradeID:    "c" + string(rune(i)),
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}()

	// Kraken - intermittent
	wg.Add(1)
	go func() {
		defer wg.Done()
		online := true
		toggleTicker := time.NewTicker(2 * time.Second)
		defer toggleTicker.Stop()

		for i := 0; i < 50; i++ {
			select {
			case <-ctx.Done():
				return
			case <-toggleTicker.C:
				online = !online
			default:
				if online {
					inCh <- domain.RawEvent{
						Source:     "kraken",
						EventType:  "trade",
						ExchangeTS: time.Now(),
						Price:      decimal.NewFromFloat(84120.00),
						TradeID:    "k" + string(rune(i)),
					}
				}
				time.Sleep(200 * time.Millisecond)
			}
		}
	}()

	wg.Wait()
	time.Sleep(500 * time.Millisecond)
	cancel()

	// System should still be functional
	state := eng.LatestState()
	if state == nil {
		t.Error("should have latest state despite intermittent source")
	}
}

func TestSnapshotEngine_PriceSpike(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  2,
		TradeFreshnessWindowMs: 2000,
		OutlierRejectPct:       1.0, // 1% outlier rejection
		CarryForwardMaxSeconds: 10,
		LateArrivalGraceMs:     50,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 3*time.Second, nil, inCh, testLogger())

	// Drain ticks
	var ticks []domain.CanonicalTick
	var mu sync.Mutex
	go func() {
		for tick := range eng.TickCh() {
			mu.Lock()
			ticks = append(ticks, tick)
			mu.Unlock()
		}
	}()

	now := time.Now()

	// Normal prices from all sources
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
		Price:      decimal.NewFromFloat(84120.00),
		TradeID:    "3",
	})

	time.Sleep(50 * time.Millisecond)

	// Sudden spike on one exchange (flash crash / manipulation attempt)
	eng.updateVenueState(domain.RawEvent{
		Source:     "kraken",
		EventType:  "trade",
		ExchangeTS: now.Add(100 * time.Millisecond),
		Price:      decimal.NewFromFloat(70000.00), // -17% spike!
		TradeID:    "4",
	})

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	lastTick := ticks[len(ticks)-1]
	mu.Unlock()

	// Price should NOT have crashed - outlier rejection should filter the spike
	// Median should still be around 84100-84150
	if lastTick.CanonicalPrice.LessThan(decimal.NewFromFloat(80000.00)) {
		t.Errorf("outlier rejection failed, price crashed to %s", lastTick.CanonicalPrice)
	}

	t.Logf("After spike: price=%s, sources=%d", lastTick.CanonicalPrice, lastTick.SourceCount)
}

func TestSnapshotEngine_AllSourcesRecover(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  2,
		TradeFreshnessWindowMs: 500, // Short freshness for faster test
		CarryForwardMaxSeconds: 2,
		LateArrivalGraceMs:     50,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 3*time.Second, nil, inCh, testLogger())

	// Drain ticks
	go func() {
		for range eng.TickCh() {
		}
	}()

	// Initial state with all sources
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

	// Verify healthy state
	state := eng.LatestState()
	if state == nil {
		t.Fatal("should have state")
	}
	if state.IsDegraded {
		t.Error("should not be degraded with 2 sources")
	}

	// Wait for data to go stale (>500ms)
	time.Sleep(600 * time.Millisecond)

	eng.mu.RLock()
	refs := eng.computeVenueRefs(time.Now())
	eng.mu.RUnlock()

	if len(refs) != 0 {
		t.Errorf("refs should be empty after staleness, got %d", len(refs))
	}

	// Sources recover
	recoveryTime := time.Now()
	eng.updateVenueState(domain.RawEvent{
		Source:     "binance",
		EventType:  "trade",
		ExchangeTS: recoveryTime,
		Price:      decimal.NewFromFloat(84200.00),
		TradeID:    "3",
	})
	eng.updateVenueState(domain.RawEvent{
		Source:     "coinbase",
		EventType:  "trade",
		ExchangeTS: recoveryTime,
		Price:      decimal.NewFromFloat(84250.00),
		TradeID:    "4",
	})

	time.Sleep(50 * time.Millisecond)

	// Should be back to healthy
	state = eng.LatestState()
	if state == nil {
		t.Fatal("should have state after recovery")
	}
	if state.IsDegraded {
		t.Error("should not be degraded after recovery")
	}

	// Verify carry-forward state was reset
	eng.mu.RLock()
	carryStart := eng.carryStart
	eng.mu.RUnlock()

	if !carryStart.IsZero() {
		t.Error("carryStart should be reset after recovery")
	}
}

func TestSnapshotEngine_MemoryStability(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping memory test in short mode")
	}

	inCh := make(chan domain.RawEvent, 10000)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  1,
		TradeFreshnessWindowMs: 5000,
		CarryForwardMaxSeconds: 10,
		LateArrivalGraceMs:     50,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 3*time.Second, nil, inCh, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go eng.Run(ctx)

	// Drain channels
	go func() {
		for range eng.TickCh() {
		}
	}()
	go func() {
		for range eng.SnapshotCh() {
		}
	}()

	// Process many events
	numEvents := 100000
	sources := []string{"binance", "coinbase", "kraken"}

	for i := 0; i < numEvents; i++ {
		source := sources[i%3]
		eng.updateVenueState(domain.RawEvent{
			Source:          source,
			SymbolCanonical: "BTC/USD",
			EventType:       "trade",
			ExchangeTS:      time.Now(),
			RecvTS:          time.Now(),
			Price:           decimal.NewFromFloat(84100.00 + float64(i%100)),
			TradeID:         string(rune(i % 1000)),
		})

		if i%10000 == 0 {
			// Periodically check state is accessible
			state := eng.LatestState()
			if state == nil && i > 0 {
				t.Errorf("state nil at iteration %d", i)
			}
		}
	}

	cancel()

	// Verify venue states didn't grow unboundedly
	eng.mu.RLock()
	venueCount := len(eng.venueStates)
	eng.mu.RUnlock()

	if venueCount != 3 {
		t.Errorf("expected 3 venue states, got %d", venueCount)
	}
}

// =============================================================================
// Concurrent Access Under Load
// =============================================================================

func TestSnapshotEngine_ConcurrentAccessUnderLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in short mode")
	}

	inCh := make(chan domain.RawEvent, 10000)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  1,
		TradeFreshnessWindowMs: 5000,
		CarryForwardMaxSeconds: 10,
		LateArrivalGraceMs:     50,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 3*time.Second, nil, inCh, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go eng.Run(ctx)

	// Drain channels
	go func() {
		for range eng.TickCh() {
		}
	}()
	go func() {
		for range eng.SnapshotCh() {
		}
	}()

	var wg sync.WaitGroup

	// Writers - multiple goroutines sending events
	for w := 0; w < 5; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sources := []string{"binance", "coinbase", "kraken"}
			for {
				select {
				case <-ctx.Done():
					return
				default:
					inCh <- domain.RawEvent{
						Source:          sources[id%3],
						SymbolCanonical: "BTC/USD",
						EventType:       "trade",
						ExchangeTS:      time.Now(),
						RecvTS:          time.Now(),
						Price:           decimal.NewFromFloat(84100.00),
						TradeID:         string(rune(id)),
					}
					time.Sleep(time.Millisecond)
				}
			}
		}(w)
	}

	// Readers - multiple goroutines reading state
	for r := 0; r < 10; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					_ = eng.LatestState()
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}

	wg.Wait()
	// Test passes if no race/panic
}

// =============================================================================
// Settlement Accuracy Test
// =============================================================================

func TestSnapshotEngine_SettlementAccuracy(t *testing.T) {
	inCh := make(chan domain.RawEvent, 1000)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  2,
		TradeFreshnessWindowMs: 5000,
		OutlierRejectPct:       1.0,
		CarryForwardMaxSeconds: 10,
		LateArrivalGraceMs:     100,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 3*time.Second, nil, inCh, testLogger())

	// Send consistent prices from all venues directly (not through channel)
	basePrice := decimal.NewFromFloat(84125.00)

	// Update venue states directly for deterministic testing
	for i := 0; i < 10; i++ {
		now := time.Now()

		eng.updateVenueState(domain.RawEvent{
			Source:          "binance",
			SymbolCanonical: "BTC/USD",
			EventType:       "trade",
			ExchangeTS:      now,
			Price:           basePrice,
			TradeID:         "b" + string(rune(i)),
		})
		eng.updateVenueState(domain.RawEvent{
			Source:          "coinbase",
			SymbolCanonical: "BTC/USD",
			EventType:       "trade",
			ExchangeTS:      now,
			Price:           basePrice,
			TradeID:         "c" + string(rune(i)),
		})
		eng.updateVenueState(domain.RawEvent{
			Source:          "kraken",
			SymbolCanonical: "BTC/USD",
			EventType:       "trade",
			ExchangeTS:      now,
			Price:           basePrice,
			TradeID:         "k" + string(rune(i)),
		})
	}

	// Drain ticks
	for {
		select {
		case <-eng.TickCh():
		default:
			goto done
		}
	}
done:

	// Check venue refs
	eng.mu.RLock()
	refs := eng.computeVenueRefs(time.Now())
	eng.mu.RUnlock()

	if len(refs) != 3 {
		t.Errorf("expected 3 venue refs, got %d", len(refs))
	}

	// Verify canonical price computation
	price, basis, isDegraded, _ := eng.computeCanonical(refs)

	if !price.Equal(basePrice) {
		t.Errorf("expected price %s, got %s", basePrice, price)
	}

	if basis != "median_trade" {
		t.Errorf("expected median_trade, got %s", basis)
	}

	if isDegraded {
		t.Error("should not be degraded with 3 sources")
	}

	t.Logf("Settlement accuracy test: price=%s, sources=%d, basis=%s", price, len(refs), basis)
}

