package api

import (
	"context"
	"log/slog"
	"net/http"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/justar9/btick/internal/config"
	"github.com/justar9/btick/internal/domain"
	"github.com/justar9/btick/internal/metrics"
)

// Store abstracts the database queries used by API handlers.
type Store interface {
	QuerySnapshots(ctx context.Context, start, end time.Time) ([]domain.Snapshot1s, error)
	QuerySnapshotAt(ctx context.Context, ts time.Time) (*domain.Snapshot1s, error)
	QueryCanonicalTicks(ctx context.Context, limit int) ([]domain.CanonicalTick, error)
	QueryRawTicks(ctx context.Context, source string, start, end time.Time, limit int) ([]domain.RawEvent, error)
	QueryClosestTradePerSource(ctx context.Context, ts time.Time, window time.Duration) ([]domain.VenueRefPrice, error)
	QueryFeedHealth(ctx context.Context) ([]domain.FeedHealth, error)
	CreateAPIAccount(ctx context.Context, email, name, tier, apiKeyHash, apiKeyPrefix string) (*domain.APIAccount, error)
	LookupAPIAccountByKeyHash(ctx context.Context, apiKeyHash string) (*domain.APIAccount, error)
	TouchAPIAccountLastUsed(ctx context.Context, accountID string, usedAt time.Time) error
}

// Engine abstracts the snapshot engine used by API handlers.
type Engine interface {
	LatestState(symbol string) *domain.LatestState
	Symbols() []string
	SnapshotCh() <-chan domain.Snapshot1s
	TickCh() <-chan domain.CanonicalTick
	SourcePriceCh() <-chan domain.SourcePriceEvent
}

// Server is the HTTP/WS API server.
type Server struct {
	httpAddr          string
	wsPath            string
	db                Store
	dbMu              sync.RWMutex
	symbolMetadata    symbolMetadataStore
	engine            Engine
	wsHub             *WSHub
	logger            *slog.Logger
	srv               *http.Server
	nowFunc           func() time.Time // injectable for testing; defaults to time.Now
	settlementWindow  time.Duration    // re-aggregation window for degraded/stale settlements
	minHealthySources int              // threshold for confirmed vs degraded
	accessCfg         config.AccessConfig
}

const maxWSBroadcastDrain = 256

type orderedWSMessage struct {
	msg   WSMessage
	order int
}

func (s *Server) now() time.Time {
	if s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now()
}

func NewServer(httpAddr, wsPath string, wsCfg config.WSConfig, pricingCfg config.PricingConfig, accessCfg config.AccessConfig, db Store, eng Engine, logger *slog.Logger) *Server {
	s := &Server{
		httpAddr:          httpAddr,
		wsPath:            wsPath,
		db:                normalizeStore(db),
		engine:            eng,
		wsHub:             NewWSHub(logger, wsCfg, eng.LatestState, eng.Symbols()),
		logger:            logger.With("component", "api"),
		settlementWindow:  pricingCfg.SettlementReaggregationWindow(),
		minHealthySources: pricingCfg.MinimumHealthySources,
		accessCfg:         accessCfg,
	}
	return s
}

func normalizeStore(db Store) Store {
	if db == nil {
		return nil
	}

	value := reflect.ValueOf(db)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return nil
		}
	}

	return db
}

func (s *Server) store() Store {
	s.dbMu.RLock()
	defer s.dbMu.RUnlock()
	return s.db
}

func (s *Server) hasStore() bool {
	return s.store() != nil
}

