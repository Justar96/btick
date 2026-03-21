package adapter

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/justar9/btick/internal/domain"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// =============================================================================
// Binance Adapter Tests
// =============================================================================

func TestBinanceAdapter_HandleTrade(t *testing.T) {
	outCh := make(chan domain.RawEvent, 10)
	ba := NewBinanceAdapter(
		"wss://test.binance.com",
		"btcusdt",
		30*time.Second,
		0,
		outCh,
		testLogger(),
	)

	// Simulate a combined stream trade message
	tradeMsg := binanceCombinedMsg{
		Stream: "btcusdt@trade",
		Data: json.RawMessage(`{
			"e": "trade",
			"E": 1710000000000,
			"s": "BTCUSDT",
			"t": 123456789,
			"p": "84150.50",
			"q": "0.001",
			"T": 1710000000000,
			"m": false
		}`),
	}
	payload, _ := json.Marshal(tradeMsg)

	recvTS := time.Now()
	ba.handleMessage(1, payload, recvTS)

	select {
	case evt := <-outCh:
		if evt.Source != "binance" {
			t.Errorf("expected source binance, got %s", evt.Source)
		}
		if evt.EventType != "trade" {
			t.Errorf("expected event type trade, got %s", evt.EventType)
		}
		if !evt.Price.Equal(decimal.NewFromFloat(84150.50)) {
			t.Errorf("expected price 84150.50, got %s", evt.Price)
		}
		if !evt.Size.Equal(decimal.NewFromFloat(0.001)) {
			t.Errorf("expected size 0.001, got %s", evt.Size)
		}
		if evt.Side != "buy" {
			t.Errorf("expected side buy (m=false), got %s", evt.Side)
		}
		if evt.TradeID != "123456789" {
			t.Errorf("expected trade_id 123456789, got %s", evt.TradeID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for event")
	}
}

func TestBinanceAdapter_HandleTrade_SellSide(t *testing.T) {
	outCh := make(chan domain.RawEvent, 10)
	ba := NewBinanceAdapter("wss://test", "btcusdt", 30*time.Second, 0, outCh, testLogger())

	tradeMsg := binanceCombinedMsg{
		Stream: "btcusdt@trade",
		Data: json.RawMessage(`{
			"e": "trade",
			"E": 1710000000000,
			"s": "BTCUSDT",
			"t": 123456790,
			"p": "84100.00",
			"q": "0.5",
			"T": 1710000000000,
			"m": true
		}`),
	}
	payload, _ := json.Marshal(tradeMsg)
	ba.handleMessage(1, payload, time.Now())

	select {
	case evt := <-outCh:
		if evt.Side != "sell" {
			t.Errorf("expected side sell (m=true), got %s", evt.Side)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout")
	}
}

func TestBinanceAdapter_HandleBookTicker(t *testing.T) {
	outCh := make(chan domain.RawEvent, 10)
	ba := NewBinanceAdapter("wss://test", "btcusdt", 30*time.Second, 0, outCh, testLogger())

	tickerMsg := binanceCombinedMsg{
		Stream: "btcusdt@bookTicker",
		Data: json.RawMessage(`{
			"u": 400900217,
			"s": "BTCUSDT",
			"b": "84100.00",
			"B": "1.5",
			"a": "84110.00",
			"A": "2.0"
		}`),
	}
	payload, _ := json.Marshal(tickerMsg)
	ba.handleMessage(1, payload, time.Now())

	select {
	case evt := <-outCh:
		if evt.EventType != "ticker" {
			t.Errorf("expected ticker, got %s", evt.EventType)
		}
		if !evt.Bid.Equal(decimal.NewFromFloat(84100.00)) {
			t.Errorf("expected bid 84100, got %s", evt.Bid)
		}
		if !evt.Ask.Equal(decimal.NewFromFloat(84110.00)) {
			t.Errorf("expected ask 84110, got %s", evt.Ask)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout")
	}
}

func TestBinanceAdapter_InvalidJSON(t *testing.T) {
	outCh := make(chan domain.RawEvent, 10)
	ba := NewBinanceAdapter("wss://test", "btcusdt", 30*time.Second, 0, outCh, testLogger())

	ba.handleMessage(1, []byte("invalid json"), time.Now())

	select {
	case <-outCh:
		t.Fatal("should not emit event for invalid JSON")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

func TestBinanceAdapter_InvalidPrice(t *testing.T) {
	outCh := make(chan domain.RawEvent, 10)
	ba := NewBinanceAdapter("wss://test", "btcusdt", 30*time.Second, 0, outCh, testLogger())

	tradeMsg := binanceCombinedMsg{
		Stream: "btcusdt@trade",
		Data: json.RawMessage(`{
			"e": "trade",
			"E": 1710000000000,
			"s": "BTCUSDT",
			"t": 123456789,
			"p": "not_a_number",
			"q": "0.001",
			"T": 1710000000000,
			"m": false
		}`),
	}
	payload, _ := json.Marshal(tradeMsg)
	ba.handleMessage(1, payload, time.Now())

	select {
	case <-outCh:
		t.Fatal("should not emit event for invalid price")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

func TestFormatBinanceWsURL(t *testing.T) {
	tests := []struct {
		name              string
		baseURL           string
		symbol            string
		includeBookTicker bool
		want              string
	}{
		{
			name:              "trade only",
			baseURL:           "wss://stream.binance.com:9443",
			symbol:            "btcusdt",
			includeBookTicker: false,
			want:              "wss://stream.binance.com:9443/stream?streams=btcusdt@trade",
		},
		{
			name:              "trade and bookTicker",
			baseURL:           "wss://stream.binance.com:9443",
			symbol:            "btcusdt",
			includeBookTicker: true,
			want:              "wss://stream.binance.com:9443/stream?streams=btcusdt@trade/btcusdt@bookTicker",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatBinanceWsURL(tt.baseURL, tt.symbol, tt.includeBookTicker)
			if got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

// =============================================================================
// Coinbase Adapter Tests
// =============================================================================

func TestCoinbaseAdapter_HandleMarketTrades(t *testing.T) {
	outCh := make(chan domain.RawEvent, 10)
	ca := NewCoinbaseAdapter(
		"wss://test.coinbase.com",
		"BTC-USD",
		"",
		30*time.Second,
		outCh,
		testLogger(),
	)

	msg := coinbaseMsg{
		Channel:   "market_trades",
		Timestamp: "2024-03-10T12:00:00.000Z",
		Events: []json.RawMessage{
			json.RawMessage(`{
				"type": "update",
				"trades": [{
					"trade_id": "trade-123",
					"product_id": "BTC-USD",
					"price": "84200.00",
					"size": "0.05",
					"side": "BUY",
					"time": "2024-03-10T12:00:00.123456789Z"
				}]
			}`),
		},
	}
	payload, _ := json.Marshal(msg)
	ca.handleMessage(1, payload, time.Now())

	select {
	case evt := <-outCh:
		if evt.Source != "coinbase" {
			t.Errorf("expected source coinbase, got %s", evt.Source)
		}
		if evt.EventType != "trade" {
			t.Errorf("expected trade, got %s", evt.EventType)
		}
		if !evt.Price.Equal(decimal.NewFromFloat(84200.00)) {
			t.Errorf("expected price 84200, got %s", evt.Price)
		}
		if evt.Side != "buy" {
			t.Errorf("expected side buy, got %s", evt.Side)
		}
		if evt.TradeID != "trade-123" {
			t.Errorf("expected trade_id trade-123, got %s", evt.TradeID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout")
	}
}

func TestCoinbaseAdapter_HandleTicker(t *testing.T) {
	outCh := make(chan domain.RawEvent, 10)
	ca := NewCoinbaseAdapter("wss://test", "BTC-USD", "", 30*time.Second, outCh, testLogger())

	msg := coinbaseMsg{
		Channel: "ticker",
		Events: []json.RawMessage{
			json.RawMessage(`{
				"type": "update",
				"tickers": [{
					"product_id": "BTC-USD",
					"price": "84150.00",
					"best_bid": "84140.00",
					"best_bid_quantity": "1.0",
					"best_ask": "84160.00",
					"best_ask_quantity": "0.5"
				}]
			}`),
		},
	}
	payload, _ := json.Marshal(msg)
	ca.handleMessage(1, payload, time.Now())

	select {
	case evt := <-outCh:
		if evt.EventType != "ticker" {
			t.Errorf("expected ticker, got %s", evt.EventType)
		}
		if !evt.Bid.Equal(decimal.NewFromFloat(84140.00)) {
			t.Errorf("expected bid 84140, got %s", evt.Bid)
		}
		if !evt.Ask.Equal(decimal.NewFromFloat(84160.00)) {
			t.Errorf("expected ask 84160, got %s", evt.Ask)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout")
	}
}

func TestCoinbaseAdapter_SkipsOtherProducts(t *testing.T) {
	outCh := make(chan domain.RawEvent, 10)
	ca := NewCoinbaseAdapter("wss://test", "BTC-USD", "", 30*time.Second, outCh, testLogger())

	msg := coinbaseMsg{
		Channel: "ticker",
		Events: []json.RawMessage{
			json.RawMessage(`{
				"type": "update",
				"tickers": [{
					"product_id": "ETH-USD",
					"best_bid": "3000.00",
					"best_ask": "3001.00"
				}]
			}`),
		},
	}
	payload, _ := json.Marshal(msg)
	ca.handleMessage(1, payload, time.Now())

	select {
	case <-outCh:
		t.Fatal("should not emit event for different product")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

func TestCoinbaseAdapter_Snapshot(t *testing.T) {
	outCh := make(chan domain.RawEvent, 10)
	ca := NewCoinbaseAdapter("wss://test", "BTC-USD", "", 30*time.Second, outCh, testLogger())

	// Snapshot should only emit the most recent trade
	msg := coinbaseMsg{
		Channel: "market_trades",
		Events: []json.RawMessage{
			json.RawMessage(`{
				"type": "snapshot",
				"trades": [
					{"trade_id": "1", "product_id": "BTC-USD", "price": "84000.00", "size": "0.1", "side": "BUY", "time": "2024-03-10T12:00:00Z"},
					{"trade_id": "2", "product_id": "BTC-USD", "price": "84100.00", "size": "0.2", "side": "SELL", "time": "2024-03-10T12:00:01Z"}
				]
			}`),
		},
	}
	payload, _ := json.Marshal(msg)
	ca.handleMessage(1, payload, time.Now())

	// Should only get 1 event (the first/most recent from snapshot)
	select {
	case evt := <-outCh:
		if evt.TradeID != "1" {
			t.Errorf("expected first trade from snapshot, got %s", evt.TradeID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout")
	}

	// Should not get second event
	select {
	case <-outCh:
		t.Fatal("should only emit one trade from snapshot")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

// =============================================================================
// Kraken Adapter Tests
// =============================================================================

func TestKrakenAdapter_HandleTrade(t *testing.T) {
	outCh := make(chan domain.RawEvent, 10)
	ka := NewKrakenAdapter(
		"wss://test.kraken.com",
		"BTC/USD",
		true,
		30*time.Second,
		outCh,
		testLogger(),
	)

	msg := krakenMsg{
		Channel: "trade",
		Type:    "update",
		Data: json.RawMessage(`[{
			"symbol": "BTC/USD",
			"side": "buy",
			"price": 84300.50,
			"qty": 0.025,
			"ord_type": "market",
			"trade_id": 987654321,
			"timestamp": "2024-03-10T12:00:00.500Z"
		}]`),
	}
	payload, _ := json.Marshal(msg)
	ka.handleMessage(1, payload, time.Now())

	select {
	case evt := <-outCh:
		if evt.Source != "kraken" {
			t.Errorf("expected source kraken, got %s", evt.Source)
		}
		if evt.EventType != "trade" {
			t.Errorf("expected trade, got %s", evt.EventType)
		}
		if !evt.Price.Equal(decimal.NewFromFloat(84300.50)) {
			t.Errorf("expected price 84300.50, got %s", evt.Price)
		}
		if !evt.Size.Equal(decimal.NewFromFloat(0.025)) {
			t.Errorf("expected size 0.025, got %s", evt.Size)
		}
		if evt.Side != "buy" {
			t.Errorf("expected side buy, got %s", evt.Side)
		}
		if evt.TradeID != "987654321" {
			t.Errorf("expected trade_id 987654321, got %s", evt.TradeID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout")
	}
}

func TestKrakenAdapter_HandleTicker(t *testing.T) {
	outCh := make(chan domain.RawEvent, 10)
	ka := NewKrakenAdapter("wss://test", "BTC/USD", true, 30*time.Second, outCh, testLogger())

	msg := krakenMsg{
		Channel: "ticker",
		Type:    "update",
		Data: json.RawMessage(`[{
			"symbol": "BTC/USD",
			"bid": 84250.00,
			"bid_qty": 1.5,
			"ask": 84260.00,
			"ask_qty": 2.0,
			"last": 84255.00
		}]`),
	}
	payload, _ := json.Marshal(msg)
	ka.handleMessage(1, payload, time.Now())

	select {
	case evt := <-outCh:
		if evt.EventType != "ticker" {
			t.Errorf("expected ticker, got %s", evt.EventType)
		}
		if !evt.Bid.Equal(decimal.NewFromFloat(84250.00)) {
			t.Errorf("expected bid 84250, got %s", evt.Bid)
		}
		if !evt.Ask.Equal(decimal.NewFromFloat(84260.00)) {
			t.Errorf("expected ask 84260, got %s", evt.Ask)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout")
	}
}

func TestKrakenAdapter_SkipsOtherSymbols(t *testing.T) {
	outCh := make(chan domain.RawEvent, 10)
	ka := NewKrakenAdapter("wss://test", "BTC/USD", true, 30*time.Second, outCh, testLogger())

	msg := krakenMsg{
		Channel: "ticker",
		Type:    "update",
		Data: json.RawMessage(`[{
			"symbol": "ETH/USD",
			"bid": 3000.00,
			"ask": 3001.00
		}]`),
	}
	payload, _ := json.Marshal(msg)
	ka.handleMessage(1, payload, time.Now())

	select {
	case <-outCh:
		t.Fatal("should not emit event for different symbol")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

func TestKrakenAdapter_Snapshot(t *testing.T) {
	outCh := make(chan domain.RawEvent, 10)
	ka := NewKrakenAdapter("wss://test", "BTC/USD", false, 30*time.Second, outCh, testLogger())

	// Snapshot should only emit the last (most recent) trade
	msg := krakenMsg{
		Channel: "trade",
		Type:    "snapshot",
		Data: json.RawMessage(`[
			{"symbol": "BTC/USD", "side": "buy", "price": 84000.00, "qty": 0.1, "trade_id": 1, "timestamp": "2024-03-10T12:00:00Z"},
			{"symbol": "BTC/USD", "side": "sell", "price": 84100.00, "qty": 0.2, "trade_id": 2, "timestamp": "2024-03-10T12:00:01Z"}
		]`),
	}
	payload, _ := json.Marshal(msg)
	ka.handleMessage(1, payload, time.Now())

	select {
	case evt := <-outCh:
		if evt.TradeID != "2" {
			t.Errorf("expected last trade from snapshot (id=2), got %s", evt.TradeID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout")
	}

	select {
	case <-outCh:
		t.Fatal("should only emit one trade from snapshot")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

// =============================================================================
// Concurrency Tests
// =============================================================================

func TestAdapter_ChannelFullDoesNotBlock(t *testing.T) {
	// Channel with capacity 1
	outCh := make(chan domain.RawEvent, 1)
	ba := NewBinanceAdapter("wss://test", "btcusdt", 30*time.Second, 0, outCh, testLogger())

	tradeMsg := binanceCombinedMsg{
		Stream: "btcusdt@trade",
		Data: json.RawMessage(`{
			"e": "trade", "E": 1710000000000, "s": "BTCUSDT",
			"t": 1, "p": "84100.00", "q": "0.001", "T": 1710000000000, "m": false
		}`),
	}
	payload, _ := json.Marshal(tradeMsg)

	// Fill the channel
	ba.handleMessage(1, payload, time.Now())

	// This should not block (drops the event)
	done := make(chan struct{})
	go func() {
		tradeMsg.Data = json.RawMessage(`{
			"e": "trade", "E": 1710000000001, "s": "BTCUSDT",
			"t": 2, "p": "84200.00", "q": "0.002", "T": 1710000000001, "m": false
		}`)
		payload2, _ := json.Marshal(tradeMsg)
		ba.handleMessage(1, payload2, time.Now())
		close(done)
	}()

	select {
	case <-done:
		// expected - did not block
	case <-time.After(100 * time.Millisecond):
		t.Fatal("handleMessage blocked when channel was full")
	}
}

func TestAdapter_ConcurrentMessages(t *testing.T) {
	outCh := make(chan domain.RawEvent, 1000)
	ba := NewBinanceAdapter("wss://test", "btcusdt", 30*time.Second, 0, outCh, testLogger())

	var wg sync.WaitGroup
	numGoroutines := 10
	messagesPerGoroutine := 10

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < messagesPerGoroutine; j++ {
				// Use unique trade IDs: id*1000 + j
				tradeID := id*1000 + j
				tradeMsg := binanceCombinedMsg{
					Stream: "btcusdt@trade",
					Data: json.RawMessage(`{
						"e": "trade", "E": 1710000000000, "s": "BTCUSDT",
						"t": ` + fmt.Sprintf("%d", tradeID) + `,
						"p": "84100.00", "q": "0.001", "T": 1710000000000, "m": false
					}`),
				}
				payload, _ := json.Marshal(tradeMsg)
				ba.handleMessage(1, payload, time.Now())
			}
		}(i)
	}

	wg.Wait()

	// Drain and count events (don't close channel, just drain what's there)
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

	expected := numGoroutines * messagesPerGoroutine
	if count != expected {
		t.Errorf("expected %d events, got %d", expected, count)
	}
}
