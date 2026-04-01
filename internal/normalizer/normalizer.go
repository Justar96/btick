package normalizer

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/justar9/btick/internal/domain"
	"github.com/justar9/btick/internal/metrics"
)

const defaultMaxSeen = 100000

type dedupShard struct {
	mu         sync.Mutex
	seen       map[string]struct{}
	order      []string
	head       int
	maxEntries int
}

// Normalizer receives raw events from adapters, assigns UUIDs, deduplicates, and forwards.
type Normalizer struct {
	inCh            <-chan domain.RawEvent
	outCh           chan<- domain.RawEvent
	canonicalSymbol string
	logger          *slog.Logger

	// Dedup: per-source bounded LRU of (source, trade_id)
	shards  sync.Map // map[string]*dedupShard
	maxSeen int
}

// New creates a Normalizer. canonicalSymbol is stamped on every outgoing event.
// Pass source names to pre-initialize dedup shards for those sources.
func New(inCh <-chan domain.RawEvent, outCh chan<- domain.RawEvent, canonicalSymbol string, sources []string, logger *slog.Logger) *Normalizer {
	n := &Normalizer{
		inCh:            inCh,
		outCh:           outCh,
		canonicalSymbol: canonicalSymbol,
		logger:          logger.With("component", "normalizer", "symbol", canonicalSymbol),
		maxSeen:         defaultMaxSeen,
	}
	for _, source := range sources {
		n.shards.Store(source, newDedupShard(n.maxSeen))
	}
	return n
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

	// Stamp canonical symbol
	evt.SymbolCanonical = n.canonicalSymbol

	select {
	case n.outCh <- evt:
	default:
		metrics.IncChannelDrop("normalizer")
		n.logger.Warn("output channel full, dropping normalized event",
			"source", evt.Source,
			"trade_id", evt.TradeID,
		)
	}
}

func (n *Normalizer) isDuplicate(key string) bool {
	shard := n.dedupShard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	if _, exists := shard.seen[key]; exists {
		return true
	}

	// Evict oldest entry if over limit
	if len(shard.seen) >= shard.maxEntries {
		oldest := shard.order[shard.head]
		if oldest != "" {
			delete(shard.seen, oldest)
		}
	}

	shard.seen[key] = struct{}{}
	shard.order[shard.head] = key
	shard.head = (shard.head + 1) % shard.maxEntries

	return false
}

func newDedupShard(maxEntries int) *dedupShard {
	if maxEntries <= 0 {
		maxEntries = defaultMaxSeen
	}

	return &dedupShard{
		seen:       make(map[string]struct{}, maxEntries),
		order:      make([]string, maxEntries),
		maxEntries: maxEntries,
	}
}

func (n *Normalizer) dedupShard(key string) *dedupShard {
	source := dedupSource(key)
	if shard, ok := n.shards.Load(source); ok {
		return shard.(*dedupShard)
	}

	shard := newDedupShard(n.maxSeen)
	actual, _ := n.shards.LoadOrStore(source, shard)
	return actual.(*dedupShard)
}

func dedupSource(key string) string {
	source, _, found := strings.Cut(key, ":")
	if found && source != "" {
		return source
	}
	return key
}

