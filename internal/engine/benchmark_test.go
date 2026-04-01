package engine

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/justar9/btick/internal/config"
	"github.com/justar9/btick/internal/domain"
	"github.com/justar9/btick/internal/normalizer"
)

// =============================================================================
// Core Tick Emission Benchmarks
// =============================================================================

func BenchmarkEmitTick(b *testing.B) {
	eng := newBenchmarkEngine()
	events := []domain.RawEvent{
		benchmarkTradeEvent("binance-a", decimal.NewFromInt(84_000)),
		benchmarkTradeEvent("binance-b", decimal.NewFromInt(84_400)),
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		eng.updateVenueState(events[i%len(events)])
		select {
		case <-eng.TickCh():
		default:
			b.Fatal("expected canonical tick")
		}
	}
}

func BenchmarkEmitTick_SamePrice(b *testing.B) {
	eng := newBenchmarkEngine()
	evt := benchmarkTradeEvent("binance-same", decimal.NewFromInt(84_200))

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		eng.updateVenueState(evt)
		// Same price → no tick emitted (deduplicated), drain if present
		select {
		case <-eng.TickCh():
		default:
		}
	}
}

// =============================================================================
// Median Computation Benchmarks
// =============================================================================

func BenchmarkComputeMedian_3Venues(b *testing.B) {
	prices := []decimal.Decimal{
		decimal.NewFromInt(84_000),
		decimal.NewFromInt(84_150),
		decimal.NewFromInt(84_300),
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		computeMedian(prices)
	}
}

func BenchmarkComputeMedian_10Venues(b *testing.B) {
	prices := make([]decimal.Decimal, 10)
	for i := range prices {
		prices[i] = decimal.NewFromInt(int64(84_000 + i*50))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		computeMedian(prices)
	}
}

func BenchmarkComputeMedian_50Venues(b *testing.B) {
	prices := make([]decimal.Decimal, 50)
	for i := range prices {
		prices[i] = decimal.NewFromInt(int64(84_000 + i*10))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		computeMedian(prices)
	}
}

// =============================================================================
// Canonical Computation Benchmarks (includes outlier rejection)
// =============================================================================

func BenchmarkComputeCanonical_4Venues(b *testing.B) {
	eng := &SnapshotEngine{}
	eng.cfg.Mode = "multi_venue_median"
	eng.cfg.MinimumHealthySources = 2
	eng.cfg.OutlierRejectPct = 1.0

	refs := []domain.VenueRefPrice{
		{Source: "binance", RefPrice: decimal.NewFromInt(84_100), Basis: "trade", AgeMs: 50},
		{Source: "coinbase", RefPrice: decimal.NewFromInt(84_200), Basis: "trade", AgeMs: 80},
		{Source: "kraken", RefPrice: decimal.NewFromInt(84_150), Basis: "trade", AgeMs: 100},
		{Source: "okx", RefPrice: decimal.NewFromInt(84_180), Basis: "trade", AgeMs: 60},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eng.computeCanonical(refs)
	}
}

func BenchmarkComputeCanonical_WithOutlier(b *testing.B) {
	eng := &SnapshotEngine{}
	eng.cfg.Mode = "multi_venue_median"
	eng.cfg.MinimumHealthySources = 2
	eng.cfg.OutlierRejectPct = 1.0

	refs := []domain.VenueRefPrice{
		{Source: "binance", RefPrice: decimal.NewFromInt(84_100), Basis: "trade", AgeMs: 50},
		{Source: "coinbase", RefPrice: decimal.NewFromInt(84_200), Basis: "trade", AgeMs: 80},
		{Source: "kraken", RefPrice: decimal.NewFromInt(84_150), Basis: "trade", AgeMs: 100},
		{Source: "okx", RefPrice: decimal.NewFromInt(90_000), Basis: "trade", AgeMs: 60}, // outlier
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eng.computeCanonical(refs)
	}
}

func BenchmarkComputeCanonical_MixedBasis(b *testing.B) {
	eng := &SnapshotEngine{}
	eng.cfg.Mode = "multi_venue_median"
	eng.cfg.MinimumHealthySources = 2
	eng.cfg.OutlierRejectPct = 1.0

	refs := []domain.VenueRefPrice{
		{Source: "binance", RefPrice: decimal.NewFromInt(84_100), Basis: "trade", AgeMs: 50},
		{Source: "coinbase", RefPrice: decimal.NewFromInt(84_200), Basis: "midpoint", AgeMs: 80},
		{Source: "kraken", RefPrice: decimal.NewFromInt(84_150), Basis: "trade", AgeMs: 100},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eng.computeCanonical(refs)
	}
}

