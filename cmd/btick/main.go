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

const databaseRetryDelay = 5 * time.Second

func resolveLogLevel() slog.Level {
	level := os.Getenv("BTICK_LOG_LEVEL")
	if level == "" {
		level = os.Getenv("LOG_LEVEL")
	}

	var parsed slog.Level
	if err := parsed.UnmarshalText([]byte(level)); err != nil {
		return slog.LevelInfo
	}

	return parsed
}

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
		Level: resolveLogLevel(),
	}))
	slog.SetDefault(logger)

	logger.Info("btick starting", "config", *configPath)

	// Load config
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	if len(cfg.Symbols) == 0 {
		logger.Error("no symbols configured")
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

	var wg sync.WaitGroup
	symbols := make([]string, 0, len(cfg.Symbols))
	for _, sym := range cfg.Symbols {
		symbols = append(symbols, sym.Canonical)
	}

	proxyEngine := engine.NewProxyEngine(symbols)
	srv := api.NewServer(cfg.Server.HTTPAddr, cfg.Server.WSPath, cfg.Server.WS, cfg.Pricing, cfg.Access, nil, proxyEngine, logger)
	srv.SetSymbolMetadata(api.BuildSymbolMetadata(cfg.Symbols))
	safeGo(&wg, cancel, logger, "api-server", func() {
		if err := srv.Run(ctx); err != nil {
			logger.Error("API server error", "error", err)
		}
	})

	if cfg.Database.DSN == "" {
		startRuntime(ctx, cancel, &wg, cfg, logger, nil, nil, srv, proxyEngine)
	} else {
		safeGo(&wg, cancel, logger, "database-init", func() {
			waitForDatabaseAndStart(ctx, cancel, &wg, cfg, logger, srv, proxyEngine)
		})
	}

	// Wait for shutdown
	wg.Wait()
	logger.Info("btick stopped")
}

func waitForDatabaseAndStart(
	ctx context.Context,
	cancel context.CancelFunc,
	wg *sync.WaitGroup,
	cfg *config.Config,
	logger *slog.Logger,
	srv *api.Server,
	proxy *engine.ProxyEngine,
) {
	for {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, 15*time.Second)
		ingestDB, queryDB, err := storage.NewPools(attemptCtx, cfg.Database, logger)
		attemptCancel()
		if err == nil {
			defer ingestDB.Close()
			if queryDB != nil {
				defer queryDB.Close()
			}
			startRuntime(ctx, cancel, wg, cfg, logger, ingestDB, queryDB, srv, proxy)
			<-ctx.Done()
			return
		}

		if ctx.Err() != nil {
			return
		}

		logger.Warn("database connection failed, runtime startup deferred",
			"error", err,
			"retry_in", databaseRetryDelay.String(),
		)

		select {
		case <-ctx.Done():
			return
		case <-time.After(databaseRetryDelay):
		}
	}
}

