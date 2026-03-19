package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/justar9/btc-price-tick/internal/engine"
	"github.com/justar9/btc-price-tick/internal/storage"
)

// Server is the HTTP/WS API server.
type Server struct {
	httpAddr string
	wsPath   string
	db       *storage.DB
	engine   *engine.SnapshotEngine
	wsHub    *WSHub
	logger   *slog.Logger
	srv      *http.Server
}

func NewServer(httpAddr, wsPath string, db *storage.DB, eng *engine.SnapshotEngine, logger *slog.Logger) *Server {
	s := &Server{
		httpAddr: httpAddr,
		wsPath:   wsPath,
		db:       db,
		engine:   eng,
		wsHub:    NewWSHub(logger),
		logger:   logger.With("component", "api"),
	}
	return s
}

func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()

	// REST endpoints
	mux.HandleFunc("GET /v1/price/latest", s.handleLatest)
	mux.HandleFunc("GET /v1/price/snapshots", s.handleSnapshots)
	mux.HandleFunc("GET /v1/price/ticks", s.handleTicks)
	mux.HandleFunc("GET /v1/price/raw", s.handleRaw)
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/health/feeds", s.handleFeedHealth)

	// WebSocket
	mux.HandleFunc("GET "+s.wsPath, s.wsHub.HandleWS)

	// CORS + content-type middleware
	handler := corsMiddleware(jsonMiddleware(mux))

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
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}