// =============================================================================
// Venue Reference Computation Benchmarks
// =============================================================================

func BenchmarkComputeVenueRefs_4Sources(b *testing.B) {
	eng := newBenchmarkEngineNVenues(4)
	now := time.Now().UTC()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eng.computeVenueRefs(now)
	}
}

func BenchmarkComputeVenueRefs_10Sources(b *testing.B) {
	eng := newBenchmarkEngineNVenues(10)
	now := time.Now().UTC()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eng.computeVenueRefs(now)
	}
}

// =============================================================================
// Quality Score Benchmarks
// =============================================================================

func BenchmarkComputeQuality_Fresh(b *testing.B) {
	eng := &SnapshotEngine{}
	eng.cfg.CarryForwardMaxSeconds = 10

	refs := []domain.VenueRefPrice{
		{Source: "binance", Basis: "trade", AgeMs: 50},
		{Source: "coinbase", Basis: "trade", AgeMs: 80},
		{Source: "kraken", Basis: "trade", AgeMs: 100},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eng.computeQuality(refs, false, 0)
	}
}

func BenchmarkComputeQuality_Stale(b *testing.B) {
	eng := &SnapshotEngine{}
	eng.cfg.CarryForwardMaxSeconds = 10

	refs := []domain.VenueRefPrice{
		{Source: "binance", Basis: "trade", AgeMs: 50},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eng.computeQuality(refs, true, 5)
	}
}

// =============================================================================
// Full Pipeline Benchmarks
// =============================================================================

func BenchmarkPipeline(b *testing.B) {
	rawCh := make(chan domain.RawEvent, 1)
	normalizedCh := make(chan domain.RawEvent, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := benchmarkDiscardLogger()
	n := normalizer.New(rawCh, normalizedCh, "BTC/USD", []string{"binance"}, logger)
	eng := newBenchmarkEngine()

	normalizerDone := make(chan struct{})
	go func() {
		defer close(normalizerDone)
		n.Run(ctx)
	}()

	bridgeDone := make(chan struct{})
	go func() {
		defer close(bridgeDone)
		for {
			select {
			case <-ctx.Done():
				return
			case evt := <-normalizedCh:
				eng.updateVenueState(evt)
			}
		}
	}()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		rawCh <- benchmarkPipelineEvent(i)
		<-eng.TickCh()
	}

	b.StopTimer()
	cancel()
	<-normalizerDone
	<-bridgeDone
}

func BenchmarkPipeline_HighVolume(b *testing.B) {
	rawCh := make(chan domain.RawEvent, 256)
	normalizedCh := make(chan domain.RawEvent, 256)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := benchmarkDiscardLogger()
	n := normalizer.New(rawCh, normalizedCh, "BTC/USD", []string{"binance", "coinbase", "kraken", "okx"}, logger)
	eng := newBenchmarkEngine()

	normalizerDone := make(chan struct{})
	go func() {
		defer close(normalizerDone)
		n.Run(ctx)
	}()

	bridgeDone := make(chan struct{})
	go func() {
		defer close(bridgeDone)
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-normalizedCh:
				if !ok {
					return
				}
				eng.updateVenueState(evt)
			}
		}
	}()

	sources := []string{"binance", "coinbase", "kraken", "okx"}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		src := sources[i%len(sources)]
		price := decimal.NewFromInt(int64(84_000 + (i%10)*50))
		now := time.Now().UTC()
		rawCh <- domain.RawEvent{
			Source:       src,
			SymbolNative: "BTCUSDT",
			EventType:    "trade",
			ExchangeTS:   now,
			RecvTS:       now.Add(10 * time.Millisecond),
			Price:        price,
			TradeID:      src + "-" + strconv.Itoa(i),
		}
		// Drain ticks — not every event produces one (price dedup)
		select {
		case <-eng.TickCh():
		default:
		}
	}

	b.StopTimer()
	cancel()
	<-normalizerDone
	<-bridgeDone
}

// =============================================================================
// UpdateVenueState Benchmarks (lock contention)
// =============================================================================

