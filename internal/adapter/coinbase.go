package adapter

import (
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"

	"github.com/justar9/btick/internal/domain"
)

// CoinbaseAdapter handles Coinbase Advanced Trade WebSocket market_trades and ticker channels.
type CoinbaseAdapter struct {
	*BaseAdapter
	outCh        chan<- domain.RawEvent
	nativeSymbol string
	jwt          string
}

// coinbaseMsg is the top-level Coinbase WebSocket message envelope.
type coinbaseMsg struct {
	Channel     string            `json:"channel"`
	ClientID    string            `json:"client_id"`
	Timestamp   string            `json:"timestamp"`
	SequenceNum int64             `json:"sequence_num"`
	Events      []json.RawMessage `json:"events"`
}

// coinbaseTradeEvent is an event inside market_trades channel.
type coinbaseTradeEvent struct {
	Type   string           `json:"type"` // "snapshot" or "update"
	Trades []coinbaseTrade  `json:"trades"`
}

type coinbaseTrade struct {
	TradeID   string `json:"trade_id"`
	ProductID string `json:"product_id"`
	Price     string `json:"price"`
	Size      string `json:"size"`
	Side      string `json:"side"` // "BUY" or "SELL"
	Time      string `json:"time"` // RFC3339Nano
}

// coinbaseTickerEvent is an event inside ticker channel.
type coinbaseTickerEvent struct {
	Type    string           `json:"type"`
	Tickers []coinbaseTicker `json:"tickers"`
}

type coinbaseTicker struct {
	Type            string `json:"type"`
	ProductID       string `json:"product_id"`
	Price           string `json:"price"`
	BestBid         string `json:"best_bid"`
	BestBidQuantity string `json:"best_bid_quantity"`
	BestAsk         string `json:"best_ask"`
	BestAskQuantity string `json:"best_ask_quantity"`
}

func NewCoinbaseAdapter(url, nativeSymbol, jwt string, pingInterval time.Duration, outCh chan<- domain.RawEvent, logger *slog.Logger) *CoinbaseAdapter {
	ca := &CoinbaseAdapter{
		BaseAdapter:  NewBaseAdapter("coinbase", url, pingInterval, 0, logger),
		outCh:        outCh,
		nativeSymbol: nativeSymbol,
		jwt:          jwt,
	}
	ca.SetMessageHandler(ca.handleMessage)
	ca.SetOnConnected(ca.subscribe)
	return ca
}

func (ca *CoinbaseAdapter) subscribe(conn *websocket.Conn) error {
	// Subscribe to market_trades
	tradesSub := map[string]interface{}{
		"type":        "subscribe",
		"product_ids": []string{ca.nativeSymbol},
		"channel":     "market_trades",
	}
	if ca.jwt != "" {
		tradesSub["jwt"] = ca.jwt
	}
	if err := conn.WriteJSON(tradesSub); err != nil {
		return err
	}

	// Subscribe to ticker for bid/ask fallback
	tickerSub := map[string]interface{}{
		"type":        "subscribe",
		"product_ids": []string{ca.nativeSymbol},
		"channel":     "ticker",
	}
	if ca.jwt != "" {
		tickerSub["jwt"] = ca.jwt
	}
	if err := conn.WriteJSON(tickerSub); err != nil {
		return err
	}

	// Subscribe to heartbeats
	hbSub := map[string]interface{}{
		"type":    "subscribe",
		"channel": "heartbeats",
	}
	if ca.jwt != "" {
		hbSub["jwt"] = ca.jwt
	}
	return conn.WriteJSON(hbSub)
}

func (ca *CoinbaseAdapter) handleMessage(_ int, data []byte, recvTS time.Time) {
	var msg coinbaseMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		ca.logger.Warn("unmarshal msg failed", "error", err)
		return
	}

	switch msg.Channel {
	case "market_trades":
		ca.handleMarketTrades(msg.Events, data, recvTS)
	case "ticker":
		ca.handleTicker(msg.Events, data, recvTS)
	case "heartbeats":
		// heartbeat received, liveness is tracked by BaseAdapter
	case "subscriptions":
		ca.logger.Info("subscription confirmed")
	default:
		// ignore unknown channels
	}
}

func (ca *CoinbaseAdapter) handleMarketTrades(events []json.RawMessage, fullPayload []byte, recvTS time.Time) {
	for _, raw := range events {
		var evt coinbaseTradeEvent
		if err := json.Unmarshal(raw, &evt); err != nil {
			ca.logger.Warn("unmarshal trade event failed", "error", err)
			continue
		}

		// Only process updates, skip snapshot of old trades
		if evt.Type == "snapshot" {
			ca.logger.Debug("received trade snapshot", "count", len(evt.Trades))
			// Process only the most recent trade from snapshot
			if len(evt.Trades) > 0 {
				ca.emitTrade(evt.Trades[0], fullPayload, recvTS)
			}
			continue
		}

		for _, t := range evt.Trades {
			ca.emitTrade(t, fullPayload, recvTS)
		}
	}
}

func (ca *CoinbaseAdapter) emitTrade(t coinbaseTrade, fullPayload []byte, recvTS time.Time) {
	price, err := decimal.NewFromString(t.Price)
	if err != nil {
		ca.logger.Warn("parse price failed", "error", err)
		return
	}
	size, err := decimal.NewFromString(t.Size)
	if err != nil {
		ca.logger.Warn("parse size failed", "error", err)
		return
	}

	exchangeTS, err := time.Parse(time.RFC3339Nano, t.Time)
	if err != nil {
		exchangeTS = recvTS
	}

	side := strings.ToLower(t.Side)

	evt := domain.RawEvent{
		Source:          "coinbase",
		SymbolNative:    t.ProductID,
		SymbolCanonical: "BTC/USD",
		EventType:       "trade",
		ExchangeTS:      exchangeTS,
		RecvTS:          recvTS,
		Price:           price,
		Size:            size,
		Side:            side,
		TradeID:         t.TradeID,
		RawPayload:      fullPayload,
	}

	select {
	case ca.outCh <- evt:
	default:
		ca.logger.Warn("output channel full, dropping event")
	}
}

func (ca *CoinbaseAdapter) handleTicker(events []json.RawMessage, fullPayload []byte, recvTS time.Time) {
	for _, raw := range events {
		var evt coinbaseTickerEvent
		if err := json.Unmarshal(raw, &evt); err != nil {
			ca.logger.Warn("unmarshal ticker event failed", "error", err)
			continue
		}

		for _, tk := range evt.Tickers {
			if tk.ProductID != ca.nativeSymbol {
				continue
			}

			bid, _ := decimal.NewFromString(tk.BestBid)
			ask, _ := decimal.NewFromString(tk.BestAsk)

			evt := domain.RawEvent{
				Source:          "coinbase",
				SymbolNative:    tk.ProductID,
				SymbolCanonical: "BTC/USD",
				EventType:       "ticker",
				ExchangeTS:      recvTS,
				RecvTS:          recvTS,
				Bid:             bid,
				Ask:             ask,
				RawPayload:      fullPayload,
			}

			select {
			case ca.outCh <- evt:
			default:
				ca.logger.Warn("output channel full, dropping ticker event")
			}
		}
	}
}
