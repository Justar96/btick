package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/justar9/btick/internal/domain"
	"github.com/shopspring/decimal"
)

func (s *Server) handleLatest(w http.ResponseWriter, r *http.Request) {
	latest := s.engine.LatestState()
	if latest == nil {
		http.Error(w, `{"error":"no data yet"}`, http.StatusServiceUnavailable)
		return
	}

	resp := map[string]interface{}{
		"symbol":        latest.Symbol,
		"ts":            latest.TS.Format(time.RFC3339Nano),
		"price":         latest.Price.String(),
		"basis":         latest.Basis,
		"is_stale":      latest.IsStale,
		"is_degraded":   latest.IsDegraded,
		"quality_score": latest.QualityScore.InexactFloat64(),
		"source_count":  latest.SourceCount,
		"sources_used":  latest.SourcesUsed,
	}

	s.writeJSON(w, resp)
}

func (s *Server) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")

	if startStr == "" {
		http.Error(w, `{"error":"start parameter required"}`, http.StatusBadRequest)
		return
	}

	start, err := time.Parse(time.RFC3339, startStr)
	if err != nil {
		http.Error(w, `{"error":"invalid start time format, use RFC3339"}`, http.StatusBadRequest)
		return
	}

	end := time.Now().UTC()
	if endStr != "" {
		end, err = time.Parse(time.RFC3339, endStr)
		if err != nil {
			http.Error(w, `{"error":"invalid end time format, use RFC3339"}`, http.StatusBadRequest)
			return
		}
	}

	if s.db == nil {
		http.Error(w, `{"error":"database not available"}`, http.StatusServiceUnavailable)
		return
	}

	snapshots, err := s.db.QuerySnapshots(r.Context(), start, end)
	if err != nil {
		s.logger.Error("query snapshots failed", "error", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	var resp []map[string]interface{}
	for _, snap := range snapshots {
		resp = append(resp, map[string]interface{}{
			"ts_second":     snap.TSSecond.Format(time.RFC3339),
			"symbol":        snap.CanonicalSymbol,
			"price":         snap.CanonicalPrice.String(),
			"basis":         snap.Basis,
			"is_stale":      snap.IsStale,
			"is_degraded":   snap.IsDegraded,
			"quality_score": snap.QualityScore.InexactFloat64(),
			"source_count":  snap.SourceCount,
			"sources_used":  snap.SourcesUsed,
			"finalized_at":  snap.FinalizedAt.Format(time.RFC3339Nano),
		})
	}

	if resp == nil {
		resp = []map[string]interface{}{}
	}
	s.writeJSON(w, resp)
}

func (s *Server) handleTicks(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	if s.db == nil {
		http.Error(w, `{"error":"database not available"}`, http.StatusServiceUnavailable)
		return
	}

	ticks, err := s.db.QueryCanonicalTicks(r.Context(), limit)
	if err != nil {
		s.logger.Error("query ticks failed", "error", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	var resp []map[string]interface{}
	for _, t := range ticks {
		resp = append(resp, map[string]interface{}{
			"ts":            t.TSEvent.Format(time.RFC3339Nano),
			"symbol":        t.CanonicalSymbol,
			"price":         t.CanonicalPrice.String(),
			"basis":         t.Basis,
			"is_stale":      t.IsStale,
			"is_degraded":   t.IsDegraded,
			"quality_score": t.QualityScore.InexactFloat64(),
			"source_count":  t.SourceCount,
			"sources_used":  t.SourcesUsed,
		})
	}

	if resp == nil {
		resp = []map[string]interface{}{}
	}
	s.writeJSON(w, resp)
}

func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")
	limitStr := r.URL.Query().Get("limit")

	var start, end time.Time
	var err error

	if startStr != "" {
		start, err = time.Parse(time.RFC3339, startStr)
		if err != nil {
			http.Error(w, `{"error":"invalid start time"}`, http.StatusBadRequest)
			return
		}
	}
	if endStr != "" {
		end, err = time.Parse(time.RFC3339, endStr)
		if err != nil {
			http.Error(w, `{"error":"invalid end time"}`, http.StatusBadRequest)
			return
		}
	}

	limit := 100
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}

	if s.db == nil {
		http.Error(w, `{"error":"database not available"}`, http.StatusServiceUnavailable)
		return
	}

	events, err := s.db.QueryRawTicks(r.Context(), source, start, end, limit)
	if err != nil {
		s.logger.Error("query raw ticks failed", "error", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	var resp []map[string]interface{}
	for _, e := range events {
		resp = append(resp, map[string]interface{}{
			"event_id":    e.EventID.String(),
			"source":      e.Source,
			"event_type":  e.EventType,
			"exchange_ts": e.ExchangeTS.Format(time.RFC3339Nano),
			"recv_ts":     e.RecvTS.Format(time.RFC3339Nano),
			"price":       e.Price.String(),
			"size":        e.Size.String(),
			"side":        e.Side,
			"trade_id":    e.TradeID,
		})
	}

	if resp == nil {
		resp = []map[string]interface{}{}
	}
	s.writeJSON(w, resp)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	latest := s.engine.LatestState()

	status := "ok"
	if latest == nil {
		status = "no_data"
	} else if latest.IsStale {
		status = "stale"
	} else if latest.IsDegraded {
		status = "degraded"
	}

	resp := map[string]interface{}{
		"status":    status,
		"timestamp": s.now().UTC().Format(time.RFC3339Nano),
	}
	if latest != nil {
		resp["latest_price"] = latest.Price.String()
		resp["latest_ts"] = latest.TS.Format(time.RFC3339Nano)
		resp["source_count"] = latest.SourceCount
	}

	s.writeJSON(w, resp)
}

func (s *Server) handleSettlement(w http.ResponseWriter, r *http.Request) {
	tsStr := r.URL.Query().Get("ts")
	if tsStr == "" {
		http.Error(w, `{"error":"ts parameter required (RFC3339 format, e.g. 2026-03-19T09:05:00Z)"}`, http.StatusBadRequest)
		return
	}

	ts, err := time.Parse(time.RFC3339, tsStr)
	if err != nil {
		http.Error(w, `{"error":"invalid ts format, use RFC3339 (e.g. 2026-03-19T09:05:00Z)"}`, http.StatusBadRequest)
		return
	}

	// Truncate to second boundary
	ts = ts.UTC().Truncate(time.Second)

	// Validate it's a 5-minute boundary for prediction markets
	if ts.Second() != 0 || ts.Minute()%5 != 0 {
		http.Error(w, `{"error":"ts must be on a 5-minute boundary (e.g. 09:05:00, 09:10:00)"}`, http.StatusBadRequest)
		return
	}

	// Don't allow future timestamps
	now := s.now().UTC()
	if ts.After(now) {
		http.Error(w, `{"error":"ts cannot be in the future"}`, http.StatusBadRequest)
		return
	}

	// Require at least 5 seconds of finalization grace period
	if now.Sub(ts) < 5*time.Second {
		http.Error(w, `{"error":"settlement price not yet finalized, wait a few seconds"}`, http.StatusTooEarly)
		return
	}

	if s.db == nil {
		http.Error(w, `{"error":"database not available"}`, http.StatusServiceUnavailable)
		return
	}

	snapshot, err := s.db.QuerySnapshotAt(r.Context(), ts)
	if err != nil {
		s.logger.Error("query settlement snapshot failed", "error", err, "ts", ts)
		http.Error(w, `{"error":"settlement price not found for this timestamp"}`, http.StatusNotFound)
		return
	}

	// If snapshot is confirmed, return it directly (fast path).
	if !snapshot.IsStale && !snapshot.IsDegraded {
		s.writeJSON(w, s.buildSettlementResp(ts, snapshot, "confirmed"))
		return
	}

	// Snapshot is degraded or stale — try re-aggregation from raw trades
	// using a wider window than the real-time snapshot engine.
	reagg := s.tryReaggregate(r.Context(), ts, snapshot)
	s.writeJSON(w, reagg)
}

func (s *Server) handleFeedHealth(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		http.Error(w, `{"error":"database not available"}`, http.StatusServiceUnavailable)
		return
	}

	feeds, err := s.db.QueryFeedHealth(r.Context())
	if err != nil {
		s.logger.Error("query feed health failed", "error", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	var resp []map[string]interface{}
	for _, f := range feeds {
		entry := map[string]interface{}{
			"source":             f.Source,
			"conn_state":         f.ConnState,
			"reconnect_count_1h": f.ReconnectCount1h,
			"consecutive_errors": f.ConsecutiveErrors,
			"stale":              f.Stale,
			"updated_at":         f.UpdatedAt.Format(time.RFC3339Nano),
		}
		if !f.LastMessageTS.IsZero() {
			entry["last_message_ts"] = f.LastMessageTS.Format(time.RFC3339Nano)
		}
		if !f.LastTradeTS.IsZero() {
			entry["last_trade_ts"] = f.LastTradeTS.Format(time.RFC3339Nano)
		}
		if f.MedianLagMs > 0 {
			entry["median_lag_ms"] = f.MedianLagMs
		}
		resp = append(resp, entry)
	}

	if resp == nil {
		resp = []map[string]interface{}{}
	}
	s.writeJSON(w, resp)
}

func (s *Server) writeJSON(w http.ResponseWriter, v interface{}) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.logger.Error("encode response", "err", err)
	}
}

// buildSettlementResp constructs the JSON response map for a settlement query.
func (s *Server) buildSettlementResp(ts time.Time, snap *domain.Snapshot1s, status string) map[string]interface{} {
	return map[string]interface{}{
		"settlement_ts":  ts.Format(time.RFC3339),
		"symbol":         snap.CanonicalSymbol,
		"price":          snap.CanonicalPrice.String(),
		"status":         status,
		"basis":          snap.Basis,
		"quality_score":  snap.QualityScore.InexactFloat64(),
		"source_count":   snap.SourceCount,
		"sources_used":   snap.SourcesUsed,
		"finalized_at":   snap.FinalizedAt.Format(time.RFC3339Nano),
		"source_details": snap.SourceDetailsJSON,
	}
}

// tryReaggregate attempts to build a better settlement price from raw trades
// when the pre-computed snapshot is degraded or stale. Falls back to the
// original snapshot if re-aggregation doesn't improve source coverage.
func (s *Server) tryReaggregate(ctx context.Context, ts time.Time, original *domain.Snapshot1s) map[string]interface{} {
	trades, err := s.db.QueryClosestTradePerSource(ctx, ts, s.settlementWindow)
	if err != nil || len(trades) == 0 {
		// Re-aggregation failed or no trades found — return original snapshot
		status := "stale"
		if !original.IsStale && original.IsDegraded {
			status = "degraded"
		}
		return s.buildSettlementResp(ts, original, status)
	}

	// If re-aggregation didn't find more sources than the original, keep original
	if len(trades) <= original.SourceCount {
		status := "degraded"
		if original.IsStale {
			status = "stale"
		}
		return s.buildSettlementResp(ts, original, status)
	}

	// Re-aggregation found more sources — compute median price
	prices := make([]decimal.Decimal, len(trades))
	sources := make([]string, len(trades))
	var latestTS time.Time
	for i, t := range trades {
		prices[i] = t.RefPrice
		sources[i] = t.Source
		if t.EventTS.After(latestTS) {
			latestTS = t.EventTS
		}
	}
	sort.Strings(sources)

	median := computeMedian(prices)
	isDegraded := len(trades) < s.minHealthySources

	status := "confirmed"
	if isDegraded {
		status = "degraded"
	}

	// Build source details for the re-aggregated result
	details := make([]domain.VenueRefPrice, len(trades))
	copy(details, trades)
	detailsJSON, err2 := json.Marshal(details)
	if err2 != nil {
		detailsJSON = []byte("{}")
	}

	// Quality score: source_score (50%) + age_penalty (30%) + basis (20%)
	sourceScore := float64(len(trades)) / 3.0
	if sourceScore > 1.0 {
		sourceScore = 1.0
	}
	var avgAge float64
	for _, t := range trades {
		age := t.AgeMs
		if age < 0 {
			age = -age
		}
		avgAge += float64(age)
	}
	avgAge /= float64(len(trades))
	agePenalty := 1.0 - avgAge/5000.0
	if agePenalty < 0 {
		agePenalty = 0
	}
	quality := sourceScore*0.5 + agePenalty*0.3 + 0.2 // no midpoint penalty

	basis := "median_trade"
	if len(trades) == 1 {
		basis = "single_trade"
	}

	s.logger.Info("settlement re-aggregated from raw trades",
		"ts", ts, "original_sources", original.SourceCount,
		"reagg_sources", len(trades), "sources", sources,
	)

	return map[string]interface{}{
		"settlement_ts":  ts.Format(time.RFC3339),
		"symbol":         original.CanonicalSymbol,
		"price":          median.String(),
		"status":         status,
		"basis":          basis,
		"quality_score":  quality,
		"source_count":   len(trades),
		"sources_used":   sources,
		"finalized_at":   original.FinalizedAt.Format(time.RFC3339Nano),
		"source_details": detailsJSON,
	}
}

// computeMedian returns the median of a decimal slice. Returns zero for empty input.
func computeMedian(prices []decimal.Decimal) decimal.Decimal {
	if len(prices) == 0 {
		return decimal.Zero
	}
	sorted := make([]decimal.Decimal, len(prices))
	copy(sorted, prices)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].LessThan(sorted[j]) })
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return sorted[n/2-1].Add(sorted[n/2]).Div(decimal.NewFromInt(2))
}
