package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/justar9/btick/internal/config"
	"github.com/justar9/btick/internal/domain"
	"github.com/justar9/btick/internal/metrics"
	"github.com/justar9/btick/internal/storage"
)

const (
	pipelineLatencyLogInterval = time.Minute
	maxPipelineLatency         = time.Minute
)

type pipelineLatencyStats struct {
	since    time.Time
	count    int64
	totalMs  int64
	maxMs    int64
	le50Ms   int64
	le100Ms  int64
	le250Ms  int64
	le500Ms  int64
	le1000Ms int64
	gt1000Ms int64
}

// SnapshotEngine produces canonical ticks and 1-second snapshots.
type SnapshotEngine struct {
	cfg             config.PricingConfig
	canonicalSymbol string
	staleAfter      time.Duration
	db              *storage.DB
	logger          *slog.Logger

	// Input channel for normalized events
	inCh <-chan domain.RawEvent

	// Output channels
	snapshotCh chan domain.Snapshot1s
	tickCh     chan domain.CanonicalTick

	// Per-venue latest state
	mu            sync.RWMutex
	venueStates   map[string]*venueState
	lastCanonical decimal.Decimal
	lastBasis     string
	carryStart    time.Time

	// Latest state for API
	latestMu    sync.RWMutex
	latestState *domain.LatestState

	latencyMu sync.Mutex
	latency   pipelineLatencyStats

	// Semaphore to limit concurrent snapshot finalizations
	finalizeSem chan struct{}

	// Buffered channel for canonical tick DB writes
	ticksToWrite     chan domain.CanonicalTick
	snapshotsToWrite chan domain.Snapshot1s
}

type venueState struct {
	lastTrade *tradeInfo
	lastQuote *quoteInfo
}

type tradeInfo struct {
	price   decimal.Decimal
	ts      time.Time
	tradeID string
}

type quoteInfo struct {
	bid decimal.Decimal
	ask decimal.Decimal
	ts  time.Time
}

func NewSnapshotEngine(
	cfg config.PricingConfig,
	canonicalSymbol string,
	staleAfter time.Duration,
	db *storage.DB,
	inCh <-chan domain.RawEvent,
	logger *slog.Logger,
) *SnapshotEngine {
	return &SnapshotEngine{
		cfg:              cfg,
		canonicalSymbol:  canonicalSymbol,
		staleAfter:       staleAfter,
		db:               db,
		inCh:             inCh,
		snapshotCh:       make(chan domain.Snapshot1s, 100),
		tickCh:           make(chan domain.CanonicalTick, 1000),
		venueStates:      make(map[string]*venueState),
		logger:           logger.With("component", "snapshot_engine"),
		latency:          pipelineLatencyStats{since: time.Now().UTC()},
		finalizeSem:      make(chan struct{}, 10), // max 10 concurrent finalizations
		ticksToWrite:     make(chan domain.CanonicalTick, 500),
		snapshotsToWrite: make(chan domain.Snapshot1s, 32),
	}
}

// SnapshotCh returns the channel for finalized snapshots (for WS broadcast).
func (e *SnapshotEngine) SnapshotCh() <-chan domain.Snapshot1s { return e.snapshotCh }

// TickCh returns the channel for canonical ticks.
func (e *SnapshotEngine) TickCh() <-chan domain.CanonicalTick { return e.tickCh }

// LatestState returns the current latest state for the API.
func (e *SnapshotEngine) LatestState() *domain.LatestState {
	e.latestMu.RLock()
	defer e.latestMu.RUnlock()
	if e.latestState == nil {
		return nil
	}
	cp := *e.latestState
	return &cp
}

