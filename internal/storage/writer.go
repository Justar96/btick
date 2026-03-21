package storage

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/justar9/btick/internal/domain"
	"github.com/justar9/btick/internal/metrics"
)

const rawTickStagingTable = "_raw_ticks_staging"

const insertRawTickQuery = `INSERT INTO raw_ticks (
	event_id, source, symbol_native, symbol_canonical,
	event_type, exchange_ts, recv_ts, price, size,
	side, trade_id, sequence, bid, ask,
	raw_payload
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
ON CONFLICT DO NOTHING`

const createRawTickStagingTableQuery = `CREATE TEMP TABLE _raw_ticks_staging
(LIKE raw_ticks INCLUDING DEFAULTS)
ON COMMIT DROP`

const mergeRawTickStagingQuery = `INSERT INTO raw_ticks (
	event_id, source, symbol_native, symbol_canonical,
	event_type, exchange_ts, recv_ts, price, size,
	side, trade_id, sequence, bid, ask,
	raw_payload
)
SELECT
	event_id, source, symbol_native, symbol_canonical,
	event_type, exchange_ts, recv_ts, price, size,
	side, trade_id, sequence, bid, ask,
	raw_payload
FROM _raw_ticks_staging
ON CONFLICT DO NOTHING`

var rawTickCopyColumns = []string{
	"event_id",
	"source",
	"symbol_native",
	"symbol_canonical",
	"event_type",
	"exchange_ts",
	"recv_ts",
	"price",
	"size",
	"side",
	"trade_id",
	"sequence",
	"bid",
	"ask",
	"raw_payload",
}

// Writer batches raw events and writes them to PostgreSQL.
type Writer struct {
	db        *DB
	logger    *slog.Logger
	maxRows   int
	maxDelay  time.Duration
	batchPool sync.Pool

	mu        sync.Mutex
	batch     []domain.RawEvent
	lastFlush time.Time
}

func NewWriter(db *DB, maxRows int, maxDelay time.Duration, logger *slog.Logger) *Writer {
	writer := &Writer{
		db:        db,
		logger:    logger.With("component", "writer"),
		maxRows:   maxRows,
		maxDelay:  maxDelay,
		lastFlush: time.Now(),
	}
	writer.batchPool.New = func() any {
		batch := make([]domain.RawEvent, 0, maxRows)
		return &batch
	}
	writer.batch = writer.getBatch()
	return writer
}

func (w *Writer) getBatch() []domain.RawEvent {
	return (*w.batchPool.Get().(*[]domain.RawEvent))[:0]
}

func (w *Writer) putBatch(batch []domain.RawEvent) {
	clear(batch)
	batch = batch[:0]
	w.batchPool.Put(&batch)
}

// Run consumes events from inCh and writes them in batches.
func (w *Writer) Run(ctx context.Context, inCh <-chan domain.RawEvent) {
	flushTicker := time.NewTicker(w.maxDelay)
	defer flushTicker.Stop()

	w.logger.Info("writer started", "max_rows", w.maxRows, "max_delay", w.maxDelay)

	for {
		select {
		case <-ctx.Done():
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			w.flush(shutdownCtx)
			shutdownCancel()
			w.logger.Info("writer stopped")
			return

		case evt, ok := <-inCh:
			if !ok {
				w.flush(context.Background())
				return
			}
			w.mu.Lock()
			w.batch = append(w.batch, evt)
			needFlush := len(w.batch) >= w.maxRows
			w.mu.Unlock()

			if needFlush {
				w.flush(ctx)
			}

		case <-flushTicker.C:
			w.flush(ctx)
		}
	}
}

func (w *Writer) flush(ctx context.Context) {
	w.mu.Lock()
	if len(w.batch) == 0 {
		w.mu.Unlock()
		return
	}
	batch := w.batch
	w.batch = w.getBatch()
	w.lastFlush = time.Now()
	w.mu.Unlock()
	defer w.putBatch(batch)

	start := time.Now()
	defer func() {
		metrics.ObserveWriterFlush(len(batch), time.Since(start))
	}()

	copied, inserted, err := w.copyThroughStaging(ctx, batch)
	if err != nil {
		w.logger.Error("copy/merge insert failed, falling back to individual inserts",
			"error", err,
			"batch_size", len(batch),
		)
		w.insertIndividually(ctx, batch)
		return
	}

	elapsed := time.Since(start)
	w.logger.Debug("batch flushed",
		"rows", len(batch),
		"copied", copied,
		"inserted", inserted,
		"elapsed", elapsed,
	)
}

func (w *Writer) copyThroughStaging(ctx context.Context, batch []domain.RawEvent) (int64, int64, error) {
	conn, err := w.db.Pool.Acquire(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	if _, err := tx.Exec(ctx, createRawTickStagingTableQuery); err != nil {
		return 0, 0, err
	}

	copied, err := tx.CopyFrom(ctx,
		pgx.Identifier{rawTickStagingTable},
		rawTickCopyColumns,
		rawTickCopySource(batch),
	)
	if err != nil {
		return 0, 0, err
	}

	tag, err := tx.Exec(ctx, mergeRawTickStagingQuery)
	if err != nil {
		return copied, 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return copied, tag.RowsAffected(), err
	}

	return copied, tag.RowsAffected(), nil
}

func rawTickCopySource(batch []domain.RawEvent) pgx.CopyFromSource {
	return pgx.CopyFromSlice(len(batch), func(i int) ([]any, error) {
		return rawTickCopyRow(batch[i]), nil
	})
}

func rawTickCopyRow(e domain.RawEvent) []any {
	return []any{
		e.EventID,
		e.Source,
		e.SymbolNative,
		e.SymbolCanonical,
		e.EventType,
		e.ExchangeTS,
		e.RecvTS,
		e.Price,
		e.Size,
		e.Side,
		e.TradeID,
		e.Sequence,
		e.Bid,
		e.Ask,
		e.RawPayload,
	}
}

func (w *Writer) insertIndividually(ctx context.Context, batch []domain.RawEvent) {
	inserted := 0
	for _, e := range batch {
		_, err := w.db.Pool.Exec(ctx, insertRawTickQuery,
			e.EventID, e.Source, e.SymbolNative, e.SymbolCanonical,
			e.EventType, e.ExchangeTS, e.RecvTS, e.Price, e.Size,
			e.Side, e.TradeID, e.Sequence, e.Bid, e.Ask,
			e.RawPayload,
		)
		if err != nil {
			w.logger.Error("individual insert failed",
				"error", err,
				"event_id", e.EventID,
				"source", e.Source,
			)
			continue
		}
		inserted++
	}
	w.logger.Info("individual inserts completed",
		"inserted", inserted,
		"total", len(batch),
	)
}
