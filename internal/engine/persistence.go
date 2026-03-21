package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/justar9/btick/internal/domain"
)

const (
	canonicalTickBatchMaxRows  = 128
	canonicalTickBatchMaxDelay = 100 * time.Millisecond
	snapshotBatchMaxRows       = 10
	snapshotBatchMaxDelay      = 10 * time.Second
)

type batchWriteOps[T any] struct {
	batchInsert       func(context.Context, []T) error
	singleInsert      func(context.Context, T) error
	itemAttrs         func(T) []any
	batchErrorMessage string
	itemErrorMessage  string
}

func runBatchedDBWriter[T any](
	ctx context.Context,
	inCh <-chan T,
	maxRows int,
	maxDelay time.Duration,
	logger *slog.Logger,
	ops batchWriteOps[T],
) {
	flushTicker := time.NewTicker(maxDelay)
	defer flushTicker.Stop()

	batch := make([]T, 0, maxRows)
	flush := func(flushCtx context.Context) {
		if len(batch) == 0 {
			return
		}

		items := append([]T(nil), batch...)
		batch = batch[:0]

		if err := ops.batchInsert(flushCtx, items); err == nil {
			return
		} else {
			logger.Error(ops.batchErrorMessage,
				"error", err,
				"batch_size", len(items),
			)
		}

		for _, item := range items {
			if err := ops.singleInsert(flushCtx, item); err != nil {
				logAttrs := append([]any{"error", err}, ops.itemAttrs(item)...)
				logger.Error(ops.itemErrorMessage, logAttrs...)
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case item := <-inCh:
					batch = append(batch, item)
					if len(batch) >= maxRows {
						writeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
						flush(writeCtx)
						cancel()
					}
				default:
					if len(batch) > 0 {
						writeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
						flush(writeCtx)
						cancel()
					}
					return
				}
			}
		case item := <-inCh:
			batch = append(batch, item)
			if len(batch) >= maxRows {
				writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				flush(writeCtx)
				cancel()
			}
		case <-flushTicker.C:
			if len(batch) > 0 {
				writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				flush(writeCtx)
				cancel()
			}
		}
	}
}

// runTickWriter drains ticksToWrite and persists canonical ticks to DB.
func (e *SnapshotEngine) runTickWriter(ctx context.Context) {
	runBatchedDBWriter(ctx, e.ticksToWrite, canonicalTickBatchMaxRows, canonicalTickBatchMaxDelay, e.logger,
		batchWriteOps[domain.CanonicalTick]{
			batchInsert:       e.db.InsertCanonicalTicks,
			singleInsert:      e.db.InsertCanonicalTick,
			itemAttrs:         func(tick domain.CanonicalTick) []any { return []any{"tick_id", tick.TickID} },
			batchErrorMessage: "insert canonical tick batch failed, falling back to individual inserts",
			itemErrorMessage:  "insert canonical tick failed",
		},
	)
}

// runSnapshotWriter drains snapshotsToWrite and persists snapshots in batches.
func (e *SnapshotEngine) runSnapshotWriter(ctx context.Context) {
	runBatchedDBWriter(ctx, e.snapshotsToWrite, snapshotBatchMaxRows, snapshotBatchMaxDelay, e.logger,
		batchWriteOps[domain.Snapshot1s]{
			batchInsert:       e.db.InsertSnapshots,
			singleInsert:      e.db.InsertSnapshot,
			itemAttrs:         func(snapshot domain.Snapshot1s) []any { return []any{"ts", snapshot.TSSecond} },
			batchErrorMessage: "insert snapshot batch failed, falling back to individual inserts",
			itemErrorMessage:  "insert snapshot failed",
		},
	)
}
