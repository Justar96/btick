package storage

import (
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/justar9/btick/internal/domain"
)

func BenchmarkFlush(b *testing.B) {
	b.ReportAllocs()

	event := benchmarkRawEvent(0)

	b.Run("copy_row", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = rawTickCopyRow(event)
		}
	})

	for _, size := range []int{128, 2000} {
		size := size
		b.Run(fmt.Sprintf("copy_source_values_%d", size), func(b *testing.B) {
			batch := benchmarkRawBatch(size)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				source := rawTickCopySource(batch)
				for source.Next() {
					if _, err := source.Values(); err != nil {
						b.Fatal(err)
					}
				}
				if err := source.Err(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}

	b.Run("batch_pool_reuse_2000", func(b *testing.B) {
		writer := NewWriter(nil, 2000, time.Second, benchmarkLogger())
		batch := benchmarkRawBatch(2000)

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf := writer.getBatch()
			buf = append(buf, batch...)
			writer.putBatch(buf)
		}
	})
}

func benchmarkLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func benchmarkRawBatch(size int) []domain.RawEvent {
	batch := make([]domain.RawEvent, size)
	for i := range batch {
		batch[i] = benchmarkRawEvent(i)
	}
	return batch
}

func benchmarkRawEvent(i int) domain.RawEvent {
	now := time.Unix(1711028164+int64(i%60), int64(i%1000)*1_000_000).UTC()
	return domain.RawEvent{
		EventID:         uuid.New(),
		Source:          benchmarkSources[i%len(benchmarkSources)],
		SymbolNative:    benchmarkSymbols[i%len(benchmarkSymbols)],
		SymbolCanonical: "BTC/USD",
		EventType:       benchmarkEventTypes[i%len(benchmarkEventTypes)],
		ExchangeTS:      now,
		RecvTS:          now.Add(25 * time.Millisecond),
		Price:           decimal.NewFromInt(84_100 + int64(i%25)),
		Size:            decimal.NewFromInt(1 + int64(i%5)),
		Side:            benchmarkSides[i%len(benchmarkSides)],
		TradeID:         "trade-" + strconv.Itoa(i),
		Sequence:        "seq-" + strconv.Itoa(i),
		Bid:             decimal.NewFromInt(84_099 + int64(i%25)),
		Ask:             decimal.NewFromInt(84_101 + int64(i%25)),
		RawPayload:      []byte(`{"type":"trade"}`),
	}
}

var benchmarkSources = []string{"binance", "coinbase", "kraken"}
var benchmarkSymbols = []string{"BTCUSDT", "BTC-USD", "BTC/USD"}
var benchmarkEventTypes = []string{"trade", "ticker"}
var benchmarkSides = []string{"buy", "sell"}
