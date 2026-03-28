package api

import (
	"context"
	"log/slog"
	"net/http"
	"reflect"
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
}

// Engine abstracts the snapshot engine used by API handlers.
type Engine interface {
	LatestState() *domain.LatestState
	SnapshotCh() <-chan domain.Snapshot1s
	TickCh() <-chan domain.CanonicalTick
}

// Server is the HTTP/WS API server.
type Server struct {
	httpAddr            string
	wsPath              string
	db                  Store
	engine              Engine
	wsHub               *WSHub
	logger              *slog.Logger
	srv                 *http.Server
	nowFunc             func() time.Time // injectable for testing; defaults to time.Now
	settlementWindow    time.Duration    // re-aggregation window for degraded/stale settlements
	minHealthySources   int              // threshold for confirmed vs degraded
}

func (s *Server) now() time.Time {
	if s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now()
}

func NewServer(httpAddr, wsPath string, wsCfg config.WSConfig, pricingCfg config.PricingConfig, db Store, eng Engine, logger *slog.Logger) *Server {
	s := &Server{
		httpAddr:          httpAddr,
		wsPath:            wsPath,
		db:                normalizeStore(db),
		engine:            eng,
		wsHub:             NewWSHub(logger, wsCfg, eng.LatestState),
		logger:            logger.With("component", "api"),
		settlementWindow:  pricingCfg.SettlementReaggregationWindow(),
		minHealthySources: pricingCfg.MinimumHealthySources,
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

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()

	// REST endpoints
	mux.HandleFunc("GET /v1/price/latest", s.handleLatest)
	mux.HandleFunc("GET /v1/price/snapshots", s.handleSnapshots)
	mux.HandleFunc("GET /v1/price/ticks", s.handleTicks)
	mux.HandleFunc("GET /v1/price/raw", s.handleRaw)
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/health/feeds", s.handleFeedHealth)
	mux.HandleFunc("GET /v1/price/settlement", s.handleSettlement)
	mux.Handle("GET /metrics", metrics.Handler())

	// WebSocket
	mux.HandleFunc("GET "+s.wsPath, s.wsHub.HandleWS)

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
	for {
		select {
		case <-ctx.Done():
			return
		case snap, ok := <-s.engine.SnapshotCh():
			if !ok {
				return
			}
			s.wsHub.Broadcast(WSMessage{
				Type:         "snapshot_1s",
				TS:           snap.TSSecond.Format(time.RFC3339Nano),
				Price:        snap.CanonicalPrice.String(),
				Basis:        snap.Basis,
				IsStale:      snap.IsStale,
				QualityScore: snap.QualityScore.String(),
				SourceCount:  snap.SourceCount,
				SourcesUsed:  snap.SourcesUsed,
			})
		case tick, ok := <-s.engine.TickCh():
			if !ok {
				return
			}
			s.wsHub.Broadcast(WSMessage{
				Type:         "latest_price",
				TS:           tick.TSEvent.Format(time.RFC3339Nano),
				Price:        tick.CanonicalPrice.String(),
				Basis:        tick.Basis,
				IsStale:      tick.IsStale,
				QualityScore: tick.QualityScore.String(),
				SourceCount:  tick.SourceCount,
				SourcesUsed:  tick.SourcesUsed,
			})
		}
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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
