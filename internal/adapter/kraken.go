package adapter

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"

	"github.com/justar9/btc-price-tick/internal/domain"
)

// KrakenAdapter handles Kraken WebSocket v2 trade and ticker channels.
type KrakenAdapter struct {
	*BaseAdapter
	outCh        chan<- domain.RawEvent
	nativeSymbol string
	useTicker    bool
}

// krakenMsg is the top-level Kraken WS v2 message.
type krakenMsg struct {
	Channel string          `json:"channel"`
	Type    string          `json:"type"` // "snapshot", "update", "subscribe", "heartbeat"
	Data    json.RawMessage `json:"data"`
	Method  string          `json:"method,omitempty"`  // for subscribe responses
	Success bool            `json:"success,omitempty"` // for subscribe responses
	Error   string          `json:"error,omitempty"`
}

type krakenTrade struct {
	Symbol    string  `json:"symbol"`
	Side      string  `json:"side"`
	Price     float64 `json:"price"`
	Qty       float64 `json:"qty"`
	OrdType   string  `json:"ord_type"`
	TradeID   int64   `json:"trade_id"`
	Timestamp string  `json:"timestamp"`
}

type krakenTicker struct {
	Symbol string  `json:"symbol"`
	Bid    float64 `json:"bid"`
	BidQty float64 `json:"bid_qty"`
	Ask    float64 `json:"ask"`
	AskQty float64 `json:"ask_qty"`
	Last   float64 `json:"last"`
}

func NewKrakenAdapter(url, nativeSymbol string, useTicker bool, pingInterval time.Duration, outCh chan<- domain.RawEvent, logger *slog.Logger) *KrakenAdapter {
	ka := &KrakenAdapter{
		BaseAdapter:  NewBaseAdapter("kraken", url, pingInterval, 0, logger),
		outCh:        outCh,
		nativeSymbol: nativeSymbol,
		useTicker:    useTicker,
	}
	ka.SetMessageHandler(ka.handleMessage)
	ka.SetOnConnected(ka.subscribe)
	return ka
}

func (ka *KrakenAdapter) subscribe(conn *websocket.Conn) error {
	// Subscribe to trades
	tradeSub := map[string]interface{}{
		"method": "subscribe",
		"params": map[string]interface{}{
			"channel":  "trade",
			"symbol":   []string{ka.nativeSymbol},
			"snapshot":  false,
		},
	}
	if err := conn.WriteJSON(tradeSub); err != nil {
		return fmt.Errorf("subscribe trade: %w", err)
	}

	// Optionally subscribe to ticker for midpoint fallback
	if ka.useTicker {
		tickerSub := map[string]interface{}{
			"method": "subscribe",
			"params": map[string]interface{}{
				"channel": "ticker",
				"symbol":  []string{ka.nativeSymbol},
			},
		}
		if err := conn.WriteJSON(tickerSub); err != nil {
			return fmt.Errorf("subscribe ticker: %w", err)
		}
	}

	return nil
}

func (ka *KrakenAdapter) handleMessage(_ int, data []byte, recvTS time.Time) {
	var msg krakenMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		ka.logger.Warn("unmarshal msg failed", "error", err)
		return
	}

	switch msg.Channel {
	case "trade":
		if msg.Type == "snapshot" || msg.Type == "update" {
			ka.handleTrades(msg.Data, data, recvTS, msg.Type)
		}
	case "ticker":
		if msg.Type == "snapshot" || msg.Type == "update" {
			ka.handleTicker(msg.Data, data, recvTS)
		}
	case "heartbeat":
		// liveness tracked by BaseAdapter
	case "status":
		ka.logger.Info("kraken status", "type", msg.Type)
	default:
		// subscribe responses, etc.
		if msg.Method == "subscribe" {
			if msg.Success {
				ka.logger.Info("subscription confirmed", "channel", msg.Channel)
			} else {
				ka.logger.Error("subscription failed", "error", msg.Error)
			}
		}
	}
}

func (ka *KrakenAdapter) handleTrades(rawData json.RawMessage, fullPayload []byte, recvTS time.Time, msgType string) {
	var trades []krakenTrade
	if err := json.Unmarshal(rawData, &trades); err != nil {
		ka.logger.Warn("unmarshal trades failed", "error", err)
		return
	}

	// For snapshot, just take the most recent trade
	if msgType == "snapshot" && len(trades) > 0 {
		trades = trades[len(trades)-1:]
	}

	for _, t := range trades {
		price := decimal.NewFromFloat(t.Price)
		size := decimal.NewFromFloat(t.Qty)

		exchangeTS, err := time.Parse(time.RFC3339Nano, t.Timestamp)
		if err != nil {
			exchangeTS = recvTS
		}

		evt := domain.RawEvent{
			Source:          "kraken",
			SymbolNative:    t.Symbol,
			SymbolCanonical: "BTC/USD",
			EventType:       "trade",
			ExchangeTS:      exchangeTS,
			RecvTS:          recvTS,
			Price:           price,
			Size:            size,
			Side:            t.Side,
			TradeID:         fmt.Sprintf("%d", t.TradeID),
			RawPayload:      fullPayload,
		}

		select {
		case ka.outCh <- evt:
		default:
			ka.logger.Warn("output channel full, dropping event")
		}
	}
}

func (ka *KrakenAdapter) handleTicker(rawData json.RawMessage, fullPayload []byte, recvTS time.Time) {
	var tickers []krakenTicker
	if err := json.Unmarshal(rawData, &tickers); err != nil {
		ka.logger.Warn("unmarshal ticker failed", "error", err)
		return
	}

	for _, tk := range tickers {
		if tk.Symbol != ka.nativeSymbol {
			continue
		}

		bid := decimal.NewFromFloat(tk.Bid)
		ask := decimal.NewFromFloat(tk.Ask)

		evt := domain.RawEvent{
			Source:          "kraken",
			SymbolNative:    tk.Symbol,
			SymbolCanonical: "BTC/USD",
			EventType:       "ticker",
			ExchangeTS:      recvTS,
			RecvTS:          recvTS,
			Bid:             bid,
			Ask:             ask,
			RawPayload:      fullPayload,
		}

		select {
		case ka.outCh <- evt:
		default:
			ka.logger.Warn("output channel full, dropping ticker")
		}
	}
}
