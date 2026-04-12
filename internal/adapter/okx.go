package adapter

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"

	"github.com/justar9/btick/internal/domain"
)

// OKXAdapter handles OKX public WebSocket trades and tickers channels.
type OKXAdapter struct {
	*BaseAdapter
	outCh           chan<- domain.RawEvent
	nativeSymbol    string
	canonicalSymbol string
}

type okxArg struct {
	Channel string `json:"channel"`
	InstID  string `json:"instId"`
}

type okxMsg struct {
	Event string          `json:"event"`
	Code  string          `json:"code"`
	Msg   string          `json:"msg"`
	Arg   okxArg          `json:"arg"`
	Data  json.RawMessage `json:"data"`
}

type okxTrade struct {
	InstID  string `json:"instId"`
	TradeID string `json:"tradeId"`
	PX      string `json:"px"`
	SZ      string `json:"sz"`
	Side    string `json:"side"`
	TS      string `json:"ts"`
}

type okxTicker struct {
	InstID string `json:"instId"`
	AskPX  string `json:"askPx"`
	BidPX  string `json:"bidPx"`
	TS     string `json:"ts"`
}

func NewOKXAdapter(url, nativeSymbol, canonicalSymbol string, pingInterval time.Duration, outCh chan<- domain.RawEvent, logger *slog.Logger) *OKXAdapter {
	oa := &OKXAdapter{
		BaseAdapter:     NewBaseAdapter("okx", url, pingInterval, 0, logger),
		outCh:           outCh,
		nativeSymbol:    nativeSymbol,
		canonicalSymbol: canonicalSymbol,
	}
	oa.SetMessageHandler(oa.handleMessage)
	oa.SetOnConnected(oa.subscribe)
	return oa
}

func (oa *OKXAdapter) subscribe(conn *websocket.Conn) error {
	return conn.WriteJSON(map[string]any{
		"op": "subscribe",
		"args": []map[string]string{
			{"channel": "trades", "instId": oa.nativeSymbol},
			{"channel": "tickers", "instId": oa.nativeSymbol},
		},
	})
}

func (oa *OKXAdapter) handleMessage(_ int, data []byte, recvTS time.Time) {
	var msg okxMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		oa.logger.Warn("unmarshal msg failed", "error", err)
		return
	}

	if msg.Event == "error" {
		oa.logger.Error("okx ws error", "code", msg.Code, "message", msg.Msg)
		return
	}
	if msg.Event != "" {
		return
	}
	if len(msg.Data) == 0 || string(msg.Data) == "null" {
		return
	}

	switch msg.Arg.Channel {
	case "trades":
		oa.handleTrades(msg.Data, data, recvTS)
	case "tickers":
		oa.handleTickers(msg.Data, data, recvTS)
	default:
		// subscription acks and other control messages are ignored
	}
}

func (oa *OKXAdapter) handleTrades(rawData json.RawMessage, fullPayload []byte, recvTS time.Time) {
	var trades []okxTrade
	if err := json.Unmarshal(rawData, &trades); err != nil {
		oa.logger.Warn("unmarshal trades failed", "error", err)
		return
	}

	for _, trade := range trades {
		if trade.InstID != oa.nativeSymbol {
			continue
		}

		price, err := decimal.NewFromString(trade.PX)
		if err != nil {
			oa.logger.Warn("parse price failed", "error", err)
			continue
		}
		size, err := decimal.NewFromString(trade.SZ)
		if err != nil {
			oa.logger.Warn("parse size failed", "error", err)
			continue
		}

		exchangeTS := recvTS
		if ts, err := parseUnixMillis(trade.TS); err == nil {
			exchangeTS = ts
		}

		evt := domain.RawEvent{
			Source:          "okx",
			SymbolNative:    trade.InstID,
			SymbolCanonical: oa.canonicalSymbol,
			EventType:       "trade",
			ExchangeTS:      exchangeTS,
			RecvTS:          recvTS,
			Price:           price,
			Size:            size,
			Side:            trade.Side,
			TradeID:         trade.TradeID,
			RawPayload:      fullPayload,
		}

		select {
		case oa.outCh <- evt:
		default:
			oa.logger.Warn("output channel full, dropping event")
		}
	}
}

func (oa *OKXAdapter) handleTickers(rawData json.RawMessage, fullPayload []byte, recvTS time.Time) {
	var tickers []okxTicker
	if err := json.Unmarshal(rawData, &tickers); err != nil {
		oa.logger.Warn("unmarshal tickers failed", "error", err)
		return
	}

	for _, ticker := range tickers {
		if ticker.InstID != oa.nativeSymbol {
			continue
		}

		bid, err := decimal.NewFromString(ticker.BidPX)
		if err != nil {
			oa.logger.Warn("parse bid failed", "error", err)
			continue
		}
		ask, err := decimal.NewFromString(ticker.AskPX)
		if err != nil {
			oa.logger.Warn("parse ask failed", "error", err)
			continue
		}

		exchangeTS := recvTS
		if ts, err := parseUnixMillis(ticker.TS); err == nil {
			exchangeTS = ts
		}

		evt := domain.RawEvent{
			Source:          "okx",
			SymbolNative:    ticker.InstID,
			SymbolCanonical: oa.canonicalSymbol,
			EventType:       "ticker",
			ExchangeTS:      exchangeTS,
			RecvTS:          recvTS,
			Bid:             bid,
			Ask:             ask,
			RawPayload:      fullPayload,
		}

		select {
		case oa.outCh <- evt:
		default:
			oa.logger.Warn("output channel full, dropping ticker event")
		}
	}
}

func parseUnixMillis(raw string) (time.Time, error) {
	ms, err := decimal.NewFromString(raw)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(ms.IntPart()).UTC(), nil
}
