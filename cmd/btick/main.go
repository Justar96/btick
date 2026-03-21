package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/justar9/btick/internal/adapter"
	"github.com/justar9/btick/internal/api"
	"github.com/justar9/btick/internal/config"
	"github.com/justar9/btick/internal/domain"
	"github.com/justar9/btick/internal/engine"
	"github.com/justar9/btick/internal/metrics"
	"github.com/justar9/btick/internal/normalizer"
	"github.com/justar9/btick/internal/storage"
)

// safeGo runs fn in a goroutine with panic recovery. On panic it logs the
// stack trace, marks the WaitGroup done, and cancels the context so the
// rest of the process shuts down cleanly instead of crashing silently.
func safeGo(wg *sync.WaitGroup, cancel context.CancelFunc, logger *slog.Logger, name string, fn func()) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				logger.Error("goroutine panicked",
					"component", name,
					"panic", fmt.Sprintf("%v", r),
					"stack", string(debug.Stack()),
				)
				cancel() // trigger graceful shutdown
			}
		}()
		fn()
	}()
}

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	// Structured JSON logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	logger.Info("btick starting", "config", *configPath)

	// Load config
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Info("shutdown signal received", "signal", sig)
		cancel()
	}()

	// Database (optional — service can run without DB for testing)
	var ingestDB *storage.DB
	var queryDB *storage.DB
	var apiDB api.Store
	if cfg.Database.DSN != "" {
		ingestDB, queryDB, err = storage.NewPools(ctx, cfg.Database, logger)
		if err != nil {
			logger.Warn("database connection failed, running without persistence", "error", err)
		} else {
			defer ingestDB.Close()
			if queryDB != nil {
				defer queryDB.Close()
			}
			ingestMaxConns, queryMaxConns := cfg.Database.PoolMaxConns()
			logger.Info("database connected",
				"ingest_max_conns", ingestMaxConns,
				"query_max_conns", queryMaxConns,
			)
		}
	}
	if queryDB != nil {
		apiDB = queryDB
	} else if ingestDB != nil {
		apiDB = ingestDB
	}

	// Channels
	// Adapters -> Normalizer
	rawCh := make(chan domain.RawEvent, 10000)
	// Normalizer -> (Writer + SnapshotEngine)
	normalizedCh := make(chan domain.RawEvent, 10000)
	// Fan-out from normalizedCh to writer and engine
	writerCh := make(chan domain.RawEvent, 10000)
	engineCh := make(chan domain.RawEvent, 10000)

	var wg sync.WaitGroup

	// Start feed adapters
	for _, src := range cfg.Sources {
		if !src.Enabled {
			logger.Info("source disabled, skipping", "source", src.Name)
			continue
		}

		s := src
		safeGo(&wg, cancel, logger, "adapter-"+s.Name, func() {
			startAdapter(ctx, s, rawCh, logger)
		})
	}

	// Normalizer
	norm := normalizer.New(rawCh, normalizedCh, logger)
	safeGo(&wg, cancel, logger, "normalizer", func() {
		norm.Run(ctx)
	})

	// Fan-out: normalizedCh -> writerCh + engineCh
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(writerCh)
		defer close(engineCh)
		var writerDrops, engineDrops int64
		for {
			select {
			case <-ctx.Done():
				if writerDrops > 0 || engineDrops > 0 {
					logger.Warn("fan-out final drop counts",
						"writer_drops", writerDrops,
						"engine_drops", engineDrops,
					)
				}
				return
			case evt, ok := <-normalizedCh:
				if !ok {
					return
				}
				select {
				case writerCh <- evt:
				default:
					writerDrops++
					metrics.IncChannelDrop("writer")
					if writerDrops%1000 == 1 {
						logger.Warn("writer channel full, dropping event",
							"source", evt.Source,
							"total_drops", writerDrops,
						)
					}
				}
				select {
				case engineCh <- evt:
				default:
					engineDrops++
					metrics.IncChannelDrop("engine")
					if engineDrops%1000 == 1 {
						logger.Warn("engine channel full, dropping event",
							"source", evt.Source,
							"total_drops", engineDrops,
						)
					}
				}
			}
		}
	}()

	// Raw event writer
	if ingestDB != nil {
		writer := storage.NewWriter(ingestDB,
			cfg.Storage.BatchInsertMaxRows,
			cfg.Storage.BatchInsertMaxDelay(),
			logger,
		)
		safeGo(&wg, cancel, logger, "writer", func() {
			writer.Run(ctx, writerCh)
		})
	}

	// Snapshot engine
	eng := engine.NewSnapshotEngine(
		cfg.Pricing,
		cfg.CanonicalSymbol,
		cfg.Health.CanonicalStaleAfter(),
		ingestDB,
		engineCh,
		logger,
	)
	safeGo(&wg, cancel, logger, "snapshot-engine", func() {
		eng.Run(ctx)
	})

	// Note: canonical ticks are written to DB by the snapshot engine directly
	// and broadcast to WebSocket clients by the API server's broadcast loop.
	// No separate tick writer goroutine is needed.

	// Retention pruner
	if ingestDB != nil {
		pruner := storage.NewPruner(ingestDB, storage.PrunerConfig{
			RawRetentionDays:       cfg.Storage.RawRetentionDays,
			SnapshotsRetentionDays: cfg.Storage.SnapshotsRetentionDays,
			CanonicalRetentionDays: cfg.Storage.CanonicalRetentionDays,
		}, logger)
		safeGo(&wg, cancel, logger, "pruner", func() {
			pruner.Run(ctx)
		})
	}

	// Feed health updater
	if ingestDB != nil {
		safeGo(&wg, cancel, logger, "feed-health", func() {
			updateFeedHealth(ctx, cfg, ingestDB, logger)
		})
	}

	// API server
	srv := api.NewServer(cfg.Server.HTTPAddr, cfg.Server.WSPath, cfg.Server.WS, apiDB, eng, logger)
	safeGo(&wg, cancel, logger, "api-server", func() {
		if err := srv.Run(ctx); err != nil {
			logger.Error("API server error", "error", err)
		}
	})

	logger.Info("all components running",
		"http", cfg.Server.HTTPAddr,
		"ws", cfg.Server.WSPath,
	)

	// Wait for shutdown
	wg.Wait()
	logger.Info("btick stopped")
}

func startAdapter(ctx context.Context, src config.SourceConfig, outCh chan<- domain.RawEvent, logger *slog.Logger) {
	switch src.Name {
	case "binance":
		a := adapter.NewBinanceAdapter(
			src.WSURL,
			src.NativeSymbol,
			src.PingInterval(),
			src.MaxConnLifetime(),
			outCh,
			logger,
		)
		a.Run(ctx)

	case "coinbase":
		a := adapter.NewCoinbaseAdapter(
			src.WSURL,
			src.NativeSymbol,
			src.JWT,
			src.PingInterval(),
			outCh,
			logger,
		)
		a.Run(ctx)

	case "kraken":
		a := adapter.NewKrakenAdapter(
			src.WSURL,
			src.NativeSymbol,
			src.UseTickerFallback,
			src.PingInterval(),
			outCh,
			logger,
		)
		a.Run(ctx)

	default:
		logger.Error("unknown source", "name", src.Name)
	}
}

func updateFeedHealth(ctx context.Context, cfg *config.Config, db *storage.DB, logger *slog.Logger) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Periodic feed health updates would normally read from adapter state
			// For now this is a placeholder that will be enhanced with adapter state access
		}
	}
}
