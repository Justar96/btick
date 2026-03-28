package normalizer

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/justar9/btick/internal/domain"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestNormalizer_AssignsUUID(t *testing.T) {
	inCh := make(chan domain.RawEvent, 1)
	outCh := make(chan domain.RawEvent, 1)
	n := New(inCh, outCh, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go n.Run(ctx)

	evt := domain.RawEvent{
		Source:       "binance",
		SymbolNative: "BTCUSDT",
		EventType:    "trade",
		Price:        decimal.NewFromFloat(84100.00),
		TradeID:      "123",
	}
	inCh <- evt

	select {
	case out := <-outCh:
		if out.EventID == uuid.Nil {
			t.Error("expected UUID to be assigned")
		}
		// UUID v7 should be time-ordered
		if out.EventID.Version() != 7 {
			t.Errorf("expected UUID v7, got version %d", out.EventID.Version())
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for normalized event")
	}
}

func TestNormalizer_DeduplicatesTrades(t *testing.T) {
	inCh := make(chan domain.RawEvent, 10)
	outCh := make(chan domain.RawEvent, 10)
	n := New(inCh, outCh, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go n.Run(ctx)

	// Send same trade twice
	evt := domain.RawEvent{
		Source:       "binance",
		SymbolNative: "BTCUSDT",
		EventType:    "trade",
		Price:        decimal.NewFromFloat(84100.00),
		TradeID:      "duplicate-123",
	}
	inCh <- evt
	inCh <- evt

	// Should only receive one
	select {
	case <-outCh:
		// first one received
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for first event")
	}

	select {
	case <-outCh:
		t.Fatal("should not receive duplicate trade")
	case <-time.After(50 * time.Millisecond):
		// expected - no duplicate
	}
}

func TestNormalizer_DifferentSourcesSameTradeID(t *testing.T) {
	inCh := make(chan domain.RawEvent, 10)
	outCh := make(chan domain.RawEvent, 10)
	n := New(inCh, outCh, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go n.Run(ctx)

	// Same trade ID but different sources should both pass
	evt1 := domain.RawEvent{
		Source:    "binance",
		EventType: "trade",
		TradeID:   "123",
		Price:     decimal.NewFromFloat(84100.00),
	}
	evt2 := domain.RawEvent{
		Source:    "coinbase",
		EventType: "trade",
		TradeID:   "123",
		Price:     decimal.NewFromFloat(84100.00),
	}
	inCh <- evt1
	inCh <- evt2

	received := 0
	timeout := time.After(200 * time.Millisecond)
loop1:
	for i := 0; i < 2; i++ {
		select {
		case <-outCh:
			received++
		case <-timeout:
			break loop1
		}
	}

	if received != 2 {
		t.Errorf("expected 2 events (different sources), got %d", received)
	}
}

func TestNormalizer_TickerEventsNotDeduplicated(t *testing.T) {
	inCh := make(chan domain.RawEvent, 10)
	outCh := make(chan domain.RawEvent, 10)
	n := New(inCh, outCh, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go n.Run(ctx)

	// Ticker events should not be deduplicated
	evt := domain.RawEvent{
		Source:    "binance",
		EventType: "ticker",
		Bid:       decimal.NewFromFloat(84100.00),
		Ask:       decimal.NewFromFloat(84110.00),
	}
	inCh <- evt
	inCh <- evt

	received := 0
	timeout := time.After(200 * time.Millisecond)
loop2:
	for i := 0; i < 2; i++ {
		select {
		case <-outCh:
			received++
		case <-timeout:
			break loop2
		}
	}

	if received != 2 {
		t.Errorf("expected 2 ticker events (no dedup), got %d", received)
	}
}

func TestNormalizer_MapsCanonicalSymbol(t *testing.T) {
	tests := []struct {
		source       string
		nativeSymbol string
		wantCanon    string
	}{
		{"binance", "BTCUSDT", "BTC/USD"},
		{"coinbase", "BTC-USD", "BTC/USD"},
		{"kraken", "BTC/USD", "BTC/USD"},
		{"okx", "BTC-USDT", "BTC/USD"},
		{"unknown", "XYZ", "BTC/USD"},
	}

	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			inCh := make(chan domain.RawEvent, 1)
			outCh := make(chan domain.RawEvent, 1)
			n := New(inCh, outCh, testLogger())

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go n.Run(ctx)

			evt := domain.RawEvent{
				Source:       tt.source,
				SymbolNative: tt.nativeSymbol,
				EventType:    "trade",
				TradeID:      "unique-" + tt.source,
				Price:        decimal.NewFromFloat(84100.00),
			}
			inCh <- evt

			select {
			case out := <-outCh:
				if out.SymbolCanonical != tt.wantCanon {
					t.Errorf("expected canonical %s, got %s", tt.wantCanon, out.SymbolCanonical)
				}
			case <-time.After(100 * time.Millisecond):
				t.Fatal("timeout")
			}
		})
	}
}

func TestNormalizer_RingBufferEviction(t *testing.T) {
	inCh := make(chan domain.RawEvent, 200)
	outCh := make(chan domain.RawEvent, 200)

	// Create normalizer with small buffer for testing
	n := &Normalizer{
		inCh:    inCh,
		outCh:   outCh,
		logger:  testLogger(),
		maxSeen: 10,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go n.Run(ctx)

	// Send 15 unique trades
	for i := 0; i < 15; i++ {
		evt := domain.RawEvent{
			Source:    "binance",
			EventType: "trade",
			TradeID:   string(rune('A' + i)),
			Price:     decimal.NewFromFloat(84100.00),
		}
		inCh <- evt
	}

	// Drain output
	for i := 0; i < 15; i++ {
		select {
		case <-outCh:
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("timeout at event %d", i)
		}
	}

	// Now send first trade again - should pass (was evicted)
	evt := domain.RawEvent{
		Source:    "binance",
		EventType: "trade",
		TradeID:   "A",
		Price:     decimal.NewFromFloat(84100.00),
	}
	inCh <- evt

	select {
	case <-outCh:
		// expected - evicted entry can be re-added
	case <-time.After(100 * time.Millisecond):
		t.Fatal("evicted trade should be allowed again")
	}
}

func TestNormalizer_ContextCancellation(t *testing.T) {
	inCh := make(chan domain.RawEvent, 1)
	outCh := make(chan domain.RawEvent, 1)
	n := New(inCh, outCh, testLogger())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		n.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// expected - Run exited
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Run did not exit on context cancellation")
	}
}

func TestNormalizer_InputChannelClosed(t *testing.T) {
	inCh := make(chan domain.RawEvent, 1)
	outCh := make(chan domain.RawEvent, 1)
	n := New(inCh, outCh, testLogger())

	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		n.Run(ctx)
		close(done)
	}()

	close(inCh)

	select {
	case <-done:
		// expected - Run exited
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Run did not exit when input channel closed")
	}
}