// Run starts the engine. It consumes events and runs a 1-second snapshot timer.
func (e *SnapshotEngine) Run(ctx context.Context) {
	e.logger.Info("snapshot engine started",
		"mode", e.cfg.Mode,
		"min_sources", e.cfg.MinimumHealthySources,
		"trade_freshness_ms", e.cfg.TradeFreshnessWindowMs,
	)

	var (
		finalizeWg   sync.WaitGroup
		writerWg     sync.WaitGroup
		writerCancel context.CancelFunc
	)

	if e.db != nil {
		writerCtx, cancel := context.WithCancel(context.Background())
		writerCancel = cancel

		writerWg.Add(2)
		go func() {
			defer writerWg.Done()
			e.runTickWriter(writerCtx)
		}()
		go func() {
			defer writerWg.Done()
			e.runSnapshotWriter(writerCtx)
		}()
	}
	defer func() {
		finalizeWg.Wait()
		if writerCancel != nil {
			writerCancel()
			writerWg.Wait()
		}
	}()

	// Wait until the next second boundary, then tick every second
	now := time.Now().UTC()
	nextSecond := now.Truncate(time.Second).Add(time.Second)
	waitDur := nextSecond.Sub(now)

	select {
	case <-time.After(waitDur):
	case <-ctx.Done():
		return
	}

	snapshotTicker := time.NewTicker(time.Second)
	defer snapshotTicker.Stop()

	// Also start the watermark delay timer
	watermark := time.Duration(e.cfg.LateArrivalGraceMs) * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			e.logger.Info("snapshot engine stopped")
			return

		case evt, ok := <-e.inCh:
			if !ok {
				return
			}
			e.updateVenueState(evt)

		case t := <-snapshotTicker.C:
			// Finalize the snapshot for the previous second, after watermark delay
			snapshotSecond := t.UTC().Truncate(time.Second).Add(-time.Second)

			// Acquire semaphore (non-blocking check first)
			select {
			case e.finalizeSem <- struct{}{}:
				finalizeWg.Add(1)
				go func(ss time.Time) {
					defer func() {
						<-e.finalizeSem
						finalizeWg.Done()
					}()
					// Wait for watermark
					time.Sleep(watermark)
					e.finalizeSnapshot(ctx, ss)
				}(snapshotSecond)
			default:
				e.logger.Warn("snapshot finalization backlogged, skipping", "ts", snapshotSecond)
			}
		}
	}
}

func (e *SnapshotEngine) updateVenueState(evt domain.RawEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()

	vs, ok := e.venueStates[evt.Source]
	if !ok {
		vs = &venueState{}
		e.venueStates[evt.Source] = vs
	}

	switch evt.EventType {
	case "trade":
		vs.lastTrade = &tradeInfo{
			price:   evt.Price,
			ts:      evt.ExchangeTS,
			tradeID: evt.TradeID,
		}
		// Emit canonical tick on each trade event
		e.emitTick(evt)

	case "ticker":
		vs.lastQuote = &quoteInfo{
			bid: evt.Bid,
			ask: evt.Ask,
			ts:  evt.ExchangeTS,
		}
	}

	e.recordPipelineLatency(evt)
}

func (e *SnapshotEngine) emitTick(evt domain.RawEvent) {
	// Compute current canonical price with available data
	refs := e.computeVenueRefs(time.Now().UTC())
	if len(refs) == 0 {
		return
	}

	canonicalPrice, basis, _, _ := e.computeCanonical(refs)
	if canonicalPrice.Equal(e.lastCanonical) {
		return // no change
	}

	e.lastCanonical = canonicalPrice
	e.lastBasis = basis

	sources := make([]string, 0, len(refs))
	for _, r := range refs {
		sources = append(sources, r.Source)
	}

	detailsJSON, _ := json.Marshal(refs)

	tick := domain.CanonicalTick{
		TickID:            uuid.Must(uuid.NewV7()),
		TSEvent:           evt.ExchangeTS,
		CanonicalSymbol:   e.canonicalSymbol,
		CanonicalPrice:    canonicalPrice,
		Basis:             basis,
		IsStale:           false,
		IsDegraded:        len(refs) < e.cfg.MinimumHealthySources,
		QualityScore:      e.computeQuality(refs, false, 0),
		SourceCount:       len(refs),
		SourcesUsed:       sources,
		SourceDetailsJSON: detailsJSON,
	}

	// Update latest state
	e.updateLatestState(tick)

	// Queue for DB write (with backpressure)
	if e.db != nil {
		select {
		case e.ticksToWrite <- tick:
		default:
			metrics.IncChannelDrop("tick_writer")
			e.logger.Warn("canonical tick write buffer full, dropping")
		}
	}

	select {
	case e.tickCh <- tick:
	default:
	}
}

