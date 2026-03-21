package storage

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/justar9/btick/internal/domain"
)

const insertRawTickQuery = `INSERT INTO raw_ticks (
	event_id, source, symbol_native, symbol_canonical,
	event_type, exchange_ts, recv_ts, price, size,
	side, trade_id, sequence, bid, ask,
	raw_payload
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
ON CONFLICT DO NOTHING`

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

	var pgxBatch pgx.Batch
	for _, e := range batch {
		pgxBatch.Queue(insertRawTickQuery,
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
		)
	}

	results := w.db.Pool.SendBatch(ctx, &pgxBatch)
	inserted := int64(0)
	for range batch {
		tag, err := results.Exec()
		if err != nil {
			_ = results.Close()
			w.logger.Error("batched insert failed, falling back to individual inserts",
				"error", err,
				"batch_size", len(batch),
			)
			w.insertIndividually(ctx, batch)
			return
		}
		inserted += tag.RowsAffected()
	}

	if err := results.Close(); err != nil {
		w.logger.Error("batch close failed, falling back to individual inserts",
			"error", err,
			"batch_size", len(batch),
		)
		w.insertIndividually(ctx, batch)
		return
	}

	elapsed := time.Since(start)
	w.logger.Debug("batch flushed",
		"rows", len(batch),
		"inserted", inserted,
		"elapsed", elapsed,
	)
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