func startRuntime(
	ctx context.Context,
	cancel context.CancelFunc,
	wg *sync.WaitGroup,
	cfg *config.Config,
	logger *slog.Logger,
	ingestDB *storage.DB,
	queryDB *storage.DB,
	srv *api.Server,
	proxy *engine.ProxyEngine,
) {
	var apiDB api.Store
	if queryDB != nil {
		apiDB = queryDB
	} else if ingestDB != nil {
		apiDB = ingestDB
	}
	srv.SetStore(apiDB)

	if ingestDB != nil {
		ingestMaxConns, queryMaxConns := cfg.Database.PoolMaxConns()
		logger.Info("database connected",
			"ingest_max_conns", ingestMaxConns,
			"query_max_conns", queryMaxConns,
		)
	}

	writerCh := make(chan domain.RawEvent, 10000)
	if ingestDB != nil {
		writer := storage.NewWriter(ingestDB,
			cfg.Storage.BatchInsertMaxRows,
			cfg.Storage.BatchInsertMaxDelay(),
			logger,
		)
		safeGo(wg, cancel, logger, "writer", func() {
			writer.Run(ctx, writerCh)
		})
	}

	engines := make(map[string]*engine.SnapshotEngine, len(cfg.Symbols))
	symbols := make([]string, 0, len(cfg.Symbols))
	for _, sym := range cfg.Symbols {
		canonical := sym.Canonical
		symbols = append(symbols, canonical)
		symLogger := logger.With("symbol", canonical)

		rawCh := make(chan domain.RawEvent, 10000)
		normalizedCh := make(chan domain.RawEvent, 10000)
		engineCh := make(chan domain.RawEvent, 10000)

		sourceNames := make([]string, 0, len(sym.Sources))
		for _, src := range sym.Sources {
			sourceNames = append(sourceNames, src.Name)
		}

		for _, src := range sym.Sources {
			if !src.Enabled {
				symLogger.Info("source disabled, skipping", "source", src.Name)
				continue
			}
			s := src
			safeGo(wg, cancel, logger, "adapter-"+canonical+"-"+s.Name, func() {
				startAdapter(ctx, s, canonical, rawCh, srv, symLogger)
			})
		}

		norm := normalizer.New(rawCh, normalizedCh, canonical, sourceNames, symLogger)
		safeGo(wg, cancel, logger, "normalizer-"+canonical, func() {
			norm.Run(ctx)
		})

		wg.Add(1)
		go func(symLogger *slog.Logger) {
			defer wg.Done()
			defer close(engineCh)
			var writerDrops, engineDrops int64
			for {
				select {
				case <-ctx.Done():
					if writerDrops > 0 || engineDrops > 0 {
						symLogger.Warn("fan-out final drop counts",
							"writer_drops", writerDrops,
							"engine_drops", engineDrops,
						)
					}
					return
				case evt, ok := <-normalizedCh:
					if !ok {
						return
					}
					if ingestDB != nil {
						select {
						case writerCh <- evt:
						default:
							writerDrops++
							metrics.IncChannelDrop("writer")
							if writerDrops%1000 == 1 {
								symLogger.Warn("writer channel full, dropping event",
									"source", evt.Source,
									"total_drops", writerDrops,
								)
							}
						}
					}
					select {
					case engineCh <- evt:
					default:
						engineDrops++
						metrics.IncChannelDrop("engine")
						if engineDrops%1000 == 1 {
							symLogger.Warn("engine channel full, dropping event",
								"source", evt.Source,
								"total_drops", engineDrops,
							)
						}
					}
				}
			}
		}(symLogger)

		eng := engine.NewSnapshotEngine(
			cfg.Pricing,
			canonical,
			cfg.Health.CanonicalStaleAfter(),
			ingestDB,
			engineCh,
			symLogger,
		)
		engines[canonical] = eng
		safeGo(wg, cancel, logger, "snapshot-engine-"+canonical, func() {
			eng.Run(ctx)
		})
	}

	proxy.Attach(engine.NewMultiEngine(engines, symbols))

	if ingestDB != nil {
		pruner := storage.NewPruner(ingestDB, storage.PrunerConfig{
			RawRetentionDays:       cfg.Storage.RawRetentionDays,
			SnapshotsRetentionDays: cfg.Storage.SnapshotsRetentionDays,
			CanonicalRetentionDays: cfg.Storage.CanonicalRetentionDays,
		}, logger)
		safeGo(wg, cancel, logger, "pruner", func() {
			pruner.Run(ctx)
		})

		safeGo(wg, cancel, logger, "feed-health", func() {
			updateFeedHealth(ctx, cfg, ingestDB, logger)
		})
	}

	logger.Info("all components running",
		"http", cfg.Server.HTTPAddr,
		"ws", cfg.Server.WSPath,
		"symbols", symbols,
	)
}

func startAdapter(ctx context.Context, src config.SourceConfig, symbol string, outCh chan<- domain.RawEvent, srv *api.Server, logger *slog.Logger) {
	stateCallback := func(name, oldState, newState string) {
		if srv != nil {
			srv.BroadcastSourceStatus(symbol, name, newState, false)
		}
	}

	switch src.Name {
	case "binance":
		a := adapter.NewBinanceAdapter(
			src.WSURL,
			src.NativeSymbol,
			symbol,
			src.PingInterval(),
			src.MaxConnLifetime(),
			outCh,
			logger,
		)
		a.SetOnStateChange(stateCallback)
		a.Run(ctx)

	case "coinbase":
		a := adapter.NewCoinbaseAdapter(
			src.WSURL,
			src.NativeSymbol,
			symbol,
			src.PingInterval(),
			outCh,
			logger,
		)
		a.SetOnStateChange(stateCallback)
		a.Run(ctx)

	case "kraken":
		a := adapter.NewKrakenAdapter(
			src.WSURL,
			src.NativeSymbol,
			symbol,
			src.UseTickerFallback,
			src.PingInterval(),
			outCh,
			logger,
		)
		a.SetOnStateChange(stateCallback)
		a.Run(ctx)

	case "okx":
		a := adapter.NewOKXAdapter(
			src.WSURL,
			src.NativeSymbol,
			symbol,
			src.PingInterval(),
			outCh,
			logger,
		)
		a.SetOnStateChange(stateCallback)
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
