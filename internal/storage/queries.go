package storage

import (
	"context"
	"strconv"
	"time"

	"github.com/justar9/btc-price-tick/internal/domain"
)

// InsertSnapshot writes a finalized 1-second snapshot.
func (db *DB) InsertSnapshot(ctx context.Context, s domain.Snapshot1s) error {
	const q = `INSERT INTO snapshots_1s (
		ts_second, canonical_symbol, canonical_price, basis,
		is_stale, is_degraded, quality_score, source_count,
		sources_used, source_details_json, last_event_exchange_ts, finalized_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	ON CONFLICT (ts_second) DO NOTHING`

	_, err := db.Pool.Exec(ctx, q,
		s.TSSecond, s.CanonicalSymbol, s.CanonicalPrice, s.Basis,
		s.IsStale, s.IsDegraded, s.QualityScore, s.SourceCount,
		s.SourcesUsed, s.SourceDetailsJSON, s.LastEventExchangeTS, s.FinalizedAt,
	)
	return err
}

// InsertCanonicalTick writes a canonical price change event.
func (db *DB) InsertCanonicalTick(ctx context.Context, t domain.CanonicalTick) error {
	const q = `INSERT INTO canonical_ticks (
		tick_id, ts_event, canonical_symbol, canonical_price, basis,
		is_stale, is_degraded, quality_score, source_count,
		sources_used, source_details_json
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	ON CONFLICT DO NOTHING`

	_, err := db.Pool.Exec(ctx, q,
		t.TickID, t.TSEvent, t.CanonicalSymbol, t.CanonicalPrice, t.Basis,
		t.IsStale, t.IsDegraded, t.QualityScore, t.SourceCount,
		t.SourcesUsed, t.SourceDetailsJSON,
	)
	return err
}

// UpsertFeedHealth updates per-source feed health state.
func (db *DB) UpsertFeedHealth(ctx context.Context, h domain.FeedHealth) error {
	const q = `INSERT INTO feed_health (
		source, conn_state, last_message_ts, last_trade_ts,
		last_heartbeat_ts, reconnect_count_1h, consecutive_errors,
		median_lag_ms, stale, details_json, updated_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'{}',$10)
	ON CONFLICT (source) DO UPDATE SET
		conn_state = EXCLUDED.conn_state,
		last_message_ts = EXCLUDED.last_message_ts,
		last_trade_ts = EXCLUDED.last_trade_ts,
		last_heartbeat_ts = EXCLUDED.last_heartbeat_ts,
		reconnect_count_1h = EXCLUDED.reconnect_count_1h,
		consecutive_errors = EXCLUDED.consecutive_errors,
		median_lag_ms = EXCLUDED.median_lag_ms,
		stale = EXCLUDED.stale,
		updated_at = EXCLUDED.updated_at`

	_, err := db.Pool.Exec(ctx, q,
		h.Source, h.ConnState, h.LastMessageTS, h.LastTradeTS,
		h.LastHeartbeatTS, h.ReconnectCount1h, h.ConsecutiveErrors,
		h.MedianLagMs, h.Stale, h.UpdatedAt,
	)
	return err
}

