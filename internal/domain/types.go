package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// RawEvent is the normalized internal representation of an exchange message.
type RawEvent struct {
	EventID         uuid.UUID       `json:"event_id"`
	Source          string          `json:"source"`
	SymbolNative    string          `json:"symbol_native"`
	SymbolCanonical string          `json:"symbol_canonical"`
	EventType       string          `json:"event_type"` // "trade" or "ticker"
	ExchangeTS      time.Time       `json:"exchange_ts"`
	RecvTS          time.Time       `json:"recv_ts"`
	Price           decimal.Decimal `json:"price"`
	Size            decimal.Decimal `json:"size"`
	Side            string          `json:"side"` // "buy" or "sell"
	TradeID         string          `json:"trade_id"`
	Sequence        string          `json:"sequence,omitempty"`
	Bid             decimal.Decimal `json:"bid,omitempty"`
	Ask             decimal.Decimal `json:"ask,omitempty"`
	RawPayload      []byte          `json:"raw_payload"`
}

// VenueRefPrice is the reference price computed for a single venue at snapshot time.
type VenueRefPrice struct {
	Source   string          `json:"source"`
	RefPrice decimal.Decimal `json:"ref_price"`
	Basis    string          `json:"basis"` // "trade", "midpoint"
	EventTS  time.Time       `json:"event_ts"`
	AgeMs    int64           `json:"age_ms"`
}

// CanonicalTick represents an irregular canonical price change.
type CanonicalTick struct {
	TickID            uuid.UUID       `json:"tick_id"`
	TSEvent           time.Time       `json:"ts_event"`
	CanonicalSymbol   string          `json:"canonical_symbol"`
	CanonicalPrice    decimal.Decimal `json:"canonical_price"`
	Basis             string          `json:"basis"`
	IsStale           bool            `json:"is_stale"`
	IsDegraded        bool            `json:"is_degraded"`
	QualityScore      decimal.Decimal `json:"quality_score"`
	SourceCount       int             `json:"source_count"`
	SourcesUsed       []string        `json:"sources_used"`
	SourceDetailsJSON []byte          `json:"source_details_json"`
}

// Snapshot1s is the immutable 1-second canonical snapshot.
type Snapshot1s struct {
	TSSecond            time.Time       `json:"ts_second"`
	CanonicalSymbol     string          `json:"canonical_symbol"`
	CanonicalPrice      decimal.Decimal `json:"canonical_price"`
	Basis               string          `json:"basis"` // "median_trade", "median_mixed", "single_trade", "carry_forward"
	IsStale             bool            `json:"is_stale"`
	IsDegraded          bool            `json:"is_degraded"`
	QualityScore        decimal.Decimal `json:"quality_score"`
	SourceCount         int             `json:"source_count"`
	SourcesUsed         []string        `json:"sources_used"`
	SourceDetailsJSON   []byte          `json:"source_details_json"`
	LastEventExchangeTS time.Time       `json:"last_event_exchange_ts,omitempty"`
	FinalizedAt         time.Time       `json:"finalized_at"`
}

// FeedHealth tracks per-source connection and data quality state.
type FeedHealth struct {
	Source            string    `json:"source"`
	ConnState         string    `json:"conn_state"` // "connected", "connecting", "disconnected"
	LastMessageTS     time.Time `json:"last_message_ts,omitempty"`
	LastTradeTS       time.Time `json:"last_trade_ts,omitempty"`
	LastHeartbeatTS   time.Time `json:"last_heartbeat_ts,omitempty"`
	ReconnectCount1h  int       `json:"reconnect_count_1h"`
	ConsecutiveErrors int       `json:"consecutive_errors"`
	MedianLagMs       int       `json:"median_lag_ms,omitempty"`
	Stale             bool      `json:"stale"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// LatestState is the in-memory representation exposed via REST/WS.
type LatestState struct {
	Symbol       string          `json:"symbol"`
	TS           time.Time       `json:"ts"`
	Price        decimal.Decimal `json:"price"`
	Basis        string          `json:"basis"`
	IsStale      bool            `json:"is_stale"`
	IsDegraded   bool            `json:"is_degraded"`
	QualityScore decimal.Decimal `json:"quality_score"`
	SourceCount  int             `json:"source_count"`
	SourcesUsed  []string        `json:"sources_used"`
}

// SourcePriceEvent is emitted when a venue's latest trade price changes.
type SourcePriceEvent struct {
	Symbol string          `json:"symbol"`
	Source string          `json:"source"`
	Price  decimal.Decimal `json:"price"`
	TS     time.Time       `json:"ts"`
}

// SourceStatusEvent is emitted when a feed's connection state changes.
type SourceStatusEvent struct {
	Symbol    string    `json:"symbol"`
	Source    string    `json:"source"`
	ConnState string    `json:"conn_state"`
	Stale     bool      `json:"stale"`
	TS        time.Time `json:"ts"`
}