func (e *SnapshotEngine) finalizeSnapshot(ctx context.Context, snapshotSecond time.Time) {
	e.mu.RLock()
	refs := e.computeVenueRefs(snapshotSecond.Add(time.Second)) // end of second
	lastCanonical := e.lastCanonical
	carryStart := e.carryStart
	e.mu.RUnlock()

	now := time.Now().UTC()

	var (
		canonicalPrice decimal.Decimal
		basis          string
		isStale        bool
		isDegraded     bool
	)

	if len(refs) == 0 {
		// No venues available — carry forward
		if lastCanonical.IsZero() {
			return // nowhere to carry from yet
		}
		canonicalPrice = lastCanonical
		basis = "carry_forward"
		isStale = true
		isDegraded = true

		e.mu.Lock()
		if e.carryStart.IsZero() {
			e.carryStart = now
		}
		carryStart = e.carryStart
		e.mu.Unlock()

		// Check carry-forward max
		if now.Sub(carryStart) > time.Duration(e.cfg.CarryForwardMaxSeconds)*time.Second {
			e.logger.Warn("carry forward exceeded max duration",
				"seconds", now.Sub(carryStart).Seconds(),
			)
		}
	} else {
		canonicalPrice, basis, isDegraded, _ = e.computeCanonical(refs)
		isStale = false

		e.mu.Lock()
		e.lastCanonical = canonicalPrice
		e.lastBasis = basis
		e.carryStart = time.Time{}
		e.mu.Unlock()
	}

	sources := make([]string, 0, len(refs))
	var lastEventTS time.Time
	for _, r := range refs {
		sources = append(sources, r.Source)
		if r.EventTS.After(lastEventTS) {
			lastEventTS = r.EventTS
		}
	}

	carryDur := 0
	if !carryStart.IsZero() {
		carryDur = int(now.Sub(carryStart).Seconds())
	}

	detailsJSON, _ := json.Marshal(refs)

	snapshot := domain.Snapshot1s{
		TSSecond:            snapshotSecond,
		CanonicalSymbol:     e.canonicalSymbol,
		CanonicalPrice:      canonicalPrice,
		Basis:               basis,
		IsStale:             isStale,
		IsDegraded:          isDegraded,
		QualityScore:        e.computeQuality(refs, isStale, carryDur),
		SourceCount:         len(refs),
		SourcesUsed:         sources,
		SourceDetailsJSON:   detailsJSON,
		LastEventExchangeTS: lastEventTS,
		FinalizedAt:         now,
	}
	metrics.SetSnapshotFinalizeLag(snapshot.FinalizedAt.Sub(snapshot.TSSecond))

	// Queue DB persistence without delaying broadcasts.
	if e.db != nil {
		select {
		case e.snapshotsToWrite <- snapshot:
		default:
			metrics.IncChannelDrop("snapshot_writer")
			e.logger.Warn("snapshot write buffer full, dropping", "ts", snapshotSecond)
		}
	}

	// Broadcast
	select {
	case e.snapshotCh <- snapshot:
	default:
	}

	e.logger.Debug("snapshot finalized",
		"ts", snapshotSecond.Format(time.RFC3339),
		"price", canonicalPrice.String(),
		"basis", basis,
		"sources", len(refs),
		"stale", isStale,
	)

	e.maybeLogPipelineLatency(now)
}