func TestNormalizer_OutputChannelFull(t *testing.T) {
	inCh := make(chan domain.RawEvent, 10)
	outCh := make(chan domain.RawEvent, 1) // small buffer
	n := New(inCh, outCh, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go n.Run(ctx)

	// Fill output channel
	inCh <- domain.RawEvent{Source: "binance", EventType: "trade", TradeID: "1", Price: decimal.NewFromFloat(84100)}

	// Wait for it to be processed
	time.Sleep(10 * time.Millisecond)

	// Send more - should not block
	done := make(chan struct{})
	go func() {
		for i := 2; i <= 5; i++ {
			inCh <- domain.RawEvent{
				Source:    "binance",
				EventType: "trade",
				TradeID:   string(rune('0' + i)),
				Price:     decimal.NewFromFloat(84100),
			}
		}
		close(done)
	}()

	select {
	case <-done:
		// expected - did not block
	case <-time.After(100 * time.Millisecond):
		t.Fatal("normalizer blocked when output channel was full")
	}
}

func TestNormalizer_ConcurrentProcessing(t *testing.T) {
	inCh := make(chan domain.RawEvent, 1000)
	outCh := make(chan domain.RawEvent, 1000)
	n := New(inCh, outCh, testLogger())

	ctx, cancel := context.WithCancel(context.Background())

	go n.Run(ctx)

	var wg sync.WaitGroup
	numProducers := 5
	eventsPerProducer := 100

	for p := 0; p < numProducers; p++ {
		wg.Add(1)
		go func(producerID int) {
			defer wg.Done()
			for i := 0; i < eventsPerProducer; i++ {
				evt := domain.RawEvent{
					Source:    "binance",
					EventType: "trade",
					TradeID:   string(rune('A'+producerID)) + "-" + string(rune('0'+i%10)) + string(rune('0'+i/10)),
					Price:     decimal.NewFromFloat(84100.00),
				}
				inCh <- evt
			}
		}(p)
	}

	wg.Wait()
	time.Sleep(50 * time.Millisecond) // let normalizer process

	// Cancel context to stop normalizer before counting
	cancel()
	time.Sleep(10 * time.Millisecond) // let normalizer exit

	// Count output (safe now that normalizer has stopped)
	count := 0
	for {
		select {
		case <-outCh:
			count++
		default:
			goto done
		}
	}
done:

	expected := numProducers * eventsPerProducer
	if count != expected {
		t.Errorf("expected %d events, got %d", expected, count)
	}
}

func TestMapCanonicalSymbol(t *testing.T) {
	tests := []struct {
		source string
		native string
		want   string
	}{
		{"binance", "BTCUSDT", "BTC/USD"},
		{"binance", "btcusdt", "BTC/USD"},
		{"coinbase", "BTC-USD", "BTC/USD"},
		{"kraken", "BTC/USD", "BTC/USD"},
		{"okx", "BTC-USDT", "BTC/USD"},
		{"unknown", "ANYTHING", "BTC/USD"},
	}

	for _, tt := range tests {
		t.Run(tt.source+"_"+tt.native, func(t *testing.T) {
			got := mapCanonicalSymbol(tt.source, tt.native)
			if got != tt.want {
				t.Errorf("mapCanonicalSymbol(%s, %s) = %s, want %s", tt.source, tt.native, got, tt.want)
			}
		})
	}
}

func TestIsDuplicate(t *testing.T) {
	n := &Normalizer{
		maxSeen: 100,
	}

	// First call should return false (not duplicate)
	if n.isDuplicate("key1") {
		t.Error("first occurrence should not be duplicate")
	}

	// Second call should return true (is duplicate)
	if !n.isDuplicate("key1") {
		t.Error("second occurrence should be duplicate")
	}

	// Different key should not be duplicate
	if n.isDuplicate("key2") {
		t.Error("different key should not be duplicate")
	}
}