func BenchmarkUpdateVenueState_Trade(b *testing.B) {
	eng := newBenchmarkEngine()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		evt := domain.RawEvent{
			Source:     "binance",
			EventType:  "trade",
			ExchangeTS: time.Now().UTC(),
			RecvTS:     time.Now().UTC(),
			Price:      decimal.NewFromInt(int64(84_000 + i%100)),
			TradeID:    "t-" + strconv.Itoa(i),
		}
		eng.updateVenueState(evt)
		// Drain tick channel
		select {
		case <-eng.TickCh():
		default:
		}
	}
}

func BenchmarkUpdateVenueState_Ticker(b *testing.B) {
	eng := newBenchmarkEngine()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		evt := domain.RawEvent{
			Source:     "binance",
			EventType:  "ticker",
			ExchangeTS: time.Now().UTC(),
			RecvTS:     time.Now().UTC(),
			Bid:        decimal.NewFromInt(int64(84_000 + i%100)),
			Ask:        decimal.NewFromInt(int64(84_010 + i%100)),
		}
		eng.updateVenueState(evt)
	}
}

// =============================================================================
// LatestState Contention Benchmark
// =============================================================================

func BenchmarkLatestState_ReadContention(b *testing.B) {
	eng := newBenchmarkEngine()
	// Seed a state
	eng.updateVenueState(benchmarkTradeEvent("seed", decimal.NewFromInt(84_100)))
	select {
	case <-eng.TickCh():
	default:
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = eng.LatestState()
		}
	})
}

// =============================================================================
// Helpers
// =============================================================================

func newBenchmarkEngine() *SnapshotEngine {
	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  2,
		TradeFreshnessWindowMs: int((30 * time.Minute) / time.Millisecond),
		QuoteFreshnessWindowMs: int((30 * time.Minute) / time.Millisecond),
		OutlierRejectPct:       1.0,
		CarryForwardMaxSeconds: 10,
	}

	now := time.Now().UTC()
	eng := NewSnapshotEngine(cfg, "BTC/USD", time.Hour, nil, nil, benchmarkDiscardLogger())
	eng.venueStates["coinbase"] = &venueState{
		lastTrade: &tradeInfo{
			price: decimal.NewFromInt(84_200),
			ts:    now,
		},
	}
	eng.venueStates["kraken"] = &venueState{
		lastTrade: &tradeInfo{
			price: decimal.NewFromInt(84_300),
			ts:    now,
		},
	}

	return eng
}

func newBenchmarkEngineNVenues(n int) *SnapshotEngine {
	cfg := config.PricingConfig{
		Mode:                   "multi_venue_median",
		MinimumHealthySources:  2,
		TradeFreshnessWindowMs: int((30 * time.Minute) / time.Millisecond),
		QuoteFreshnessWindowMs: int((30 * time.Minute) / time.Millisecond),
		OutlierRejectPct:       1.0,
		CarryForwardMaxSeconds: 10,
	}

	now := time.Now().UTC()
	eng := NewSnapshotEngine(cfg, "BTC/USD", time.Hour, nil, nil, benchmarkDiscardLogger())
	for i := 0; i < n; i++ {
		name := "venue-" + strconv.Itoa(i)
		eng.venueStates[name] = &venueState{
			lastTrade: &tradeInfo{
				price: decimal.NewFromInt(int64(84_000 + i*50)),
				ts:    now,
			},
		}
	}

	return eng
}

func benchmarkTradeEvent(tradeID string, price decimal.Decimal) domain.RawEvent {
	now := time.Now().UTC()
	return domain.RawEvent{
		Source:          "binance",
		SymbolNative:    "BTCUSDT",
		SymbolCanonical: "BTC/USD",
		EventType:       "trade",
		ExchangeTS:      now,
		RecvTS:          now.Add(10 * time.Millisecond),
		Price:           price,
		TradeID:         tradeID,
	}
}

func benchmarkPipelineEvent(i int) domain.RawEvent {
	price := decimal.NewFromInt(84_000)
	if i%2 == 1 {
		price = decimal.NewFromInt(84_400)
	}

	now := time.Now().UTC()
	return domain.RawEvent{
		Source:       "binance",
		SymbolNative: "BTCUSDT",
		EventType:    "trade",
		ExchangeTS:   now,
		RecvTS:       now.Add(10 * time.Millisecond),
		Price:        price,
		TradeID:      "trade-" + strconv.Itoa(i),
	}
}

func benchmarkDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