// computeVenueRefs computes per-venue reference prices at given time.
func (e *SnapshotEngine) computeVenueRefs(atTime time.Time) []domain.VenueRefPrice {
	tradeFreshness := time.Duration(e.cfg.TradeFreshnessWindowMs) * time.Millisecond
	quoteFreshness := time.Duration(e.cfg.QuoteFreshnessWindowMs) * time.Millisecond

	var refs []domain.VenueRefPrice

	for source, vs := range e.venueStates {
		// Try trade first
		if vs.lastTrade != nil {
			age := atTime.Sub(vs.lastTrade.ts)
			if age <= tradeFreshness && age >= 0 {
				refs = append(refs, domain.VenueRefPrice{
					Source:   source,
					RefPrice: vs.lastTrade.price,
					Basis:    "trade",
					EventTS:  vs.lastTrade.ts,
					AgeMs:    age.Milliseconds(),
				})
				continue
			}
		}

		// Midpoint fallback
		if vs.lastQuote != nil {
			age := atTime.Sub(vs.lastQuote.ts)
			if age <= quoteFreshness && age >= 0 &&
				!vs.lastQuote.bid.IsZero() && !vs.lastQuote.ask.IsZero() {
				midpoint := vs.lastQuote.bid.Add(vs.lastQuote.ask).Div(decimal.NewFromInt(2))
				refs = append(refs, domain.VenueRefPrice{
					Source:   source,
					RefPrice: midpoint,
					Basis:    "midpoint",
					EventTS:  vs.lastQuote.ts,
					AgeMs:    age.Milliseconds(),
				})
				continue
			}
		}
		// Venue excluded for this snapshot
	}

	// Sort by source name for deterministic behavior
	sort.Slice(refs, func(i, j int) bool {
		return refs[i].Source < refs[j].Source
	})

	return refs
}

// computeCanonical computes the canonical price from venue reference prices.
func (e *SnapshotEngine) computeCanonical(refs []domain.VenueRefPrice) (decimal.Decimal, string, bool, int) {
	if len(refs) == 0 {
		return decimal.Zero, "none", true, 0
	}

	if e.cfg.Mode == "single_venue" || len(refs) == 1 {
		basis := "single_trade"
		if refs[0].Basis == "midpoint" {
			basis = "single_midpoint"
		}
		return refs[0].RefPrice, basis, len(refs) < e.cfg.MinimumHealthySources, 0
	}

	// Multi-venue median
	prices := make([]decimal.Decimal, len(refs))
	for i, r := range refs {
		prices[i] = r.RefPrice
	}

	median := computeMedian(prices)

	// Outlier rejection
	if e.cfg.OutlierRejectPct > 0 {
		threshold := decimal.NewFromFloat(e.cfg.OutlierRejectPct / 100.0)
		var filtered []decimal.Decimal
		for _, p := range prices {
			deviation := p.Sub(median).Abs().Div(median)
			if deviation.LessThanOrEqual(threshold) {
				filtered = append(filtered, p)
			}
		}
		if len(filtered) > 0 {
			prices = filtered
			median = computeMedian(prices)
		}
	}

	// Determine basis
	basis := "median_trade"
	hasMidpoint := false
	for _, r := range refs {
		if r.Basis == "midpoint" {
			hasMidpoint = true
			break
		}
	}
	if hasMidpoint {
		basis = "median_mixed"
	}

	isDegraded := len(prices) < e.cfg.MinimumHealthySources
	rejected := len(refs) - len(prices)

	return median, basis, isDegraded, rejected
}

// computeMedian returns the median of a slice of decimals.
func computeMedian(prices []decimal.Decimal) decimal.Decimal {
	n := len(prices)
	if n == 0 {
		return decimal.Zero
	}

	sorted := make([]decimal.Decimal, n)
	copy(sorted, prices)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].LessThan(sorted[j])
	})

	if n%2 == 1 {
		return sorted[n/2]
	}
	return sorted[n/2-1].Add(sorted[n/2]).Div(decimal.NewFromInt(2))
}

