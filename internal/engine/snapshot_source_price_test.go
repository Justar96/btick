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
			Mode:                   "median",
			MinimumHealthySources:  1,
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
			Mode:                   "median",
			MinimumHealthySources:  1,
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
			Mode:                   "median",
			MinimumHealthySources:  1,
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
