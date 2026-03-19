package normalizer

import (
	"context"
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"github.com/justar9/btc-price-tick/internal/domain"
)

// Normalizer receives raw events from adapters, assigns UUIDs, deduplicates, and forwards.
type Normalizer struct {
	inCh    <-chan domain.RawEvent
	outCh   chan<- domain.RawEvent
	logger  *slog.Logger

	// Dedup: bounded LRU of (source, trade_id)
	mu      sync.Mutex
	seen    map[string]struct{}
	order   []string
	head    int       // ring buffer head
	maxSeen int
}

func New(inCh <-chan domain.RawEvent, outCh chan<- domain.RawEvent, logger *slog.Logger) *Normalizer {
	return &Normalizer{
		inCh:    inCh,
		outCh:   outCh,
		logger:  logger.With("component", "normalizer"),
		seen:    make(map[string]struct{}, 100000),
		order:   make([]string, 100000), // Pre-allocate full size for ring buffer
		maxSeen: 100000,
	}
}

func (n *Normalizer) Run(ctx context.Context) {
	n.logger.Info("normalizer started")
	for {
		select {
		case <-ctx.Done():
			n.logger.Info("normalizer stopped")
			return
		case evt, ok := <-n.inCh:
			if !ok {
				return
			}
			n.process(evt)
		}
	}
}

func (n *Normalizer) process(evt domain.RawEvent) {
	// Assign UUID v7 (time-ordered)
	evt.EventID = uuid.Must(uuid.NewV7())

	// Dedup trade events by (source, trade_id)
	if evt.EventType == "trade" && evt.TradeID != "" {
		key := evt.Source + ":" + evt.TradeID
		if n.isDuplicate(key) {
			return
		}
	}

	// Map symbol to canonical
	evt.SymbolCanonical = mapCanonicalSymbol(evt.Source, evt.SymbolNative)

	select {
	case n.outCh <- evt:
	default:
		n.logger.Warn("output channel full, dropping normalized event",
			"source", evt.Source,
			"trade_id", evt.TradeID,
		)
	}
}

func (n *Normalizer) isDuplicate(key string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()

	if _, exists := n.seen[key]; exists {
		return true
	}

	// Evict oldest entry if over limit
	if len(n.seen) >= n.maxSeen {
		oldest := n.order[n.head]
		if oldest != "" {
			delete(n.seen, oldest)
		}
	}

	n.seen[key] = struct{}{}
	n.order[n.head] = key
	n.head = (n.head + 1) % n.maxSeen

	return false
}

func mapCanonicalSymbol(source, native string) string {
	// All our configured sources map to BTC/USD
	switch source {
	case "binance":
		// btcusdt -> BTC/USD (treating USDT ≈ USD for canonical purposes)
		return "BTC/USD"
	case "coinbase":
		// BTC-USD -> BTC/USD
		return "BTC/USD"
	case "kraken":
		// BTC/USD -> BTC/USD
		return "BTC/USD"
	default:
		return "BTC/USD"
	}
}