// computeQuality computes a 0-1 quality score.
func (e *SnapshotEngine) computeQuality(refs []domain.VenueRefPrice, isStale bool, carryDurSec int) decimal.Decimal {
	if isStale {
		// Decaying quality for carry-forward
		if e.cfg.CarryForwardMaxSeconds <= 0 {
			return decimal.Zero
		}
		decay := math.Max(0, 1.0-float64(carryDurSec)/float64(e.cfg.CarryForwardMaxSeconds))
		return decimal.NewFromFloat(decay * 0.3) // max 0.3 when stale
	}

	if len(refs) == 0 {
		return decimal.Zero
	}

	// Base score from source count
	maxSources := 3.0
	sourceScore := math.Min(float64(len(refs))/maxSources, 1.0)

	// Age penalty: average age, penalize if > 500ms
	var totalAge float64
	for _, r := range refs {
		totalAge += float64(r.AgeMs)
	}
	avgAge := totalAge / float64(len(refs))
	agePenalty := math.Max(0, 1.0-avgAge/2000.0) // 0 at 2s, 1 at 0s

	// Midpoint penalty
	midpointCount := 0.0
	for _, r := range refs {
		if r.Basis == "midpoint" {
			midpointCount++
		}
	}
	midpointPenalty := 1.0 - (midpointCount/float64(len(refs)))*0.2 // 20% penalty per midpoint

	score := sourceScore*0.5 + agePenalty*0.3 + midpointPenalty*0.2
	return decimal.NewFromFloat(math.Round(score*10000) / 10000)
}

func (e *SnapshotEngine) recordPipelineLatency(evt domain.RawEvent) {
	if evt.ExchangeTS.IsZero() || evt.RecvTS.IsZero() {
		return
	}

	lag := evt.RecvTS.Sub(evt.ExchangeTS)
	if lag < 0 || lag > maxPipelineLatency {
		return
	}
	metrics.ObservePipelineLatency(lag)

	lagMs := lag.Milliseconds()

	e.latencyMu.Lock()
	defer e.latencyMu.Unlock()

	if e.latency.since.IsZero() {
		e.latency.since = evt.RecvTS.UTC()
	}

	e.latency.count++
	e.latency.totalMs += lagMs
	if lagMs > e.latency.maxMs {
		e.latency.maxMs = lagMs
	}

	switch {
	case lagMs <= 50:
		e.latency.le50Ms++
	case lagMs <= 100:
		e.latency.le100Ms++
	case lagMs <= 250:
		e.latency.le250Ms++
	case lagMs <= 500:
		e.latency.le500Ms++
	case lagMs <= 1000:
		e.latency.le1000Ms++
	default:
		e.latency.gt1000Ms++
	}
}

func (e *SnapshotEngine) maybeLogPipelineLatency(now time.Time) {
	e.latencyMu.Lock()
	if e.latency.count == 0 || now.Sub(e.latency.since) < pipelineLatencyLogInterval {
		e.latencyMu.Unlock()
		return
	}

	stats := e.latency
	e.latency = pipelineLatencyStats{since: now.UTC()}
	e.latencyMu.Unlock()

	avgMs := math.Round((float64(stats.totalMs)/float64(stats.count))*100) / 100
	window := now.Sub(stats.since)
	if window <= 0 {
		window = pipelineLatencyLogInterval
	}

	e.logger.Info("pipeline latency histogram",
		"window", window,
		"samples", stats.count,
		"avg_ms", avgMs,
		"max_ms", stats.maxMs,
		"le_50ms", stats.le50Ms,
		"le_100ms", stats.le100Ms,
		"le_250ms", stats.le250Ms,
		"le_500ms", stats.le500Ms,
		"le_1000ms", stats.le1000Ms,
		"gt_1000ms", stats.gt1000Ms,
	)
}
func (e *SnapshotEngine) updateLatestState(tick domain.CanonicalTick) {
	e.latestMu.Lock()
	defer e.latestMu.Unlock()

	e.latestState = &domain.LatestState{
		Symbol:       tick.CanonicalSymbol,
		TS:           tick.TSEvent,
		Price:        tick.CanonicalPrice,
		Basis:        tick.Basis,
		IsStale:      tick.IsStale,
		IsDegraded:   tick.IsDegraded,
		QualityScore: tick.QualityScore,
		SourceCount:  tick.SourceCount,
		SourcesUsed:  tick.SourcesUsed,
	}
}
