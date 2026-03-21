package storage

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/justar9/btick/internal/domain"
)

func TestRawTickCopyRow(t *testing.T) {
	now := time.Unix(1711028164, 0).UTC()
	eventID := uuid.MustParse("018e5f6c-5d53-7d42-a2ff-d7a1a95c7a11")
	evt := domain.RawEvent{
		EventID:         eventID,
		Source:          "binance",
		SymbolNative:    "BTCUSDT",
		SymbolCanonical: "BTC/USD",
		EventType:       "trade",
		ExchangeTS:      now,
		RecvTS:          now.Add(25 * time.Millisecond),
		Price:           decimal.RequireFromString("84100.12"),
		Size:            decimal.RequireFromString("0.125"),
		Side:            "buy",
		TradeID:         "trade-123",
		Sequence:        "seq-9",
		Bid:             decimal.RequireFromString("84100.10"),
		Ask:             decimal.RequireFromString("84100.14"),
		RawPayload:      []byte(`{"trade_id":"trade-123"}`),
	}

	row := rawTickCopyRow(evt)
	if len(row) != len(rawTickCopyColumns) {
		t.Fatalf("expected %d columns, got %d", len(rawTickCopyColumns), len(row))
	}

	if got := row[0].(uuid.UUID); got != eventID {
		t.Fatalf("unexpected event id: got %s want %s", got, eventID)
	}

	if got := row[1].(string); got != evt.Source {
		t.Fatalf("unexpected source: got %q want %q", got, evt.Source)
	}

	if got := row[8].(decimal.Decimal); !got.Equal(evt.Size) {
		t.Fatalf("unexpected size: got %s want %s", got, evt.Size)
	}

	if got := string(row[14].([]byte)); got != string(evt.RawPayload) {
		t.Fatalf("unexpected payload: got %q want %q", got, string(evt.RawPayload))
	}
}

func TestRawTickCopySource(t *testing.T) {
	batch := []domain.RawEvent{
		{
			EventID:         uuid.MustParse("018e5f6c-5d53-7d42-a2ff-d7a1a95c7a11"),
			Source:          "binance",
			SymbolNative:    "BTCUSDT",
			SymbolCanonical: "BTC/USD",
			EventType:       "trade",
			ExchangeTS:      time.Unix(1711028164, 0).UTC(),
			RecvTS:          time.Unix(1711028164, 50000000).UTC(),
			Price:           decimal.RequireFromString("84100.12"),
			Size:            decimal.RequireFromString("0.125"),
			TradeID:         "trade-1",
			RawPayload:      []byte(`{"trade_id":"trade-1"}`),
		},
		{
			EventID:         uuid.MustParse("018e5f6c-5d53-7d42-a2ff-d7a1a95c7a12"),
			Source:          "coinbase",
			SymbolNative:    "BTC-USD",
			SymbolCanonical: "BTC/USD",
			EventType:       "ticker",
			ExchangeTS:      time.Unix(1711028165, 0).UTC(),
			RecvTS:          time.Unix(1711028165, 20000000).UTC(),
			Bid:             decimal.RequireFromString("84101.00"),
			Ask:             decimal.RequireFromString("84101.25"),
			RawPayload:      []byte(`{"type":"ticker"}`),
		},
	}

	source := rawTickCopySource(batch)
	var rows [][]any
	for source.Next() {
		values, err := source.Values()
		if err != nil {
			t.Fatalf("copy source values: %v", err)
		}
		rows = append(rows, values)
	}

	if err := source.Err(); err != nil {
		t.Fatalf("copy source err: %v", err)
	}

	if len(rows) != len(batch) {
		t.Fatalf("expected %d rows, got %d", len(batch), len(rows))
	}

	if got := rows[0][10].(string); got != "trade-1" {
		t.Fatalf("unexpected first trade id: got %q", got)
	}

	if got := string(rows[1][14].([]byte)); got != `{"type":"ticker"}` {
		t.Fatalf("unexpected second payload: got %q", got)
	}
}