func (s *Server) SetStore(db Store) {
	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	s.db = normalizeStore(db)
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	publicRoute := func(handler http.Handler) http.Handler { return handler }
	requireStarter := func(handler http.Handler) http.Handler {
		return s.requireTier("starter", handler)
	}
	requirePro := func(handler http.Handler) http.Handler {
		return s.requireTier("pro", handler)
	}

	// REST endpoints
	mux.Handle("GET /v1/price/latest", publicRoute(http.HandlerFunc(s.handleLatest)))
	mux.Handle("GET /v1/price/snapshots", requireStarter(http.HandlerFunc(s.handleSnapshots)))
	mux.Handle("GET /v1/price/ticks", requireStarter(http.HandlerFunc(s.handleTicks)))
	mux.Handle("GET /v1/price/raw", requirePro(http.HandlerFunc(s.handleRaw)))
	mux.Handle("GET /v1/health", publicRoute(http.HandlerFunc(s.handleHealth)))
	mux.Handle("GET /v1/health/feeds", publicRoute(http.HandlerFunc(s.handleFeedHealth)))
	mux.Handle("GET /v1/price/settlement", requireStarter(http.HandlerFunc(s.handleSettlement)))
	mux.Handle("GET /v1/symbols", publicRoute(http.HandlerFunc(s.handleSymbols)))
	mux.Handle("GET /v1/metadata", publicRoute(http.HandlerFunc(s.handleMetadata)))
	mux.Handle("GET /metrics", publicRoute(metrics.Handler()))
	if s.accessCfg.Enabled {
		mux.Handle("GET /v1/auth/me", s.requireTier("", http.HandlerFunc(s.handleAuthMe)))
	}
	if s.accessCfg.Enabled && s.accessCfg.SignupEnabled {
		mux.Handle("POST /v1/auth/signup", http.HandlerFunc(s.handleSignup))
	}

	// WebSocket
	mux.Handle("GET "+s.wsPath, requireStarter(http.HandlerFunc(s.wsHub.HandleWS)))

	// CORS + content-type middleware
	return corsMiddleware(jsonMiddleware(mux))
}

