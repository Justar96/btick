package engine

import (
	"sync"

	"github.com/justar9/btick/internal/domain"
)

type attachableEngine interface {
	LatestState(symbol string) *domain.LatestState
	SnapshotCh() <-chan domain.Snapshot1s
	TickCh() <-chan domain.CanonicalTick
	SourcePriceCh() <-chan domain.SourcePriceEvent
}

// ProxyEngine exposes stable channels and symbol metadata before the runtime
// pipeline is fully initialized, then forwards data once attached.
type ProxyEngine struct {
	mu            sync.RWMutex
	symbols       []string
	current       attachableEngine
	snapshotCh    chan domain.Snapshot1s
	tickCh        chan domain.CanonicalTick
	sourcePriceCh chan domain.SourcePriceEvent
}

func NewProxyEngine(symbols []string) *ProxyEngine {
	cloned := append([]string(nil), symbols...)
	return &ProxyEngine{
		symbols:       cloned,
		snapshotCh:    make(chan domain.Snapshot1s, 100),
		tickCh:        make(chan domain.CanonicalTick, 1000),
		sourcePriceCh: make(chan domain.SourcePriceEvent, 100),
	}
}

func (p *ProxyEngine) Attach(runtime attachableEngine) {
	p.mu.Lock()
	if p.current != nil {
		p.mu.Unlock()
		return
	}
	p.current = runtime
	p.mu.Unlock()

	go func() {
		for snap := range runtime.SnapshotCh() {
			p.snapshotCh <- snap
		}
	}()

	go func() {
		for tick := range runtime.TickCh() {
			p.tickCh <- tick
		}
	}()

	go func() {
		for sp := range runtime.SourcePriceCh() {
			p.sourcePriceCh <- sp
		}
	}()
}

func (p *ProxyEngine) LatestState(symbol string) *domain.LatestState {
	p.mu.RLock()
	runtime := p.current
	p.mu.RUnlock()
	if runtime == nil {
		return nil
	}
	return runtime.LatestState(symbol)
}

func (p *ProxyEngine) Symbols() []string {
	return append([]string(nil), p.symbols...)
}

func (p *ProxyEngine) SnapshotCh() <-chan domain.Snapshot1s {
	return p.snapshotCh
}

func (p *ProxyEngine) TickCh() <-chan domain.CanonicalTick {
	return p.tickCh
}

func (p *ProxyEngine) SourcePriceCh() <-chan domain.SourcePriceEvent {
	return p.sourcePriceCh
}
