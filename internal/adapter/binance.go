package adapter

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"

	"github.com/justar9/btc-price-tick/internal/domain"
)

// BinanceAdapter handles Binance btcusdt@trade and btcusdt@bookTicker streams.
type BinanceAdapter struct {
	*BaseAdapter
	outCh  chan<- domain.RawEvent
	symbol string
}

// binanceTrade represents a Binance trade stream message.
type binanceTrade struct {
	EventType string `json:"e"`
	EventTime int64  `json:"E"`
	Symbol    string `json:"s"`
	TradeID   int64  `json:"t"`
	Price     string `json:"p"`
	Quantity  string `json:"q"`
	TradeTime int64  `json:"T"`
	IsMaker   bool   `json:"m"` // true = buyer is maker → taker sold → side = "sell"
}

// binanceBookTicker represents a Binance bookTicker stream message.
type binanceBookTicker struct {
	UpdateID int64  `json:"u"`
	Symbol   string `json:"s"`
	BestBid  string `json:"b"`
	BidQty   string `json:"B"`
	BestAsk  string `json:"a"`
	AskQty   string `json:"A"`
}

// binanceCombinedMsg wraps messages from combined streams.
type binanceCombinedMsg struct {
	Stream string          `json:"stream"`
	Data   json.RawMessage `json:"data"`
}

func NewBinanceAdapter(url, nativeSymbol string, pingInterval, maxLifetime time.Duration, outCh chan<- domain.RawEvent, logger *slog.Logger) *BinanceAdapter {
	ba := &BinanceAdapter{
		BaseAdapter: NewBaseAdapter("binance", url, pingInterval, maxLifetime, logger),
		outCh:       outCh,
		symbol:      nativeSymbol,
	}
	ba.SetMessageHandler(ba.handleMessage)
	// Binance combined streams don't require a subscribe message;
	// streams are specified in the URL query parameter.
	return ba
}

func (ba *BinanceAdapter) handleMessage(_ int, data []byte, recvTS time.Time) {
	// Combined stream format: {"stream":"btcusdt@trade","data":{...}}
	var combined binanceCombinedMsg
	if err := json.Unmarshal(data, &combined); err != nil {
		ba.logger.Warn("unmarshal combined msg failed", "error", err)
		return
	}

	switch {
	case len(combined.Stream) > 0 && contains(combined.Stream, "@trade"):
		ba.handleTrade(combined.Data, data, recvTS)
	case len(combined.Stream) > 0 && contains(combined.Stream, "@bookTicker"):
		ba.handleBookTicker(combined.Data, data, recvTS)
	default:
		// Could be a ping response or other control message
	}
}

func (ba *BinanceAdapter) handleTrade(rawData json.RawMessage, fullPayload []byte, recvTS time.Time) {
	var t binanceTrade
	if err := json.Unmarshal(rawData, &t); err != nil {
		ba.logger.Warn("unmarshal trade failed", "error", err)
		return
	}

	price, err := decimal.NewFromString(t.Price)
	if err != nil {
		ba.logger.Warn("parse price failed", "error", err, "price", t.Price)
		return
	}
	size, err := decimal.NewFromString(t.Quantity)
	if err != nil {
		ba.logger.Warn("parse quantity failed", "error", err, "qty", t.Quantity)
		return
	}

	side := "buy"
	if t.IsMaker {
		side = "sell"
	}

	evt := domain.RawEvent{
		Source:          "binance",
		SymbolNative:    t.Symbol,
		SymbolCanonical: "BTC/USD",
		EventType:       "trade",
		ExchangeTS:      time.UnixMilli(t.TradeTime),
		RecvTS:          recvTS,
		Price:           price,
		Size:            size,
		Side:            side,
		TradeID:         strconv.FormatInt(t.TradeID, 10),
		RawPayload:      fullPayload,
	}

	select {
	case ba.outCh <- evt:
	default:
		ba.logger.Warn("output channel full, dropping event")
	}
}

func (ba *BinanceAdapter) handleBookTicker(rawData json.RawMessage, fullPayload []byte, recvTS time.Time) {
	var bt binanceBookTicker
	if err := json.Unmarshal(rawData, &bt); err != nil {
		ba.logger.Warn("unmarshal bookTicker failed", "error", err)
		return
	}

	bid, _ := decimal.NewFromString(bt.BestBid)
	ask, _ := decimal.NewFromString(bt.BestAsk)

	evt := domain.RawEvent{
		Source:          "binance",
		SymbolNative:    bt.Symbol,
		SymbolCanonical: "BTC/USD",
		EventType:       "ticker",
		ExchangeTS:      recvTS, // bookTicker has no exchange timestamp
		RecvTS:          recvTS,
		Bid:             bid,
		Ask:             ask,
		RawPayload:      fullPayload,
	}

	select {
	case ba.outCh <- evt:
	default:
		ba.logger.Warn("output channel full, dropping ticker event")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// formatBinanceWsURL constructs the combined stream URL.
func FormatBinanceWsURL(baseURL, symbol string, includeBookTicker bool) string {
	streams := symbol + "@trade"
	if includeBookTicker {
		streams += "/" + symbol + "@bookTicker"
	}
	return fmt.Sprintf("%s/stream?streams=%s", baseURL, streams)
}