func (s *Server) Run(ctx context.Context) error {
	handler := s.handler()

	s.srv = &http.Server{
		Addr:         s.httpAddr,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start WS hub
	go s.wsHub.Run(ctx)

	// Start WS broadcaster (reads from engine channels)
	go s.broadcastLoop(ctx)

	s.logger.Info("API server starting", "addr", s.httpAddr)

	errCh := make(chan error, 1)
	go func() {
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func (s *Server) broadcastLoop(ctx context.Context) {
	snapshotCh := s.engine.SnapshotCh()
	tickCh := s.engine.TickCh()
	sourcePriceCh := s.engine.SourcePriceCh()

	for {
		batch, nextSnapshotCh, nextTickCh, nextSourcePriceCh, ok := s.nextBroadcastBatch(ctx, snapshotCh, tickCh, sourcePriceCh)
		snapshotCh = nextSnapshotCh
		tickCh = nextTickCh
		sourcePriceCh = nextSourcePriceCh

		if !ok {
			if ctx.Err() != nil || (snapshotCh == nil && tickCh == nil && sourcePriceCh == nil) {
				return
			}
			continue
		}

		for _, msg := range batch {
			s.wsHub.Broadcast(msg)
		}
	}
}

func (s *Server) nextBroadcastBatch(
	ctx context.Context,
	snapshotCh <-chan domain.Snapshot1s,
	tickCh <-chan domain.CanonicalTick,
	sourcePriceCh <-chan domain.SourcePriceEvent,
) ([]WSMessage, <-chan domain.Snapshot1s, <-chan domain.CanonicalTick, <-chan domain.SourcePriceEvent, bool) {
	pending := make(map[string]orderedWSMessage, maxWSBroadcastDrain)
	order := 0
	coalesced := 0

	record := func(msg WSMessage) {
		key := msg.Type + "\x00" + msg.Symbol + "\x00" + msg.Source
		if _, ok := pending[key]; ok {
			coalesced++
		}
		pending[key] = orderedWSMessage{msg: msg, order: order}
		order++
	}

	for len(pending) == 0 {
		msg, nextSnapshotCh, nextTickCh, nextSourcePriceCh, ok := s.nextBroadcastMessage(ctx, snapshotCh, tickCh, sourcePriceCh, true)
		snapshotCh = nextSnapshotCh
		tickCh = nextTickCh
		sourcePriceCh = nextSourcePriceCh
		if !ok {
			return nil, snapshotCh, tickCh, sourcePriceCh, false
		}
		record(msg)
	}

	for drained := 1; drained < maxWSBroadcastDrain; drained++ {
		msg, nextSnapshotCh, nextTickCh, nextSourcePriceCh, ok := s.nextBroadcastMessage(ctx, snapshotCh, tickCh, sourcePriceCh, false)
		snapshotCh = nextSnapshotCh
		tickCh = nextTickCh
		sourcePriceCh = nextSourcePriceCh
		if !ok {
			break
		}
		record(msg)
	}

	ordered := make([]orderedWSMessage, 0, len(pending))
	for _, entry := range pending {
		ordered = append(ordered, entry)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].order < ordered[j].order
	})

	batch := make([]WSMessage, 0, len(ordered))
	for _, entry := range ordered {
		batch = append(batch, entry.msg)
	}

	metrics.AddWSCoalesced(coalesced)

	return batch, snapshotCh, tickCh, sourcePriceCh, true
}

func (s *Server) nextBroadcastMessage(
	ctx context.Context,
	snapshotCh <-chan domain.Snapshot1s,
	tickCh <-chan domain.CanonicalTick,
	sourcePriceCh <-chan domain.SourcePriceEvent,
	block bool,
) (WSMessage, <-chan domain.Snapshot1s, <-chan domain.CanonicalTick, <-chan domain.SourcePriceEvent, bool) {
	for {
		if snapshotCh == nil && tickCh == nil && sourcePriceCh == nil {
			return WSMessage{}, nil, nil, nil, false
		}

		if block {
			select {
			case <-ctx.Done():
				return WSMessage{}, snapshotCh, tickCh, sourcePriceCh, false
			case snap, ok := <-snapshotCh:
				if !ok {
					snapshotCh = nil
					continue
				}
				return snapshotToWSMessage(snap), snapshotCh, tickCh, sourcePriceCh, true
			case tick, ok := <-tickCh:
				if !ok {
					tickCh = nil
					continue
				}
				return tickToWSMessage(tick), snapshotCh, tickCh, sourcePriceCh, true
			case sp, ok := <-sourcePriceCh:
				if !ok {
					sourcePriceCh = nil
					continue
				}
				return sourcePriceToWSMessage(sp), snapshotCh, tickCh, sourcePriceCh, true
			}
		}

		select {
		case snap, ok := <-snapshotCh:
			if !ok {
				snapshotCh = nil
				continue
			}
			return snapshotToWSMessage(snap), snapshotCh, tickCh, sourcePriceCh, true
		case tick, ok := <-tickCh:
			if !ok {
				tickCh = nil
				continue
			}
			return tickToWSMessage(tick), snapshotCh, tickCh, sourcePriceCh, true
		case sp, ok := <-sourcePriceCh:
			if !ok {
				sourcePriceCh = nil
				continue
			}
			return sourcePriceToWSMessage(sp), snapshotCh, tickCh, sourcePriceCh, true
		default:
			return WSMessage{}, snapshotCh, tickCh, sourcePriceCh, false
		}
	}
}

func snapshotToWSMessage(snap domain.Snapshot1s) WSMessage {
	return WSMessage{
		Type:         "snapshot_1s",
		Symbol:       snap.CanonicalSymbol,
		TS:           snap.TSSecond.Format(time.RFC3339Nano),
		Price:        snap.CanonicalPrice.String(),
		Basis:        snap.Basis,
		IsStale:      snap.IsStale,
		IsDegraded:   snap.IsDegraded,
		QualityScore: snap.QualityScore.String(),
		SourceCount:  snap.SourceCount,
		SourcesUsed:  snap.SourcesUsed,
	}
}

func tickToWSMessage(tick domain.CanonicalTick) WSMessage {
	return WSMessage{
		Type:          "latest_price",
		Symbol:        tick.CanonicalSymbol,
		TS:            tick.TSEvent.Format(time.RFC3339Nano),
		Price:         tick.CanonicalPrice.String(),
		Basis:         tick.Basis,
		IsStale:       tick.IsStale,
		IsDegraded:    tick.IsDegraded,
		QualityScore:  tick.QualityScore.String(),
		SourceCount:   tick.SourceCount,
		SourcesUsed:   tick.SourcesUsed,
		SourceDetails: tick.SourceDetailsJSON,
	}
}

func sourcePriceToWSMessage(sp domain.SourcePriceEvent) WSMessage {
	return WSMessage{
		Type:      "source_price",
		Symbol:    sp.Symbol,
		Source:    sp.Source,
		TS:        sp.TS.Format(time.RFC3339Nano),
		Price:     sp.Price.String(),
		LatencyMs: sp.LatencyMs,
	}
}

// BroadcastSourceStatus broadcasts a source_status message to WS clients.
func (s *Server) BroadcastSourceStatus(symbol, source, connState string, stale bool) {
	s.wsHub.Broadcast(WSMessage{
		Type:      "source_status",
		Symbol:    symbol,
		Source:    source,
		TS:        time.Now().UTC().Format(time.RFC3339Nano),
		ConnState: connState,
		Stale:     stale,
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func jsonMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			w.Header().Set("Content-Type", "application/json")
		}
		next.ServeHTTP(w, r)
	})
}
