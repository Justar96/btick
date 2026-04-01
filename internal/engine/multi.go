package engine

import (
	"sync"

	"github.com/justar9/btick/internal/domain"
)

// MultiEngine aggregates multiple per-symbol SnapshotEngines behind a single
// interface that the API server can consume.
type MultiEngine struct {
	engines map[string]*SnapshotEngine // keyed by canonical symbol
	symbols []string                   // ordered list of symbols

	snapshotCh chan domain.Snapshot1s
	tickCh     chan domain.CanonicalTick
}

// NewMultiEngine creates a MultiEngine from a set of per-symbol engines.
// It merges their snapshot and tick channels into single output channels.
func NewMultiEngine(engines map[string]*SnapshotEngine, symbols []string) *MultiEngine {
	m := &MultiEngine{
		engines:    engines,
		symbols:    symbols,
		snapshotCh: make(chan domain.Snapshot1s, 100*len(engines)),
		tickCh:     make(chan domain.CanonicalTick, 1000*len(engines)),
	}

	// Merge per-engine channels into the combined output channels.
	var wg sync.WaitGroup
	for _, eng := range engines {
		e := eng
		wg.Add(2)
		go func() {
			defer wg.Done()
			for snap := range e.SnapshotCh() {
				m.snapshotCh <- snap
			}
		}()
		go func() {
			defer wg.Done()
			for tick := range e.TickCh() {
				m.tickCh <- tick
			}
		}()
	}

	// Close merged channels once all sources are done.
	go func() {
		wg.Wait()
		close(m.snapshotCh)
		close(m.tickCh)
	}()

	return m
}

// LatestState returns the latest price state for the given symbol.
// Returns nil if symbol is unknown or no data yet.
func (m *MultiEngine) LatestState(symbol string) *domain.LatestState {
	if eng, ok := m.engines[symbol]; ok {
		return eng.LatestState()
	}
	return nil
}

// Symbols returns the ordered list of configured canonical symbols.
func (m *MultiEngine) Symbols() []string {
	return m.symbols
}

// SnapshotCh returns the merged snapshot channel for all symbols.
func (m *MultiEngine) SnapshotCh() <-chan domain.Snapshot1s {
	return m.snapshotCh
}

// TickCh returns the merged tick channel for all symbols.
func (m *MultiEngine) TickCh() <-chan domain.CanonicalTick {
	return m.tickCh
}