// QuerySnapshots returns 1-second snapshots in a time range.
func (db *DB) QuerySnapshots(ctx context.Context, start, end time.Time) ([]domain.Snapshot1s, error) {
	const q = `SELECT ts_second, canonical_symbol, canonical_price, basis,
		is_stale, is_degraded, quality_score, source_count,
		sources_used, source_details_json, last_event_exchange_ts, finalized_at
	FROM snapshots_1s
	WHERE ts_second >= $1 AND ts_second <= $2
	ORDER BY ts_second ASC`

	rows, err := db.Pool.Query(ctx, q, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []domain.Snapshot1s
	for rows.Next() {
		var s domain.Snapshot1s
		err := rows.Scan(
			&s.TSSecond, &s.CanonicalSymbol, &s.CanonicalPrice, &s.Basis,
			&s.IsStale, &s.IsDegraded, &s.QualityScore, &s.SourceCount,
			&s.SourcesUsed, &s.SourceDetailsJSON, &s.LastEventExchangeTS, &s.FinalizedAt,
		)
		if err != nil {
			return nil, err
		}
		results = append(results, s)
	}
	return results, rows.Err()
}

// QueryLatestSnapshot returns the most recent finalized snapshot.
func (db *DB) QueryLatestSnapshot(ctx context.Context) (*domain.Snapshot1s, error) {
	const q = `SELECT ts_second, canonical_symbol, canonical_price, basis,
		is_stale, is_degraded, quality_score, source_count,
		sources_used, source_details_json, last_event_exchange_ts, finalized_at
	FROM snapshots_1s
	ORDER BY ts_second DESC
	LIMIT 1`

	var s domain.Snapshot1s
	err := db.Pool.QueryRow(ctx, q).Scan(
		&s.TSSecond, &s.CanonicalSymbol, &s.CanonicalPrice, &s.Basis,
		&s.IsStale, &s.IsDegraded, &s.QualityScore, &s.SourceCount,
		&s.SourcesUsed, &s.SourceDetailsJSON, &s.LastEventExchangeTS, &s.FinalizedAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// QueryCanonicalTicks returns recent canonical ticks.
func (db *DB) QueryCanonicalTicks(ctx context.Context, limit int) ([]domain.CanonicalTick, error) {
	if limit <= 0 {
		limit = 100
	}
	const q = `SELECT tick_id, ts_event, canonical_symbol, canonical_price, basis,
		is_stale, is_degraded, quality_score, source_count,
		sources_used, source_details_json
	FROM canonical_ticks
	ORDER BY ts_event DESC
	LIMIT $1`

	rows, err := db.Pool.Query(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []domain.CanonicalTick
	for rows.Next() {
		var t domain.CanonicalTick
		err := rows.Scan(
			&t.TickID, &t.TSEvent, &t.CanonicalSymbol, &t.CanonicalPrice, &t.Basis,
			&t.IsStale, &t.IsDegraded, &t.QualityScore, &t.SourceCount,
			&t.SourcesUsed, &t.SourceDetailsJSON,
		)
		if err != nil {
			return nil, err
		}
		results = append(results, t)
	}
	return results, rows.Err()
}

// QueryRawTicks returns raw events for debugging.
func (db *DB) QueryRawTicks(ctx context.Context, source string, start, end time.Time, limit int) ([]domain.RawEvent, error) {
	if limit <= 0 {
		limit = 100
	}

	q := `SELECT event_id, source, symbol_native, symbol_canonical,
		event_type, exchange_ts, recv_ts, price, size,
		side, trade_id, sequence, bid, ask, raw_payload
	FROM raw_ticks WHERE 1=1`
	args := []interface{}{}
	argN := 1

	if source != "" {
		q += ` AND source = $` + strconv.Itoa(argN)
		args = append(args, source)
		argN++
	}
	if !start.IsZero() {
		q += ` AND exchange_ts >= $` + strconv.Itoa(argN)
		args = append(args, start)
		argN++
	}
	if !end.IsZero() {
		q += ` AND exchange_ts <= $` + strconv.Itoa(argN)
		args = append(args, end)
		argN++
	}
	q += ` ORDER BY exchange_ts DESC LIMIT $` + strconv.Itoa(argN)
	args = append(args, limit)

	rows, err := db.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []domain.RawEvent
	for rows.Next() {
		var e domain.RawEvent
		err := rows.Scan(
			&e.EventID, &e.Source, &e.SymbolNative, &e.SymbolCanonical,
			&e.EventType, &e.ExchangeTS, &e.RecvTS, &e.Price, &e.Size,
			&e.Side, &e.TradeID, &e.Sequence, &e.Bid, &e.Ask, &e.RawPayload,
		)
		if err != nil {
			return nil, err
		}
		results = append(results, e)
	}
	return results, rows.Err()
}

// QuerySnapshotAt returns the snapshot at a specific second (for settlement).
func (db *DB) QuerySnapshotAt(ctx context.Context, ts time.Time) (*domain.Snapshot1s, error) {
	const q = `SELECT ts_second, canonical_symbol, canonical_price, basis,
		is_stale, is_degraded, quality_score, source_count,
		sources_used, source_details_json, last_event_exchange_ts, finalized_at
	FROM snapshots_1s
	WHERE ts_second = $1`

	var s domain.Snapshot1s
	err := db.Pool.QueryRow(ctx, q, ts).Scan(
		&s.TSSecond, &s.CanonicalSymbol, &s.CanonicalPrice, &s.Basis,
		&s.IsStale, &s.IsDegraded, &s.QualityScore, &s.SourceCount,
		&s.SourcesUsed, &s.SourceDetailsJSON, &s.LastEventExchangeTS, &s.FinalizedAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// QueryFeedHealth returns all feed health records.
func (db *DB) QueryFeedHealth(ctx context.Context) ([]domain.FeedHealth, error) {
	const q = `SELECT source, conn_state, last_message_ts, last_trade_ts,
		last_heartbeat_ts, reconnect_count_1h, consecutive_errors,
		median_lag_ms, stale, updated_at
	FROM feed_health
	ORDER BY source`

	rows, err := db.Pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []domain.FeedHealth
	for rows.Next() {
		var h domain.FeedHealth
		err := rows.Scan(
			&h.Source, &h.ConnState, &h.LastMessageTS, &h.LastTradeTS,
			&h.LastHeartbeatTS, &h.ReconnectCount1h, &h.ConsecutiveErrors,
			&h.MedianLagMs, &h.Stale, &h.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		results = append(results, h)
	}
	return results, rows.Err()
}
