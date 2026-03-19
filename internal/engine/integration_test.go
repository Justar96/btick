package engine

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/justar9/btc-price-tick/internal/config"
	"github.com/justar9/btc-price-tick/internal/domain"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestSnapshotEngine_EndToEnd_SingleVenue(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  2,
		TradeFreshnessWindowMs: 5000,
		QuoteFreshnessWindowMs: 3000,
		OutlierRejectPct:       1.0,
		CarryForwardMaxSeconds: 10,
		LateArrivalGraceMs:     100,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	// Directly call updateVenueState to test without Run() loop
	evt := domain.RawEvent{
		Source:          "binance",
		SymbolNative:    "BTCUSDT",
		SymbolCanonical: "BTC/USD",
		EventType:       "trade",
		ExchangeTS:      time.Now(),
		RecvTS:          time.Now(),
		Price:           decimal.NewFromFloat(84100.00),
		Size:            decimal.NewFromFloat(0.5),
		Side:            "buy",
		TradeID:         "test-1",
	}
	eng.updateVenueState(evt)

	// Should receive a tick
	select {
	case tick := <-eng.TickCh():
		if !tick.CanonicalPrice.Equal(decimal.NewFromFloat(84100.00)) {
			t.Errorf("expected price 84100, got %s", tick.CanonicalPrice)
		}
		if tick.SourceCount != 1 {
			t.Errorf("expected 1 source, got %d", tick.SourceCount)
		}
		if tick.Basis != "single_trade" {
			t.Errorf("expected single_trade basis, got %s", tick.Basis)
		}
		if !tick.IsDegraded {
			t.Error("should be degraded with only 1 source (min 2)")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for tick")
	}

	// Check latest state
	state := eng.LatestState()
	if state == nil {
		t.Fatal("latest state should not be nil")
	}
	if !state.Price.Equal(decimal.NewFromFloat(84100.00)) {
		t.Errorf("expected latest price 84100, got %s", state.Price)
	}
}

func TestSnapshotEngine_EndToEnd_MultiVenueMedian(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  2,
		TradeFreshnessWindowMs: 5000,
		QuoteFreshnessWindowMs: 3000,
		OutlierRejectPct:       1.0,
		CarryForwardMaxSeconds: 10,
		LateArrivalGraceMs:     100,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	now := time.Now()

	// Send trades from 3 venues directly
	events := []domain.RawEvent{
		{
			Source:          "binance",
			SymbolCanonical: "BTC/USD",
			EventType:       "trade",
			ExchangeTS:      now,
			RecvTS:          now,
			Price:           decimal.NewFromFloat(84100.00),
			TradeID:         "b-1",
		},
		{
			Source:          "coinbase",
			SymbolCanonical: "BTC/USD",
			EventType:       "trade",
			ExchangeTS:      now,
			RecvTS:          now,
			Price:           decimal.NewFromFloat(84200.00),
			TradeID:         "c-1",
		},
		{
			Source:          "kraken",
			SymbolCanonical: "BTC/USD",
			EventType:       "trade",
			ExchangeTS:      now,
			RecvTS:          now,
			Price:           decimal.NewFromFloat(84150.00),
			TradeID:         "k-1",
		},
	}

	for _, e := range events {
		eng.updateVenueState(e)
	}

	// Drain all ticks and keep the last one
	var finalTick domain.CanonicalTick
	timeout := time.After(100 * time.Millisecond)
loop:
	for {
		select {
		case tick := <-eng.TickCh():
			finalTick = tick
		case <-timeout:
			break loop
		}
	}

	// The last tick should have 3 sources (all venues updated)
	if finalTick.SourceCount < 2 {
		t.Fatalf("expected at least 2 sources, got %d", finalTick.SourceCount)
	}

	// Median of 84100, 84150, 84200 = 84150
	if !finalTick.CanonicalPrice.Equal(decimal.NewFromFloat(84150.00)) {
		t.Errorf("expected median price 84150, got %s", finalTick.CanonicalPrice)
	}

	if finalTick.Basis != "median_trade" {
		t.Errorf("expected median_trade basis, got %s", finalTick.Basis)
	}

	if finalTick.IsDegraded {
		t.Error("should not be degraded with 3 sources (min 2)")
	}
}

func TestSnapshotEngine_EndToEnd_MidpointFallback(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  2,
		TradeFreshnessWindowMs: 1000, // 1 second freshness
		QuoteFreshnessWindowMs: 3000,
		OutlierRejectPct:       1.0,
		CarryForwardMaxSeconds: 10,
		LateArrivalGraceMs:     100,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	now := time.Now()

	// Send fresh trade from binance
	eng.updateVenueState(domain.RawEvent{
		Source:          "binance",
		SymbolCanonical: "BTC/USD",
		EventType:       "trade",
		ExchangeTS:      now,
		RecvTS:          now,
		Price:           decimal.NewFromFloat(84100.00),
		TradeID:         "b-1",
	})

	// Send ticker (quote) from coinbase - will be used as midpoint fallback
	eng.updateVenueState(domain.RawEvent{
		Source:          "coinbase",
		SymbolCanonical: "BTC/USD",
		EventType:       "ticker",
		ExchangeTS:      now,
		RecvTS:          now,
		Bid:             decimal.NewFromFloat(84180.00),
		Ask:             decimal.NewFromFloat(84220.00),
	})

	// Now send another trade to trigger tick computation
	eng.updateVenueState(domain.RawEvent{
		Source:          "kraken",
		SymbolCanonical: "BTC/USD",
		EventType:       "trade",
		ExchangeTS:      now,
		RecvTS:          now,
		Price:           decimal.NewFromFloat(84150.00),
		TradeID:         "k-1",
	})

	// Drain ticks
	var lastTick domain.CanonicalTick
	timeout := time.After(100 * time.Millisecond)
loop:
	for {
		select {
		case tick := <-eng.TickCh():
			lastTick = tick
		case <-timeout:
			break loop
		}
	}

	// Should have sources: binance (trade), kraken (trade)
	// Coinbase ticker is available but trades take priority
	if lastTick.SourceCount < 2 {
		t.Errorf("expected at least 2 sources, got %d", lastTick.SourceCount)
	}
}

func TestSnapshotEngine_EndToEnd_OutlierRejection(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  2,
		TradeFreshnessWindowMs: 5000,
		QuoteFreshnessWindowMs: 3000,
		OutlierRejectPct:       1.0, // 1% threshold
		CarryForwardMaxSeconds: 10,
		LateArrivalGraceMs:     100,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	now := time.Now()

	// Send trades with one outlier - use updateVenueState directly
	events := []domain.RawEvent{
		{
			Source:          "binance",
			SymbolCanonical: "BTC/USD",
			EventType:       "trade",
			ExchangeTS:      now,
			RecvTS:          now,
			Price:           decimal.NewFromFloat(84100.00),
			TradeID:         "b-1",
		},
		{
			Source:          "coinbase",
			SymbolCanonical: "BTC/USD",
			EventType:       "trade",
			ExchangeTS:      now,
			RecvTS:          now,
			Price:           decimal.NewFromFloat(84150.00),
			TradeID:         "c-1",
		},
		{
			Source:          "kraken",
			SymbolCanonical: "BTC/USD",
			EventType:       "trade",
			ExchangeTS:      now,
			RecvTS:          now,
			Price:           decimal.NewFromFloat(90000.00), // >1% outlier
			TradeID:         "k-1",
		},
	}

	for _, e := range events {
		eng.updateVenueState(e)
	}

	// Drain all ticks and keep the last one
	var finalTick domain.CanonicalTick
	timeout := time.After(100 * time.Millisecond)
loop:
	for {
		select {
		case tick := <-eng.TickCh():
			finalTick = tick
		case <-timeout:
			break loop
		}
	}

	// After outlier rejection, median of 84100, 84150 = 84125
	expected := decimal.NewFromFloat(84125.00)
	if !finalTick.CanonicalPrice.Equal(expected) {
		t.Errorf("expected price %s after outlier rejection, got %s", expected, finalTick.CanonicalPrice)
	}
}

func TestSnapshotEngine_EndToEnd_PriceChangeEmitsTick(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  1,
		TradeFreshnessWindowMs: 5000,
		QuoteFreshnessWindowMs: 3000,
		OutlierRejectPct:       0,
		CarryForwardMaxSeconds: 10,
		LateArrivalGraceMs:     100,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	now := time.Now()

	// First trade
	eng.updateVenueState(domain.RawEvent{
		Source:          "binance",
		SymbolCanonical: "BTC/USD",
		EventType:       "trade",
		ExchangeTS:      now,
		RecvTS:          now,
		Price:           decimal.NewFromFloat(84100.00),
		TradeID:         "1",
	})

	select {
	case tick := <-eng.TickCh():
		if !tick.CanonicalPrice.Equal(decimal.NewFromFloat(84100.00)) {
			t.Errorf("expected first tick price 84100, got %s", tick.CanonicalPrice)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for first tick")
	}

	// Same price trade - should NOT emit tick
	eng.updateVenueState(domain.RawEvent{
		Source:          "binance",
		SymbolCanonical: "BTC/USD",
		EventType:       "trade",
		ExchangeTS:      now,
		RecvTS:          now,
		Price:           decimal.NewFromFloat(84100.00),
		TradeID:         "2",
	})

	select {
	case <-eng.TickCh():
		t.Fatal("should not emit tick for same price")
	case <-time.After(50 * time.Millisecond):
		// expected
	}

	// Different price - should emit tick
	eng.updateVenueState(domain.RawEvent{
		Source:          "binance",
		SymbolCanonical: "BTC/USD",
		EventType:       "trade",
		ExchangeTS:      now,
		RecvTS:          now,
		Price:           decimal.NewFromFloat(84200.00),
		TradeID:         "3",
	})

	select {
	case tick := <-eng.TickCh():
		if !tick.CanonicalPrice.Equal(decimal.NewFromFloat(84200.00)) {
			t.Errorf("expected second tick price 84200, got %s", tick.CanonicalPrice)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for second tick")
	}
}

func TestSnapshotEngine_ConcurrentEventProcessing(t *testing.T) {
	inCh := make(chan domain.RawEvent, 1000)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  1,
		TradeFreshnessWindowMs: 5000,
		QuoteFreshnessWindowMs: 3000,
		OutlierRejectPct:       0,
		CarryForwardMaxSeconds: 10,
		LateArrivalGraceMs:     100,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	// Drain ticks in background
	var tickCount int
	var tickMu sync.Mutex
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-eng.TickCh():
				tickMu.Lock()
				tickCount++
				tickMu.Unlock()
			case <-done:
				return
			}
		}
	}()

	// Send events concurrently from multiple goroutines
	var wg sync.WaitGroup
	sources := []string{"binance", "coinbase", "kraken"}
	eventsPerSource := 100

	for _, source := range sources {
		wg.Add(1)
		go func(src string) {
			defer wg.Done()
			for i := 0; i < eventsPerSource; i++ {
				evt := domain.RawEvent{
					Source:          src,
					SymbolCanonical: "BTC/USD",
					EventType:       "trade",
					ExchangeTS:      time.Now(),
					RecvTS:          time.Now(),
					Price:           decimal.NewFromFloat(84100.00 + float64(i)),
					TradeID:         src + "-" + string(rune('0'+i%10)),
				}
				eng.updateVenueState(evt)
			}
		}(source)
	}

	wg.Wait()
	time.Sleep(50 * time.Millisecond) // let ticks drain
	close(done)

	tickMu.Lock()
	count := tickCount
	tickMu.Unlock()

	// Should have received some ticks (exact count depends on price changes)
	if count == 0 {
		t.Error("expected some ticks to be emitted")
	}

	// Verify latest state is accessible
	state := eng.LatestState()
	if state == nil {
		t.Error("latest state should not be nil after processing events")
	}
}

