package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/justar9/btc-price-tick/internal/adapter"
	"github.com/justar9/btc-price-tick/internal/api"
	"github.com/justar9/btc-price-tick/internal/config"
	"github.com/justar9/btc-price-tick/internal/domain"
	"github.com/justar9/btc-price-tick/internal/engine"
	"github.com/justar9/btc-price-tick/internal/normalizer"
	"github.com/justar9/btc-price-tick/internal/storage"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	// Structured JSON logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	logger.Info("btc-price-tick starting", "config", *configPath)

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
	var db *storage.DB
	if cfg.Database.DSN != "" {
		db, err = storage.New(ctx, cfg.Database, logger)
		if err != nil {
			logger.Warn("database connection failed, running without persistence", "error", err)
		} else {
			defer db.Close()
			logger.Info("database connected")
		}
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

		wg.Add(1)
		go func(s config.SourceConfig) {
			defer wg.Done()
			startAdapter(ctx, s, rawCh, logger)
		}(src)
	}

	// Normalizer
	norm := normalizer.New(rawCh, normalizedCh, logger)
	wg.Add(1)
	go func() {
		defer wg.Done()
		norm.Run(ctx)
	}()

	// Fan-out: normalizedCh -> writerCh + engineCh
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(writerCh)
		defer close(engineCh)
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-normalizedCh:
				if !ok {
					return
				}
				// Non-blocking send to both
				select {
				case writerCh <- evt:
				default:
				}
				select {
				case engineCh <- evt:
				default:
				}
			}
		}
	}()

	// Raw event writer
	if db != nil {
		writer := storage.NewWriter(db,
			cfg.Storage.BatchInsertMaxRows,
			cfg.Storage.BatchInsertMaxDelay(),
			logger,
		)
		wg.Add(1)
		go func() {
			defer wg.Done()
			writer.Run(ctx, writerCh)
		}()
	}

	// Snapshot engine
	eng := engine.NewSnapshotEngine(
		cfg.Pricing,
		cfg.CanonicalSymbol,
		cfg.Health.CanonicalStaleAfter(),
		db,
		engineCh,
		logger,
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		eng.Run(ctx)
	}()

	// Note: canonical ticks are written to DB by the snapshot engine directly
	// and broadcast to WebSocket clients by the API server's broadcast loop.
	// No separate tick writer goroutine is needed.

	// Feed health updater
	if db != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			updateFeedHealth(ctx, cfg, db, logger)
		}()
	}

	// API server
	srv := api.NewServer(cfg.Server.HTTPAddr, cfg.Server.WSPath, db, eng, logger)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := srv.Run(ctx); err != nil {
			logger.Error("API server error", "error", err)
		}
	}()

	logger.Info("all components running",
		"http", cfg.Server.HTTPAddr,
		"ws", cfg.Server.WSPath,
	)

	// Wait for shutdown
	wg.Wait()
	logger.Info("btc-price-tick stopped")
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
