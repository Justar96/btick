package storage

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// PrunerConfig holds retention settings for the pruner.
type PrunerConfig struct {
	RawRetentionDays       int
	SnapshotsRetentionDays int
	CanonicalRetentionDays int
	Interval               time.Duration
	BatchSize              int
}

// Pruner periodically deletes expired rows from raw_ticks, canonical_ticks,
// and snapshots_1s to prevent unbounded table growth.
type Pruner struct {
	db     *DB
	cfg    PrunerConfig
	logger *slog.Logger
}

// NewPruner creates a new retention pruner.
func NewPruner(db *DB, cfg PrunerConfig, logger *slog.Logger) *Pruner {
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Minute
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 10000
	}
	return &Pruner{
		db:     db,
		cfg:    cfg,
		logger: logger.With("component", "pruner"),
	}
}

// Run starts the pruner loop. It blocks until ctx is cancelled.
func (p *Pruner) Run(ctx context.Context) {
	p.logger.Info("pruner started",
		"raw_retention_days", p.cfg.RawRetentionDays,
		"snapshots_retention_days", p.cfg.SnapshotsRetentionDays,
		"canonical_retention_days", p.cfg.CanonicalRetentionDays,
		"interval", p.cfg.Interval,
		"batch_size", p.cfg.BatchSize,
	)

	// Run once at startup after a short delay
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
		p.pruneAll(ctx)
	}

	ticker := time.NewTicker(p.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("pruner stopped")
			return
		case <-ticker.C:
			p.pruneAll(ctx)
		}
	}
}

func (p *Pruner) pruneAll(ctx context.Context) {
	start := time.Now()

	rawDeleted := p.pruneTable(ctx, "raw_ticks", "exchange_ts", p.cfg.RawRetentionDays)
	canonicalDeleted := p.pruneTable(ctx, "canonical_ticks", "ts_event", p.cfg.CanonicalRetentionDays)
	snapshotsDeleted := p.pruneTable(ctx, "snapshots_1s", "ts_second", p.cfg.SnapshotsRetentionDays)

	elapsed := time.Since(start)
	total := rawDeleted + canonicalDeleted + snapshotsDeleted
	if total > 0 {
		p.logger.Info("pruning cycle complete",
			"raw_deleted", rawDeleted,
			"canonical_deleted", canonicalDeleted,
			"snapshots_deleted", snapshotsDeleted,
			"elapsed", elapsed,
		)
	} else {
		p.logger.Debug("pruning cycle complete, nothing to prune", "elapsed", elapsed)
	}
}

// pruneTable deletes rows older than retentionDays from the given table
// using the specified timestamp column. Deletes are batched to avoid
// long-running transactions and excessive WAL generation.
func (p *Pruner) pruneTable(ctx context.Context, table, tsColumn string, retentionDays int) int64 {
	if retentionDays <= 0 {
		return 0
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	var totalDeleted int64

	for {
		if ctx.Err() != nil {
			return totalDeleted
		}

		q := fmt.Sprintf(
			`DELETE FROM %s WHERE ctid IN (SELECT ctid FROM %s WHERE %s < $1 LIMIT $2)`,
			table, table, tsColumn,
		)

		tag, err := p.db.Pool.Exec(ctx, q, cutoff, p.cfg.BatchSize)
		if err != nil {
			p.logger.Error("prune batch failed",
				"table", table,
				"error", err,
			)
			return totalDeleted
		}

		deleted := tag.RowsAffected()
		totalDeleted += deleted

		if deleted < int64(p.cfg.BatchSize) {
			break
		}

		// Brief pause between batches to avoid starving other queries
		select {
		case <-ctx.Done():
			return totalDeleted
		case <-time.After(100 * time.Millisecond):
		}
	}

	return totalDeleted
}