func TestSnapshotEngine_LatestStateThreadSafe(t *testing.T) {
	inCh := make(chan domain.RawEvent, 100)

	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  1,
		TradeFreshnessWindowMs: 5000,
		QuoteFreshnessWindowMs: 3000,
		CarryForwardMaxSeconds: 10,
		LateArrivalGraceMs:     100,
	}

	eng := NewSnapshotEngine(cfg, "BTC/USD", 5*time.Second, nil, inCh, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go eng.Run(ctx)

	// Concurrent reads and writes
	var wg sync.WaitGroup

	// Writer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			inCh <- domain.RawEvent{
				Source:          "binance",
				SymbolCanonical: "BTC/USD",
				EventType:       "trade",
				ExchangeTS:      time.Now(),
				RecvTS:          time.Now(),
				Price:           decimal.NewFromFloat(84100.00 + float64(i)),
				TradeID:         string(rune('A' + i%26)),
			}
			time.Sleep(time.Millisecond)
		}
	}()

	// Readers
	for r := 0; r < 5; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_ = eng.LatestState()
				time.Sleep(time.Millisecond)
			}
		}()
	}

	wg.Wait()
	// Test passes if no race condition panic
}

func TestSnapshotEngine_QualityScoreDecay(t *testing.T) {
	eng := &SnapshotEngine{
		cfg: config.PricingConfig{
			CarryForwardMaxSeconds: 10,
		},
	}

	refs := []domain.VenueRefPrice{
		{Source: "binance", Basis: "trade", AgeMs: 100},
	}

	// Fresh data should have high quality
	freshScore := eng.computeQuality(refs, false, 0)
	if freshScore.LessThan(decimal.NewFromFloat(0.5)) {
		t.Errorf("fresh data should have quality >= 0.5, got %s", freshScore)
	}

	// Stale data should have lower quality
	staleScore := eng.computeQuality(refs, true, 0)
	if staleScore.GreaterThan(decimal.NewFromFloat(0.3)) {
		t.Errorf("stale data should have quality <= 0.3, got %s", staleScore)
	}

	// Stale with decay should have even lower quality
	decayedScore := eng.computeQuality(refs, true, 5)
	if decayedScore.GreaterThanOrEqual(staleScore) {
		t.Errorf("decayed score %s should be less than fresh stale score %s", decayedScore, staleScore)
	}

	// Fully decayed should approach zero
	fullyDecayed := eng.computeQuality(refs, true, 10)
	if fullyDecayed.GreaterThan(decimal.NewFromFloat(0.01)) {
		t.Errorf("fully decayed should be near zero, got %s", fullyDecayed)
	}
}
