package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/justar9/btick/internal/config"
	"github.com/justar9/btick/internal/domain"
	"github.com/shopspring/decimal"
)

// =============================================================================
// Mocks
// =============================================================================

type mockStore struct {
	snapshots     []domain.Snapshot1s
	snapshotsErr  error
	snapshotAt    *domain.Snapshot1s
	snapshotAtErr error
	ticks         []domain.CanonicalTick
	ticksErr      error
	rawTicks      []domain.RawEvent
	rawTicksErr   error
	feedHealth    []domain.FeedHealth
	feedHealthErr error
}

func (m *mockStore) QuerySnapshots(_ context.Context, _, _ time.Time) ([]domain.Snapshot1s, error) {
	return m.snapshots, m.snapshotsErr
}
func (m *mockStore) QuerySnapshotAt(_ context.Context, _ time.Time) (*domain.Snapshot1s, error) {
	return m.snapshotAt, m.snapshotAtErr
}
func (m *mockStore) QueryCanonicalTicks(_ context.Context, _ int) ([]domain.CanonicalTick, error) {
	return m.ticks, m.ticksErr
}
func (m *mockStore) QueryRawTicks(_ context.Context, _ string, _, _ time.Time, _ int) ([]domain.RawEvent, error) {
	return m.rawTicks, m.rawTicksErr
}
func (m *mockStore) QueryFeedHealth(_ context.Context) ([]domain.FeedHealth, error) {
	return m.feedHealth, m.feedHealthErr
}

type mockEngine struct {
	latestState *domain.LatestState
	snapshotCh  chan domain.Snapshot1s
	tickCh      chan domain.CanonicalTick
}

func newMockEngine(state *domain.LatestState) *mockEngine {
	return &mockEngine{
		latestState: state,
		snapshotCh:  make(chan domain.Snapshot1s, 10),
		tickCh:      make(chan domain.CanonicalTick, 10),
	}
}

func (m *mockEngine) LatestState() *domain.LatestState { return m.latestState }
func (m *mockEngine) SnapshotCh() <-chan domain.Snapshot1s {
	return m.snapshotCh
}
func (m *mockEngine) TickCh() <-chan domain.CanonicalTick {
	return m.tickCh
}

// testServer creates a Server with the given mocks.
func testServer(store Store, eng Engine) *Server {
	return &Server{
		httpAddr: ":0",
		wsPath:   "/ws/price",
		db:       store,
		engine:   eng,
		wsHub:    NewWSHub(testLogger(), config.WSConfig{}, nil),
		logger:   testLogger(),
	}
}

var refTime = time.Date(2026, 3, 19, 9, 10, 0, 0, time.UTC)

func sampleLatestState() *domain.LatestState {
	return &domain.LatestState{
		Symbol:       "BTC/USD",
		TS:           refTime,
		Price:        decimal.NewFromInt(84150),
		Basis:        "median_trade",
		QualityScore: decimal.NewFromFloat(0.95),
		SourceCount:  3,
		SourcesUsed:  []string{"binance", "coinbase", "kraken"},
	}
}

// =============================================================================
// handleLatest
// =============================================================================

