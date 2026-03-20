package storage

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/justar9/btc-price-tick/internal/domain"
)

// Writer batches raw events and writes them to PostgreSQL.
type Writer struct {
	db       *DB
	logger   *slog.Logger
	maxRows  int
	maxDelay time.Duration

	mu        sync.Mutex
	batch     []domain.RawEvent
	lastFlush time.Time
}

func NewWriter(db *DB, maxRows int, maxDelay time.Duration, logger *slog.Logger) *Writer {
	return &Writer{
		db:        db,
		logger:    logger.With("component", "writer"),
		maxRows:   maxRows,
		maxDelay:  maxDelay,
		batch:     make([]domain.RawEvent, 0, maxRows),
		lastFlush: time.Now(),
	}
}

// Run consumes events from inCh and writes them in batches.
func (w *Writer) Run(ctx context.Context, inCh <-chan domain.RawEvent) {
	flushTicker := time.NewTicker(w.maxDelay)
	defer flushTicker.Stop()

	w.logger.Info("writer started", "max_rows", w.maxRows, "max_delay", w.maxDelay)

	for {
		select {
		case <-ctx.Done():
			w.flush(context.Background()) // final flush
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
	w.batch = make([]domain.RawEvent, 0, w.maxRows)
	w.lastFlush = time.Now()
	w.mu.Unlock()

	start := time.Now()

	rows := make([][]interface{}, 0, len(batch))
	for _, e := range batch {
		rows = append(rows, []interface{}{
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
			e.ExchangeTS.Format("2006-01-02"),
		})
	}

	cols := []string{
		"event_id", "source", "symbol_native", "symbol_canonical",
		"event_type", "exchange_ts", "recv_ts", "price", "size",
		"side", "trade_id", "sequence", "bid", "ask",
		"raw_payload", "ingest_partition_date",
	}

	// Use temp table + COPY + INSERT ... ON CONFLICT to get bulk speed
	// with duplicate handling. CopyFrom alone cannot handle ON CONFLICT.
	tx, err := w.db.Pool.Begin(ctx)
	if err != nil {
		w.logger.Error("begin tx failed, falling back to individual inserts",
			"error", err,
			"batch_size", len(batch),
		)
		w.insertIndividually(ctx, batch)
		return
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `CREATE TEMP TABLE _stage (LIKE raw_ticks INCLUDING DEFAULTS) ON COMMIT DROP`)
	if err != nil {
		w.logger.Error("create staging table failed, falling back to individual inserts",
			"error", err,
			"batch_size", len(batch),
		)
		w.insertIndividually(ctx, batch)
		return
	}

	_, err = tx.CopyFrom(ctx, pgx.Identifier{"_stage"}, cols, pgx.CopyFromRows(rows))
	if err != nil {
		w.logger.Error("copy to staging table failed, falling back to individual inserts",
			"error", err,
			"batch_size", len(batch),
		)
		w.insertIndividually(ctx, batch)
		return
	}

	tag, err := tx.Exec(ctx, `INSERT INTO raw_ticks SELECT * FROM _stage ON CONFLICT DO NOTHING`)
	if err != nil {
		w.logger.Error("merge from staging failed, falling back to individual inserts",
			"error", err,
			"batch_size", len(batch),
		)
		w.insertIndividually(ctx, batch)
		return
	}

	if err = tx.Commit(ctx); err != nil {
		w.logger.Error("commit failed, falling back to individual inserts",
			"error", err,
			"batch_size", len(batch),
		)
		w.insertIndividually(ctx, batch)
		return
	}

	elapsed := time.Since(start)
	w.logger.Debug("batch flushed",
		"rows", len(batch),
		"inserted", tag.RowsAffected(),
		"elapsed", elapsed,
	)
}

func (w *Writer) insertIndividually(ctx context.Context, batch []domain.RawEvent) {
	const q = `INSERT INTO raw_ticks (
		event_id, source, symbol_native, symbol_canonical,
		event_type, exchange_ts, recv_ts, price, size,
		side, trade_id, sequence, bid, ask,
		raw_payload, ingest_partition_date
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
	ON CONFLICT DO NOTHING`

	inserted := 0
	for _, e := range batch {
		_, err := w.db.Pool.Exec(ctx, q,
			e.EventID, e.Source, e.SymbolNative, e.SymbolCanonical,
			e.EventType, e.ExchangeTS, e.RecvTS, e.Price, e.Size,
			e.Side, e.TradeID, e.Sequence, e.Bid, e.Ask,
			e.RawPayload, e.ExchangeTS.Format("2006-01-02"),
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
