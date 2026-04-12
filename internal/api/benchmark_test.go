package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/justar9/btick/internal/config"
	"github.com/justar9/btick/internal/domain"
	"github.com/shopspring/decimal"
)

func benchLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func benchHub(clients int) *WSHub {
	hub := NewWSHub(benchLogger(), config.WSConfig{
		SendBufferSize:     256,
		SlowClientMaxDrops: 10000, // high limit to avoid eviction in bench
	}, func(_ string) *domain.LatestState { return nil }, []string{"BTC/USD"})

	// Add mock clients with buffered channels.
	hub.mu.Lock()
	for i := 0; i < clients; i++ {
		c := &wsClient{
			sendCh:      make(chan []byte, 256),
			subs:        newSubscriptions(),
			logger:      hub.logger,
			connectedAt: time.Now().UTC(),
		}
		hub.clients[c] = struct{}{}
	}
	hub.mu.Unlock()

	return hub
}

func benchMessage() WSMessage {
	return WSMessage{
		Type:         "latest_price",
		Symbol:       "BTC/USD",
		TS:           time.Now().UTC().Format(time.RFC3339Nano),
		Price:        decimal.NewFromInt(84_150).String(),
		Basis:        "median_trade",
		QualityScore: "0.95",
		SourceCount:  4,
		SourcesUsed:  []string{"binance", "coinbase", "kraken", "okx"},
	}
}

// =============================================================================
// Broadcast Benchmarks — varying client counts
// =============================================================================

func BenchmarkBroadcast_1Client(b *testing.B) {
	benchmarkBroadcastN(b, 1)
}

func BenchmarkBroadcast_10Clients(b *testing.B) {
	benchmarkBroadcastN(b, 10)
}

func BenchmarkBroadcast_100Clients(b *testing.B) {
	benchmarkBroadcastN(b, 100)
}

func BenchmarkBroadcast_500Clients(b *testing.B) {
	benchmarkBroadcastN(b, 500)
}

func BenchmarkBroadcast_1000Clients(b *testing.B) {
	benchmarkBroadcastN(b, 1000)
}

func benchmarkBroadcastN(b *testing.B, n int) {
	hub := benchHub(n)
	msg := benchMessage()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		hub.Broadcast(msg)
	}

	b.StopTimer()
	// Drain client channels to avoid accumulation.
	hub.mu.RLock()
	for c := range hub.clients {
		for len(c.sendCh) > 0 {
			<-c.sendCh
		}
	}
	hub.mu.RUnlock()
}

// =============================================================================
// Broadcast with subscription filtering
// =============================================================================

func BenchmarkBroadcast_100Clients_Filtered(b *testing.B) {
	hub := benchHub(100)

	// Half of clients unsubscribe from latest_price
	hub.mu.RLock()
	i := 0
	for c := range hub.clients {
		if i%2 == 0 {
			c.subs.set("latest_price", false)
		}
		i++
	}
	hub.mu.RUnlock()

	msg := benchMessage()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		hub.Broadcast(msg)
	}
}

// =============================================================================
// Broadcast with slow clients (channel full → drops)
// =============================================================================

func BenchmarkBroadcast_100Clients_SlowMix(b *testing.B) {
	hub := NewWSHub(benchLogger(), config.WSConfig{
		SendBufferSize:     4, // tiny buffer
		SlowClientMaxDrops: 100000,
	}, func(_ string) *domain.LatestState { return nil }, []string{"BTC/USD"})

	hub.mu.Lock()
	for i := 0; i < 100; i++ {
		c := &wsClient{
			sendCh:      make(chan []byte, 4),
			subs:        newSubscriptions(),
			logger:      hub.logger,
			connectedAt: time.Now().UTC(),
		}
		hub.clients[c] = struct{}{}
	}
	hub.mu.Unlock()

	msg := benchMessage()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		hub.Broadcast(msg)
	}
}

// =============================================================================
// JSON Marshal benchmark (message serialization cost)
// =============================================================================

func BenchmarkWSMessage_Marshal(b *testing.B) {
	msg := benchMessage()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(msg)
	}
}

// =============================================================================
// Snapshot message construction benchmark
// =============================================================================

func BenchmarkSnapshotToWSMessage(b *testing.B) {
	snap := domain.Snapshot1s{
		TSSecond:        time.Now().UTC().Truncate(time.Second),
		CanonicalSymbol: "BTC/USD",
		CanonicalPrice:  decimal.NewFromInt(84_150),
		Basis:           "median_trade",
		QualityScore:    decimal.NewFromFloat(0.95),
		SourceCount:     4,
		SourcesUsed:     []string{"binance", "coinbase", "kraken", "okx"},
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = WSMessage{
			Type:         "snapshot_1s",
			Symbol:       snap.CanonicalSymbol,
			TS:           snap.TSSecond.Format(time.RFC3339Nano),
			Price:        snap.CanonicalPrice.String(),
			Basis:        snap.Basis,
			IsStale:      snap.IsStale,
			QualityScore: snap.QualityScore.String(),
			SourceCount:  snap.SourceCount,
			SourcesUsed:  snap.SourcesUsed,
		}
	}
}

// =============================================================================
// Broadcast throughput benchmark (messages/sec)
// =============================================================================

func BenchmarkBroadcast_Throughput_100Clients(b *testing.B) {
	hub := benchHub(100)
	sources := []string{"binance", "coinbase", "kraken", "okx"}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		msg := WSMessage{
			Type:         "latest_price",
			Symbol:       "BTC/USD",
			TS:           time.Now().UTC().Format(time.RFC3339Nano),
			Price:        strconv.Itoa(84_000 + i%500),
			Basis:        "median_trade",
			QualityScore: "0.95",
			SourceCount:  4,
			SourcesUsed:  sources,
		}
		hub.Broadcast(msg)
	}

	b.StopTimer()
	hub.mu.RLock()
	for c := range hub.clients {
		for len(c.sendCh) > 0 {
			<-c.sendCh
		}
	}
	hub.mu.RUnlock()
}