func TestHandleLatest_NoData(t *testing.T) {
	s := testServer(nil, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleLatest(rr, httptest.NewRequest("GET", "/v1/price/latest", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
	expectBodyContains(t, rr, "no data yet")
}

func TestHandleLatest_WithData(t *testing.T) {
	state := sampleLatestState()
	s := testServer(nil, newMockEngine(state))
	rr := httptest.NewRecorder()
	s.handleLatest(rr, httptest.NewRequest("GET", "/v1/price/latest", nil))

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	var resp map[string]interface{}
	decodeJSON(t, rr, &resp)

	if resp["symbol"] != "BTC/USD" {
		t.Errorf("expected symbol BTC/USD, got %v", resp["symbol"])
	}
	if resp["price"] != "84150" {
		t.Errorf("expected price 84150, got %v", resp["price"])
	}
	if resp["basis"] != "median_trade" {
		t.Errorf("expected basis median_trade, got %v", resp["basis"])
	}
	if int(resp["source_count"].(float64)) != 3 {
		t.Errorf("expected source_count 3, got %v", resp["source_count"])
	}
}

func TestHandleLatest_StaleAndDegraded(t *testing.T) {
	state := sampleLatestState()
	state.IsStale = true
	state.IsDegraded = true
	s := testServer(nil, newMockEngine(state))
	rr := httptest.NewRecorder()
	s.handleLatest(rr, httptest.NewRequest("GET", "/v1/price/latest", nil))

	var resp map[string]interface{}
	decodeJSON(t, rr, &resp)
	if resp["is_stale"] != true {
		t.Errorf("expected is_stale true")
	}
	if resp["is_degraded"] != true {
		t.Errorf("expected is_degraded true")
	}
}

// =============================================================================
// handleSnapshots
// =============================================================================

func TestHandleSnapshots_MissingStart(t *testing.T) {
	s := testServer(&mockStore{}, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleSnapshots(rr, httptest.NewRequest("GET", "/v1/price/snapshots", nil))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
	expectBodyContains(t, rr, "start parameter required")
}

func TestHandleSnapshots_BadStart(t *testing.T) {
	s := testServer(&mockStore{}, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleSnapshots(rr, httptest.NewRequest("GET", "/v1/price/snapshots?start=badtime", nil))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
	expectBodyContains(t, rr, "invalid start time")
}

func TestHandleSnapshots_BadEnd(t *testing.T) {
	s := testServer(&mockStore{}, newMockEngine(nil))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/price/snapshots?start=2026-03-19T09:00:00Z&end=badtime", nil)
	s.handleSnapshots(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
	expectBodyContains(t, rr, "invalid end time")
}

func TestHandleSnapshots_NoDB(t *testing.T) {
	s := testServer(nil, newMockEngine(nil))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/price/snapshots?start=2026-03-19T09:00:00Z", nil)
	s.handleSnapshots(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
}

func TestHandleSnapshots_DBError(t *testing.T) {
	store := &mockStore{snapshotsErr: errors.New("db fail")}
	s := testServer(store, newMockEngine(nil))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/price/snapshots?start=2026-03-19T09:00:00Z", nil)
	s.handleSnapshots(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rr.Code)
	}
}

func TestHandleSnapshots_Success(t *testing.T) {
	snap := domain.Snapshot1s{
		TSSecond:        refTime,
		CanonicalSymbol: "BTC/USD",
		CanonicalPrice:  decimal.NewFromInt(84150),
		Basis:           "median_trade",
		QualityScore:    decimal.NewFromFloat(0.95),
		SourceCount:     3,
		SourcesUsed:     []string{"binance", "coinbase", "kraken"},
		FinalizedAt:     refTime.Add(time.Second),
	}
	store := &mockStore{snapshots: []domain.Snapshot1s{snap}}
	s := testServer(store, newMockEngine(nil))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/price/snapshots?start=2026-03-19T09:00:00Z&end=2026-03-19T09:15:00Z", nil)
	s.handleSnapshots(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	var resp []map[string]interface{}
	decodeJSON(t, rr, &resp)
	if len(resp) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(resp))
	}
	if resp[0]["price"] != "84150" {
		t.Errorf("expected price 84150, got %v", resp[0]["price"])
	}
}

func TestHandleSnapshots_Empty(t *testing.T) {
	store := &mockStore{snapshots: nil}
	s := testServer(store, newMockEngine(nil))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/price/snapshots?start=2026-03-19T09:00:00Z", nil)
	s.handleSnapshots(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	var resp []map[string]interface{}
	decodeJSON(t, rr, &resp)
	if len(resp) != 0 {
		t.Errorf("expected empty array, got %d items", len(resp))
	}
}

// =============================================================================
// handleTicks
// =============================================================================

func TestHandleTicks_NoDB(t *testing.T) {
	s := testServer(nil, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleTicks(rr, httptest.NewRequest("GET", "/v1/price/ticks", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
}

func TestHandleTicks_DBError(t *testing.T) {
	store := &mockStore{ticksErr: errors.New("db fail")}
	s := testServer(store, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleTicks(rr, httptest.NewRequest("GET", "/v1/price/ticks", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rr.Code)
	}
}

func TestHandleTicks_DefaultLimit(t *testing.T) {
	store := &mockStore{ticks: nil}
	s := testServer(store, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleTicks(rr, httptest.NewRequest("GET", "/v1/price/ticks", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestHandleTicks_CustomLimit(t *testing.T) {
	store := &mockStore{ticks: nil}
	s := testServer(store, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleTicks(rr, httptest.NewRequest("GET", "/v1/price/ticks?limit=50", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestHandleTicks_InvalidLimit(t *testing.T) {
	store := &mockStore{ticks: nil}
	s := testServer(store, newMockEngine(nil))
	rr := httptest.NewRecorder()
	// Invalid limit falls back to default, not an error
	s.handleTicks(rr, httptest.NewRequest("GET", "/v1/price/ticks?limit=abc", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestHandleTicks_Success(t *testing.T) {
	tick := domain.CanonicalTick{
		TickID:          uuid.New(),
		TSEvent:         refTime,
		CanonicalSymbol: "BTC/USD",
		CanonicalPrice:  decimal.NewFromInt(84150),
		Basis:           "median_trade",
		QualityScore:    decimal.NewFromFloat(0.95),
		SourceCount:     3,
		SourcesUsed:     []string{"binance", "coinbase", "kraken"},
	}
	store := &mockStore{ticks: []domain.CanonicalTick{tick}}
	s := testServer(store, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleTicks(rr, httptest.NewRequest("GET", "/v1/price/ticks?limit=10", nil))

	var resp []map[string]interface{}
	decodeJSON(t, rr, &resp)
	if len(resp) != 1 {
		t.Fatalf("expected 1 tick, got %d", len(resp))
	}
	if resp[0]["price"] != "84150" {
		t.Errorf("expected price 84150, got %v", resp[0]["price"])
	}
}

// =============================================================================
// handleRaw
// =============================================================================

func TestHandleRaw_NoDB(t *testing.T) {
	s := testServer(nil, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleRaw(rr, httptest.NewRequest("GET", "/v1/price/raw", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
}

func TestHandleRaw_BadStart(t *testing.T) {
	s := testServer(&mockStore{}, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleRaw(rr, httptest.NewRequest("GET", "/v1/price/raw?start=bad", nil))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestHandleRaw_BadEnd(t *testing.T) {
	s := testServer(&mockStore{}, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleRaw(rr, httptest.NewRequest("GET", "/v1/price/raw?end=bad", nil))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestHandleRaw_DBError(t *testing.T) {
	store := &mockStore{rawTicksErr: errors.New("db fail")}
	s := testServer(store, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleRaw(rr, httptest.NewRequest("GET", "/v1/price/raw", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rr.Code)
	}
}

func TestHandleRaw_Success(t *testing.T) {
	evt := domain.RawEvent{
		EventID:    uuid.New(),
		Source:     "binance",
		EventType:  "trade",
		ExchangeTS: refTime,
		RecvTS:     refTime,
		Price:      decimal.NewFromInt(84150),
		Size:       decimal.NewFromFloat(0.01),
		Side:       "buy",
		TradeID:    "12345",
	}
	store := &mockStore{rawTicks: []domain.RawEvent{evt}}
	s := testServer(store, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleRaw(rr, httptest.NewRequest("GET", "/v1/price/raw?source=binance&limit=10", nil))

	var resp []map[string]interface{}
	decodeJSON(t, rr, &resp)
	if len(resp) != 1 {
		t.Fatalf("expected 1 event, got %d", len(resp))
	}
	if resp[0]["source"] != "binance" {
		t.Errorf("expected source binance, got %v", resp[0]["source"])
	}
}

func TestHandleRaw_WithTimeRange(t *testing.T) {
	store := &mockStore{rawTicks: nil}
	s := testServer(store, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleRaw(rr, httptest.NewRequest("GET", "/v1/price/raw?start=2026-03-19T09:00:00Z&end=2026-03-19T09:15:00Z", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestHandleRaw_Empty(t *testing.T) {
	store := &mockStore{rawTicks: nil}
	s := testServer(store, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleRaw(rr, httptest.NewRequest("GET", "/v1/price/raw", nil))

	var resp []map[string]interface{}
	decodeJSON(t, rr, &resp)
	if len(resp) != 0 {
		t.Errorf("expected empty array, got %d items", len(resp))
	}
}

// =============================================================================
// handleHealth
// =============================================================================

func TestHandleHealth_NoData(t *testing.T) {
	s := testServer(nil, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleHealth(rr, httptest.NewRequest("GET", "/v1/health", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	var resp map[string]interface{}
	decodeJSON(t, rr, &resp)
	if resp["status"] != "no_data" {
		t.Errorf("expected status no_data, got %v", resp["status"])
	}
	if _, ok := resp["latest_price"]; ok {
		t.Error("should not have latest_price when no data")
	}
}

func TestHandleHealth_OK(t *testing.T) {
	state := sampleLatestState()
	s := testServer(nil, newMockEngine(state))
	rr := httptest.NewRecorder()
	s.handleHealth(rr, httptest.NewRequest("GET", "/v1/health", nil))

	var resp map[string]interface{}
	decodeJSON(t, rr, &resp)
	if resp["status"] != "ok" {
		t.Errorf("expected status ok, got %v", resp["status"])
	}
	if resp["latest_price"] != "84150" {
		t.Errorf("expected latest_price 84150, got %v", resp["latest_price"])
	}
}

func TestHandleHealth_Stale(t *testing.T) {
	state := sampleLatestState()
	state.IsStale = true
	s := testServer(nil, newMockEngine(state))
	rr := httptest.NewRecorder()
	s.handleHealth(rr, httptest.NewRequest("GET", "/v1/health", nil))

	var resp map[string]interface{}
	decodeJSON(t, rr, &resp)
	if resp["status"] != "stale" {
		t.Errorf("expected status stale, got %v", resp["status"])
	}
}

func TestHandleHealth_Degraded(t *testing.T) {
	state := sampleLatestState()
	state.IsDegraded = true
	s := testServer(nil, newMockEngine(state))
	rr := httptest.NewRecorder()
	s.handleHealth(rr, httptest.NewRequest("GET", "/v1/health", nil))

	var resp map[string]interface{}
	decodeJSON(t, rr, &resp)
	if resp["status"] != "degraded" {
		t.Errorf("expected status degraded, got %v", resp["status"])
	}
}

// =============================================================================
// handleSettlement
// =============================================================================

func TestHandleSettlement_MissingTS(t *testing.T) {
	s := testServer(&mockStore{}, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleSettlement(rr, httptest.NewRequest("GET", "/v1/price/settlement", nil))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
	expectBodyContains(t, rr, "ts parameter required")
}

func TestHandleSettlement_BadTS(t *testing.T) {
	s := testServer(&mockStore{}, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleSettlement(rr, httptest.NewRequest("GET", "/v1/price/settlement?ts=bad", nil))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
	expectBodyContains(t, rr, "invalid ts format")
}

func TestHandleSettlement_Not5MinBoundary(t *testing.T) {
	s := testServer(&mockStore{}, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleSettlement(rr, httptest.NewRequest("GET", "/v1/price/settlement?ts=2026-03-19T09:03:00Z", nil))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
	expectBodyContains(t, rr, "5-minute boundary")
}

func TestHandleSettlement_FutureTS(t *testing.T) {
	fakeNow := time.Date(2026, 3, 19, 9, 10, 30, 0, time.UTC)
	s := testServer(&mockStore{}, newMockEngine(nil))
	s.nowFunc = func() time.Time { return fakeNow }
	rr := httptest.NewRecorder()
	// 09:15:00 is in the future relative to fakeNow (09:10:30)
	s.handleSettlement(rr, httptest.NewRequest("GET", "/v1/price/settlement?ts=2026-03-19T09:15:00Z", nil))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
	expectBodyContains(t, rr, "cannot be in the future")
}

func TestHandleSettlement_TooRecent(t *testing.T) {
	// Fix the clock to 2 seconds after a 5-minute boundary
	boundary := time.Date(2026, 3, 19, 9, 10, 0, 0, time.UTC)
	fakeNow := boundary.Add(2 * time.Second)

	store := &mockStore{snapshotAtErr: errors.New("not found")}
	s := testServer(store, newMockEngine(nil))
	s.nowFunc = func() time.Time { return fakeNow }

	rr := httptest.NewRecorder()
	s.handleSettlement(rr, httptest.NewRequest("GET", "/v1/price/settlement?ts="+boundary.Format(time.RFC3339), nil))
	if rr.Code != http.StatusTooEarly {
		t.Errorf("expected 425, got %d", rr.Code)
	}
	expectBodyContains(t, rr, "not yet finalized")
}

func TestHandleSettlement_NoDB(t *testing.T) {
	// fakeNow is well past the 5-second grace for 09:00:00
	fakeNow := time.Date(2026, 3, 19, 9, 10, 30, 0, time.UTC)
	s := testServer(nil, newMockEngine(nil))
	s.nowFunc = func() time.Time { return fakeNow }
	rr := httptest.NewRecorder()
	s.handleSettlement(rr, httptest.NewRequest("GET", "/v1/price/settlement?ts=2026-03-19T09:00:00Z", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
}

func TestHandleSettlement_DBError(t *testing.T) {
	fakeNow := time.Date(2026, 3, 19, 9, 10, 30, 0, time.UTC)
	store := &mockStore{snapshotAtErr: errors.New("not found")}
	s := testServer(store, newMockEngine(nil))
	s.nowFunc = func() time.Time { return fakeNow }
	rr := httptest.NewRecorder()
	s.handleSettlement(rr, httptest.NewRequest("GET", "/v1/price/settlement?ts=2026-03-19T09:00:00Z", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestHandleSettlement_Confirmed(t *testing.T) {
	fakeNow := time.Date(2026, 3, 19, 9, 10, 30, 0, time.UTC)
	snap := &domain.Snapshot1s{
		TSSecond:        refTime,
		CanonicalSymbol: "BTC/USD",
		CanonicalPrice:  decimal.NewFromInt(84150),
		Basis:           "median_trade",
		QualityScore:    decimal.NewFromFloat(0.95),
		SourceCount:     3,
		SourcesUsed:     []string{"binance", "coinbase", "kraken"},
		FinalizedAt:     refTime.Add(time.Second),
	}
	store := &mockStore{snapshotAt: snap}
	s := testServer(store, newMockEngine(nil))
	s.nowFunc = func() time.Time { return fakeNow }
	rr := httptest.NewRecorder()
	s.handleSettlement(rr, httptest.NewRequest("GET", "/v1/price/settlement?ts=2026-03-19T09:10:00Z", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	var resp map[string]interface{}
	decodeJSON(t, rr, &resp)
	if resp["status"] != "confirmed" {
		t.Errorf("expected status confirmed, got %v", resp["status"])
	}
	if resp["price"] != "84150" {
		t.Errorf("expected price 84150, got %v", resp["price"])
	}
}

func TestHandleSettlement_Stale(t *testing.T) {
	fakeNow := time.Date(2026, 3, 19, 9, 10, 30, 0, time.UTC)
	snap := &domain.Snapshot1s{
		CanonicalPrice: decimal.NewFromInt(84150),
		IsStale:        true,
		FinalizedAt:    refTime,
	}
	store := &mockStore{snapshotAt: snap}
	s := testServer(store, newMockEngine(nil))
	s.nowFunc = func() time.Time { return fakeNow }
	rr := httptest.NewRecorder()
	s.handleSettlement(rr, httptest.NewRequest("GET", "/v1/price/settlement?ts=2026-03-19T09:10:00Z", nil))

	var resp map[string]interface{}
	decodeJSON(t, rr, &resp)
	if resp["status"] != "stale" {
		t.Errorf("expected status stale, got %v", resp["status"])
	}
}

func TestHandleSettlement_Degraded(t *testing.T) {
	fakeNow := time.Date(2026, 3, 19, 9, 10, 30, 0, time.UTC)
	snap := &domain.Snapshot1s{
		CanonicalPrice: decimal.NewFromInt(84150),
		IsDegraded:     true,
		FinalizedAt:    refTime,
	}
	store := &mockStore{snapshotAt: snap}
	s := testServer(store, newMockEngine(nil))
	s.nowFunc = func() time.Time { return fakeNow }
	rr := httptest.NewRecorder()
	s.handleSettlement(rr, httptest.NewRequest("GET", "/v1/price/settlement?ts=2026-03-19T09:10:00Z", nil))

	var resp map[string]interface{}
	decodeJSON(t, rr, &resp)
	if resp["status"] != "degraded" {
		t.Errorf("expected status degraded, got %v", resp["status"])
	}
}

func TestHandleSettlement_NonZeroSeconds(t *testing.T) {
	s := testServer(&mockStore{}, newMockEngine(nil))
	rr := httptest.NewRecorder()
	// 09:05:30 has non-zero seconds
	s.handleSettlement(rr, httptest.NewRequest("GET", "/v1/price/settlement?ts=2026-03-19T09:05:30Z", nil))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-zero seconds, got %d", rr.Code)
	}
}

// =============================================================================
// handleFeedHealth
// =============================================================================

func TestHandleFeedHealth_NoDB(t *testing.T) {
	s := testServer(nil, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleFeedHealth(rr, httptest.NewRequest("GET", "/v1/health/feeds", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
}

func TestHandleFeedHealth_TypedNilStore(t *testing.T) {
	var store *mockStore
	s := NewServer(":8080", "/ws/price", config.WSConfig{}, store, newMockEngine(nil), testLogger())
	rr := httptest.NewRecorder()

	s.handleFeedHealth(rr, httptest.NewRequest("GET", "/v1/health/feeds", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
}

func TestHandleFeedHealth_DBError(t *testing.T) {
	store := &mockStore{feedHealthErr: errors.New("db fail")}
	s := testServer(store, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleFeedHealth(rr, httptest.NewRequest("GET", "/v1/health/feeds", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rr.Code)
	}
}

func TestHandleFeedHealth_Success(t *testing.T) {
	feeds := []domain.FeedHealth{
		{
			Source:            "binance",
			ConnState:         "connected",
			LastMessageTS:     refTime,
			LastTradeTS:       refTime,
			ReconnectCount1h:  0,
			ConsecutiveErrors: 0,
			MedianLagMs:       42,
			Stale:             false,
			UpdatedAt:         refTime,
		},
		{
			Source:    "coinbase",
			ConnState: "connected",
			// LastMessageTS zero — should be omitted
			// LastTradeTS zero — should be omitted
			// MedianLagMs 0 — should be omitted
			UpdatedAt: refTime,
		},
	}
	store := &mockStore{feedHealth: feeds}
	s := testServer(store, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleFeedHealth(rr, httptest.NewRequest("GET", "/v1/health/feeds", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	var resp []map[string]interface{}
	decodeJSON(t, rr, &resp)
	if len(resp) != 2 {
		t.Fatalf("expected 2 feeds, got %d", len(resp))
	}

	// First feed has optional fields
	if resp[0]["source"] != "binance" {
		t.Errorf("expected source binance, got %v", resp[0]["source"])
	}
	if _, ok := resp[0]["last_message_ts"]; !ok {
		t.Error("expected last_message_ts for binance")
	}
	if _, ok := resp[0]["last_trade_ts"]; !ok {
		t.Error("expected last_trade_ts for binance")
	}
	if _, ok := resp[0]["median_lag_ms"]; !ok {
		t.Error("expected median_lag_ms for binance")
	}

	// Second feed omits zero-value optional fields
	if _, ok := resp[1]["last_message_ts"]; ok {
		t.Error("expected no last_message_ts for coinbase (zero value)")
	}
	if _, ok := resp[1]["last_trade_ts"]; ok {
		t.Error("expected no last_trade_ts for coinbase (zero value)")
	}
	if _, ok := resp[1]["median_lag_ms"]; ok {
		t.Error("expected no median_lag_ms for coinbase (zero value)")
	}
}

func TestHandleFeedHealth_Empty(t *testing.T) {
	store := &mockStore{feedHealth: nil}
	s := testServer(store, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleFeedHealth(rr, httptest.NewRequest("GET", "/v1/health/feeds", nil))

	var resp []map[string]interface{}
	decodeJSON(t, rr, &resp)
	if len(resp) != 0 {
		t.Errorf("expected empty array, got %d items", len(resp))
	}
}

// =============================================================================
// Middleware Tests
// =============================================================================

func TestCorsMiddleware_OPTIONS(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "inner")
	})
	handler := corsMiddleware(inner)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("OPTIONS", "/", nil))

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing CORS origin header")
	}
	if rr.Header().Get("Access-Control-Allow-Methods") != "GET, OPTIONS" {
		t.Error("missing CORS methods header")
	}
	if rr.Header().Get("Access-Control-Allow-Headers") != "Content-Type" {
		t.Error("missing CORS allowed-headers header")
	}
	// Body should be empty for OPTIONS
	if rr.Body.String() == "inner" {
		t.Error("OPTIONS should not call inner handler")
	}
}

func TestCorsMiddleware_GET(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "ok")
	})
	handler := corsMiddleware(inner)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))

	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing CORS origin header on GET")
	}
	if rr.Body.String() != "ok" {
		t.Errorf("expected body 'ok', got %q", rr.Body.String())
	}
}

func TestJsonMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	})
	handler := jsonMiddleware(inner)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))

	if rr.Header().Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", rr.Header().Get("Content-Type"))
	}
}

// =============================================================================
// broadcastLoop
// =============================================================================

func TestBroadcastLoop_SnapshotBroadcast(t *testing.T) {
	eng := newMockEngine(nil)
	s := testServer(nil, eng)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go s.broadcastLoop(ctx)

	snap := domain.Snapshot1s{
		TSSecond:       refTime,
		CanonicalPrice: decimal.NewFromInt(84150),
		Basis:          "median_trade",
		QualityScore:   decimal.NewFromFloat(0.95),
		SourceCount:    3,
		SourcesUsed:    []string{"binance", "coinbase", "kraken"},
	}
	eng.snapshotCh <- snap

	// Give the broadcastLoop time to process
	time.Sleep(50 * time.Millisecond)
	cancel()
}

func TestBroadcastLoop_TickBroadcast(t *testing.T) {
	eng := newMockEngine(nil)
	s := testServer(nil, eng)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go s.broadcastLoop(ctx)

	tick := domain.CanonicalTick{
		TSEvent:        refTime,
		CanonicalPrice: decimal.NewFromInt(84150),
		Basis:          "median_trade",
		QualityScore:   decimal.NewFromFloat(0.95),
		SourceCount:    3,
		SourcesUsed:    []string{"binance", "coinbase", "kraken"},
	}
	eng.tickCh <- tick

	time.Sleep(50 * time.Millisecond)
	cancel()
}

func TestBroadcastLoop_ContextCancel(t *testing.T) {
	eng := newMockEngine(nil)
	s := testServer(nil, eng)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.broadcastLoop(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
		// good
	case <-time.After(time.Second):
		t.Fatal("broadcastLoop did not return on context cancel")
	}
}

func TestBroadcastLoop_ChannelClose(t *testing.T) {
	eng := newMockEngine(nil)
	s := testServer(nil, eng)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		s.broadcastLoop(ctx)
		close(done)
	}()

	close(eng.snapshotCh)
	select {
	case <-done:
		// good
	case <-time.After(time.Second):
		t.Fatal("broadcastLoop did not return on channel close")
	}
}

// =============================================================================
// NewServer
// =============================================================================

func TestNewServer(t *testing.T) {
	eng := newMockEngine(sampleLatestState())
	s := NewServer(":8080", "/ws/price", config.WSConfig{}, nil, eng, testLogger())
	if s == nil {
		t.Fatal("server should not be nil")
	}
	if s.httpAddr != ":8080" {
		t.Errorf("expected addr :8080, got %s", s.httpAddr)
	}
	if s.wsHub == nil {
		t.Fatal("wsHub should be initialized")
	}
}

func TestServerHandler_Metrics(t *testing.T) {
	s := testServer(nil, newMockEngine(nil))

	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	s.handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("unexpected content type: %s", got)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "btick_writer_flush_duration_seconds") {
		t.Fatalf("expected metrics body to include writer flush metric, got %q", body)
	}
}

// =============================================================================
// Server.Run
// =============================================================================

func TestServerRun_StartAndShutdown(t *testing.T) {
	eng := newMockEngine(sampleLatestState())
	s := NewServer("127.0.0.1:0", "/ws/price", config.WSConfig{HeartbeatIntervalS: 60}, nil, eng, testLogger())

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Run(ctx)
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Cancel triggers graceful shutdown
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("expected nil error on clean shutdown, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down in time")
	}
}

func TestServerRun_BadAddr(t *testing.T) {
	eng := newMockEngine(nil)
	// Bind to an invalid address to trigger ListenAndServe error
	s := NewServer("999.999.999.999:99999", "/ws/price", config.WSConfig{HeartbeatIntervalS: 60}, nil, eng, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Run(ctx)
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Error("expected error for bad address")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not return error for bad address")
	}
}

// =============================================================================
// broadcastLoop — tick channel close
// =============================================================================

func TestBroadcastLoop_TickChannelClose(t *testing.T) {
	eng := newMockEngine(nil)
	s := testServer(nil, eng)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		s.broadcastLoop(ctx)
		close(done)
	}()

	close(eng.tickCh)
	select {
	case <-done:
		// good
	case <-time.After(time.Second):
		t.Fatal("broadcastLoop did not return on tick channel close")
	}
}

// =============================================================================
// Helpers
// =============================================================================

func decodeJSON(t *testing.T, rr *httptest.ResponseRecorder, v interface{}) {
	t.Helper()
	if err := json.NewDecoder(rr.Body).Decode(v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func expectBodyContains(t *testing.T, rr *httptest.ResponseRecorder, substr string) {
	t.Helper()
	if !contains(rr.Body.String(), substr) {
		t.Errorf("expected body to contain %q, got %q", substr, rr.Body.String())
	}
}
