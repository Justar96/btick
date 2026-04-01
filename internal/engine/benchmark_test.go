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
