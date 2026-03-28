package adapter

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"

	"github.com/justar9/btick/internal/domain"
)

// CoinbaseAdapter handles Coinbase Exchange public ws-feed matches and ticker channels.
type CoinbaseAdapter struct {
	*BaseAdapter
	outCh        chan<- domain.RawEvent
	nativeSymbol string
}

// coinbaseMsg is the top-level Coinbase Exchange ws-feed message.
type coinbaseMsg struct {
	Type      string `json:"type"`
	ProductID string `json:"product_id"`
	Price     string `json:"price"`
	Size      string `json:"size"`
	Side      string `json:"side"`
	Time      string `json:"time"`
	TradeID   int64  `json:"trade_id"`
	BestBid   string `json:"best_bid"`
	BestAsk   string `json:"best_ask"`
	Message   string `json:"message"`
}

func NewCoinbaseAdapter(url, nativeSymbol string, pingInterval time.Duration, outCh chan<- domain.RawEvent, logger *slog.Logger) *CoinbaseAdapter {
	ca := &CoinbaseAdapter{
		BaseAdapter:  NewBaseAdapter("coinbase", url, pingInterval, 0, logger),
		outCh:        outCh,
		nativeSymbol: nativeSymbol,
	}
	ca.SetMessageHandler(ca.handleMessage)
	ca.SetOnConnected(ca.subscribe)
	return ca
}

func (ca *CoinbaseAdapter) subscribe(conn *websocket.Conn) error {
	return conn.WriteJSON(map[string]interface{}{
		"type":        "subscribe",
		"product_ids": []string{ca.nativeSymbol},
		"channels":    []string{"matches", "ticker", "heartbeat"},
	})
}

func (ca *CoinbaseAdapter) handleMessage(_ int, data []byte, recvTS time.Time) {
	var msg coinbaseMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		ca.logger.Warn("unmarshal msg failed", "error", err)
		return
	}

	switch msg.Type {
	case "match", "last_match":
		ca.emitTrade(msg, data, recvTS)
	case "ticker":
		ca.emitTicker(msg, data, recvTS)
	case "heartbeat", "subscriptions":
		// heartbeat received, liveness is tracked by BaseAdapter
	case "error":
		ca.logger.Error("coinbase ws error", "message", msg.Message)
	default:
		// ignore unknown channels
	}
}

func (ca *CoinbaseAdapter) emitTrade(msg coinbaseMsg, fullPayload []byte, recvTS time.Time) {
	if msg.ProductID != ca.nativeSymbol {
		return
	}

	price, err := decimal.NewFromString(msg.Price)
	if err != nil {
		ca.logger.Warn("parse price failed", "error", err)
		return
	}
	size, err := decimal.NewFromString(msg.Size)
	if err != nil {
		ca.logger.Warn("parse size failed", "error", err)
		return
	}

	exchangeTS, err := time.Parse(time.RFC3339Nano, msg.Time)
	if err != nil {
		exchangeTS = recvTS
	}

	side := strings.ToLower(msg.Side)

	evt := domain.RawEvent{
		Source:          "coinbase",
		SymbolNative:    msg.ProductID,
		SymbolCanonical: "BTC/USD",
		EventType:       "trade",
		ExchangeTS:      exchangeTS,
		RecvTS:          recvTS,
		Price:           price,
		Size:            size,
		Side:            side,
		TradeID:         strconv.FormatInt(msg.TradeID, 10),
		RawPayload:      fullPayload,
	}

	select {
	case ca.outCh <- evt:
	default:
		ca.logger.Warn("output channel full, dropping event")
	}
}

func (ca *CoinbaseAdapter) emitTicker(msg coinbaseMsg, fullPayload []byte, recvTS time.Time) {
	if msg.ProductID != ca.nativeSymbol {
		return
	}

	bid, err := decimal.NewFromString(msg.BestBid)
	if err != nil {
		ca.logger.Warn("parse bid failed", "error", err)
		return
	}
	ask, err := decimal.NewFromString(msg.BestAsk)
	if err != nil {
		ca.logger.Warn("parse ask failed", "error", err)
		return
	}

	exchangeTS, err := time.Parse(time.RFC3339Nano, msg.Time)
	if err != nil {
		exchangeTS = recvTS
	}

	evt := domain.RawEvent{
		Source:          "coinbase",
		SymbolNative:    msg.ProductID,
		SymbolCanonical: "BTC/USD",
		EventType:       "ticker",
		ExchangeTS:      exchangeTS,
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
