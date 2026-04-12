package api

import (
	"bytes"
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
	"github.com/gorilla/websocket"
	"github.com/justar9/btick/internal/config"
	"github.com/justar9/btick/internal/domain"
	"github.com/shopspring/decimal"
)

// =============================================================================
// Mocks
// =============================================================================

type mockStore struct {
	snapshots        []domain.Snapshot1s
	snapshotsErr     error
	snapshotAt       *domain.Snapshot1s
	snapshotAtErr    error
	ticks            []domain.CanonicalTick
	ticksErr         error
	rawTicks         []domain.RawEvent
	rawTicksErr      error
	closestTrades    []domain.VenueRefPrice
	closestTradesErr error
	feedHealth       []domain.FeedHealth
	feedHealthErr    error
	apiAccounts      map[string]*domain.APIAccount
	createAccountErr error
	lookupAccountErr error
	touchAccountErr  error
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
func (m *mockStore) QueryClosestTradePerSource(_ context.Context, _ time.Time, _ time.Duration) ([]domain.VenueRefPrice, error) {
	return m.closestTrades, m.closestTradesErr
}
func (m *mockStore) QueryFeedHealth(_ context.Context) ([]domain.FeedHealth, error) {
	return m.feedHealth, m.feedHealthErr
}
func (m *mockStore) CreateAPIAccount(_ context.Context, email, name, tier, apiKeyHash, apiKeyPrefix string) (*domain.APIAccount, error) {
	if m.createAccountErr != nil {
		return nil, m.createAccountErr
	}
	if m.apiAccounts == nil {
		m.apiAccounts = make(map[string]*domain.APIAccount)
	}
	for _, account := range m.apiAccounts {
		if account.Email == email {
			return nil, errAPIAccountExists
		}
	}
	account := &domain.APIAccount{
		AccountID:    uuid.New(),
		Email:        email,
		Name:         name,
		Tier:         tier,
		APIKeyPrefix: apiKeyPrefix,
		Active:       true,
		CreatedAt:    time.Now().UTC(),
	}
	m.apiAccounts[apiKeyHash] = account
	return account, nil
}
func (m *mockStore) LookupAPIAccountByKeyHash(_ context.Context, apiKeyHash string) (*domain.APIAccount, error) {
	if m.lookupAccountErr != nil {
		return nil, m.lookupAccountErr
	}
	if m.apiAccounts == nil {
		return nil, nil
	}
	return m.apiAccounts[apiKeyHash], nil
}
func (m *mockStore) TouchAPIAccountLastUsed(_ context.Context, accountID string, usedAt time.Time) error {
	if m.touchAccountErr != nil {
		return m.touchAccountErr
	}
	for _, account := range m.apiAccounts {
		if account.AccountID.String() == accountID {
			account.LastUsedAt = usedAt
			return nil
		}
	}
	return nil
}

type mockEngine struct {
	latestState   *domain.LatestState
	symbols       []string
	snapshotCh    chan domain.Snapshot1s
	tickCh        chan domain.CanonicalTick
	sourcePriceCh chan domain.SourcePriceEvent
}

func newMockEngine(state *domain.LatestState) *mockEngine {
	return &mockEngine{
		latestState:   state,
		symbols:       []string{"BTC/USD"},
		snapshotCh:    make(chan domain.Snapshot1s, 10),
		tickCh:        make(chan domain.CanonicalTick, 10),
		sourcePriceCh: make(chan domain.SourcePriceEvent, 10),
	}
}

func (m *mockEngine) LatestState(_ string) *domain.LatestState      { return m.latestState }
func (m *mockEngine) Symbols() []string                             { return m.symbols }
func (m *mockEngine) SnapshotCh() <-chan domain.Snapshot1s          { return m.snapshotCh }
func (m *mockEngine) TickCh() <-chan domain.CanonicalTick           { return m.tickCh }
func (m *mockEngine) SourcePriceCh() <-chan domain.SourcePriceEvent { return m.sourcePriceCh }

// testServer creates a Server with the given mocks.
func testServer(store Store, eng Engine) *Server {
	return &Server{
		httpAddr:          ":0",
		wsPath:            "/ws/price",
		db:                store,
		engine:            eng,
		wsHub:             NewWSHub(testLogger(), config.WSConfig{}, nil, []string{"BTC/USD"}),
		logger:            testLogger(),
		settlementWindow:  5 * time.Second,
		minHealthySources: 2,
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
	deps := resp["dependencies"].(map[string]interface{})
	database := deps["database"].(map[string]interface{})
	if database["ready"] != false {
		t.Errorf("expected database.ready false, got %v", database["ready"])
	}
	if _, ok := resp["latest_price"]; ok {
		t.Error("should not have latest_price when no data")
	}
}

func TestHandleHealth_OK(t *testing.T) {
	state := sampleLatestState()
	s := testServer(&mockStore{}, newMockEngine(state))
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
	deps := resp["dependencies"].(map[string]interface{})
	database := deps["database"].(map[string]interface{})
	if database["ready"] != true {
		t.Errorf("expected database.ready true, got %v", database["ready"])
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
	s := NewServer(":8080", "/ws/price", config.WSConfig{}, config.PricingConfig{MinimumHealthySources: 2}, config.AccessConfig{}, store, newMockEngine(nil), testLogger())
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

func TestHandleMetadata_Defaults(t *testing.T) {
	s := testServer(nil, newMockEngine(nil))
	rr := httptest.NewRecorder()
	s.handleMetadata(rr, httptest.NewRequest("GET", "/v1/metadata?symbol=BTC/USD", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp map[string]interface{}
	decodeJSON(t, rr, &resp)
	if resp["symbol"] != "BTC/USD" {
		t.Fatalf("unexpected symbol: %v", resp["symbol"])
	}
	if resp["base_asset"] != "BTC" {
		t.Fatalf("unexpected base_asset: %v", resp["base_asset"])
	}
	if resp["quote_asset"] != "USD" {
		t.Fatalf("unexpected quote_asset: %v", resp["quote_asset"])
	}
	if resp["product_type"] != "price" {
		t.Fatalf("unexpected product_type: %v", resp["product_type"])
	}
	if resp["product_sub_type"] != "reference" {
		t.Fatalf("unexpected product_sub_type: %v", resp["product_sub_type"])
	}
	if resp["product_name"] != "BTC/USD Ref Price" {
		t.Fatalf("unexpected product_name: %v", resp["product_name"])
	}
	if resp["market_hours"] != "24/7" {
		t.Fatalf("unexpected market_hours: %v", resp["market_hours"])
	}
	if resp["feed_id"] != "btick-refprice-btc-usd" {
		t.Fatalf("unexpected feed_id: %v", resp["feed_id"])
	}
}

func TestHandleMetadata_ConfiguredOverrides(t *testing.T) {
	s := testServer(nil, newMockEngine(nil))
	s.SetSymbolMetadata(BuildSymbolMetadata([]config.SymbolConfig{{
		Canonical:      "BTC/USD",
		BaseAsset:      "BTC_CR",
		QuoteAsset:     "USD_FX",
		ProductType:    "price",
		ProductSubType: "reference",
		ProductName:    "BTC/USD-RefPrice-DS-Premium-Global-003",
		MarketHours:    "24/7/365",
		FeedID:         "feed-btc-usd-premium-global-003",
	}}))

	rr := httptest.NewRecorder()
	s.handleMetadata(rr, httptest.NewRequest("GET", "/v1/metadata?symbol=BTC/USD", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp map[string]interface{}
	decodeJSON(t, rr, &resp)
	if resp["base_asset"] != "BTC_CR" {
		t.Fatalf("unexpected base_asset: %v", resp["base_asset"])
	}
	if resp["quote_asset"] != "USD_FX" {
		t.Fatalf("unexpected quote_asset: %v", resp["quote_asset"])
	}
	if resp["product_type"] != "price" {
		t.Fatalf("unexpected product_type: %v", resp["product_type"])
	}
	if resp["product_sub_type"] != "reference" {
		t.Fatalf("unexpected product_sub_type: %v", resp["product_sub_type"])
	}
	if resp["product_name"] != "BTC/USD-RefPrice-DS-Premium-Global-003" {
		t.Fatalf("unexpected product_name: %v", resp["product_name"])
	}
	if resp["market_hours"] != "24/7/365" {
		t.Fatalf("unexpected market_hours: %v", resp["market_hours"])
	}
	if resp["feed_id"] != "feed-btc-usd-premium-global-003" {
		t.Fatalf("unexpected feed_id: %v", resp["feed_id"])
	}
}

func TestHandleMetadata_UnknownSymbol(t *testing.T) {
	eng := newMockEngine(nil)
	eng.symbols = []string{"BTC/USD"}
	s := testServer(nil, eng)
	rr := httptest.NewRecorder()
	s.handleMetadata(rr, httptest.NewRequest("GET", "/v1/metadata?symbol=SOL/USD", nil))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
	expectBodyContains(t, rr, "symbol not found")
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
	if rr.Header().Get("Access-Control-Allow-Methods") != "GET, POST, OPTIONS" {
		t.Error("missing CORS methods header")
	}
	if rr.Header().Get("Access-Control-Allow-Headers") != "Content-Type, Authorization, X-API-Key" {
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

func TestHandleSignup_Created(t *testing.T) {
	store := &mockStore{}
	s := testServer(store, newMockEngine(nil))
	s.accessCfg = config.AccessConfig{Enabled: true, SignupEnabled: true, DefaultSignupTier: "starter"}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/signup", bytes.NewBufferString(`{"email":"alice@example.com","name":"Alice"}`))
	s.handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rr.Code)
	}
	var resp map[string]interface{}
	decodeJSON(t, rr, &resp)
	if resp["email"] != "alice@example.com" {
		t.Fatalf("unexpected email: %v", resp["email"])
	}
	if resp["tier"] != "starter" {
		t.Fatalf("unexpected tier: %v", resp["tier"])
	}
	apiKey, _ := resp["api_key"].(string)
	if !strings.HasPrefix(apiKey, "btk_") {
		t.Fatalf("expected generated api key, got %q", apiKey)
	}
}

func TestHandleSignup_Conflict(t *testing.T) {
	store := &mockStore{apiAccounts: map[string]*domain.APIAccount{
		hashAPIKey("btk_existing"): {AccountID: uuid.New(), Email: "alice@example.com", Tier: "starter", Active: true},
	}}
	s := testServer(store, newMockEngine(nil))
	s.accessCfg = config.AccessConfig{Enabled: true, SignupEnabled: true, DefaultSignupTier: "starter"}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/signup", bytes.NewBufferString(`{"email":"alice@example.com"}`))
	s.handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rr.Code)
	}
}

func TestHandleAuthMe_MissingAPIKey(t *testing.T) {
	s := NewServer(":8080", "/ws/price", config.WSConfig{}, config.PricingConfig{}, config.AccessConfig{Enabled: true}, &mockStore{}, newMockEngine(nil), testLogger())

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)
	s.handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	expectBodyContains(t, rr, "api key required")
}

func TestHandleAuthMe_Success(t *testing.T) {
	accountID := uuid.New()
	createdAt := refTime.Add(-time.Hour)
	store := &mockStore{apiAccounts: map[string]*domain.APIAccount{
		hashAPIKey("btk_starter"): {
			AccountID:    accountID,
			Email:        "starter@example.com",
			Name:         "Starter User",
			Tier:         "starter",
			APIKeyPrefix: "btk_starter",
			Active:       true,
			CreatedAt:    createdAt,
		},
	}}
	s := NewServer(":8080", "/ws/price", config.WSConfig{}, config.PricingConfig{}, config.AccessConfig{Enabled: true}, store, newMockEngine(nil), testLogger())

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer btk_starter")
	s.handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-API-Tier"); got != "starter" {
		t.Fatalf("expected X-API-Tier starter, got %q", got)
	}

	var resp map[string]any
	decodeJSON(t, rr, &resp)
	if resp["account_id"] != accountID.String() {
		t.Fatalf("unexpected account_id: %v", resp["account_id"])
	}
	if resp["email"] != "starter@example.com" {
		t.Fatalf("unexpected email: %v", resp["email"])
	}
	if resp["tier"] != "starter" {
		t.Fatalf("unexpected tier: %v", resp["tier"])
	}
	if resp["active"] != true {
		t.Fatalf("expected active=true, got %v", resp["active"])
	}
	if resp["api_key_prefix"] != "btk_starter" {
		t.Fatalf("unexpected api_key_prefix: %v", resp["api_key_prefix"])
	}
	if resp["created_at"] != createdAt.Format(time.RFC3339Nano) {
		t.Fatalf("unexpected created_at: %v", resp["created_at"])
	}
	if _, ok := resp["last_used_at"]; !ok {
		t.Fatal("expected last_used_at to be present")
	}
	storedAccount := store.apiAccounts[hashAPIKey("btk_starter")]
	if storedAccount == nil || storedAccount.LastUsedAt.IsZero() {
		t.Fatal("expected store last_used_at to be updated")
	}
}

func TestRequireTier_MissingAPIKey(t *testing.T) {
	s := NewServer(":8080", "/ws/price", config.WSConfig{}, config.PricingConfig{}, config.AccessConfig{Enabled: true}, &mockStore{}, newMockEngine(nil), testLogger())

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/price/snapshots?start=2026-03-19T09:00:00Z", nil)
	s.handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	expectBodyContains(t, rr, "api key required")
}

func TestRequireTier_StarterAllowsSnapshots(t *testing.T) {
	store := &mockStore{
		snapshots: []domain.Snapshot1s{{
			TSSecond:        refTime,
			CanonicalSymbol: "BTC/USD",
			CanonicalPrice:  decimal.NewFromInt(84150),
			Basis:           "median_trade",
			QualityScore:    decimal.NewFromFloat(0.95),
			SourceCount:     3,
			SourcesUsed:     []string{"binance", "coinbase", "kraken"},
			FinalizedAt:     refTime.Add(time.Second),
		}},
		apiAccounts: map[string]*domain.APIAccount{
			hashAPIKey("btk_starter"): {AccountID: uuid.New(), Email: "starter@example.com", Tier: "starter", Active: true},
		},
	}
	s := NewServer(":8080", "/ws/price", config.WSConfig{}, config.PricingConfig{}, config.AccessConfig{Enabled: true}, store, newMockEngine(nil), testLogger())

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/price/snapshots?start=2026-03-19T09:00:00Z", nil)
	req.Header.Set("X-API-Key", "btk_starter")
	s.handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-API-Tier"); got != "starter" {
		t.Fatalf("expected X-API-Tier starter, got %q", got)
	}
	var resp []map[string]interface{}
	decodeJSON(t, rr, &resp)
	if len(resp) != 1 {
		t.Fatalf("expected one snapshot, got %d", len(resp))
	}
}

func TestRequireTier_StarterBlockedFromRaw(t *testing.T) {
	store := &mockStore{apiAccounts: map[string]*domain.APIAccount{
		hashAPIKey("btk_starter"): {AccountID: uuid.New(), Email: "starter@example.com", Tier: "starter", Active: true},
	}}
	s := NewServer(":8080", "/ws/price", config.WSConfig{}, config.PricingConfig{}, config.AccessConfig{Enabled: true}, store, newMockEngine(nil), testLogger())

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/price/raw", nil)
	req.Header.Set("X-API-Key", "btk_starter")
	s.handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
	expectBodyContains(t, rr, "tier upgrade required")
}

func TestRequireTier_WebSocketRequiresAPIKey(t *testing.T) {
	store := &mockStore{apiAccounts: map[string]*domain.APIAccount{
		hashAPIKey("btk_starter"): {AccountID: uuid.New(), Email: "starter@example.com", Tier: "starter", Active: true},
	}}
	s := NewServer(":0", "/ws/price", config.WSConfig{}, config.PricingConfig{}, config.AccessConfig{Enabled: true}, store, newMockEngine(nil), testLogger())
	server := httptest.NewServer(s.handler())
	defer server.Close()

	wsURL := "ws" + server.URL[4:] + "/ws/price"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("expected websocket dial without api key to fail")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 response, got %+v", resp)
	}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL+"?api_key=btk_starter", nil)
	if err != nil {
		t.Fatalf("expected websocket dial with api key to succeed: %v", err)
	}
	defer func() { _ = conn.Close() }()
	drainMessages(t, conn, 2)
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

func TestBroadcastLoop_SourcePriceBroadcast(t *testing.T) {
	eng := newMockEngine(nil)
	s := testServer(nil, eng)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go s.broadcastLoop(ctx)

	eng.sourcePriceCh <- domain.SourcePriceEvent{
		Symbol: "BTC/USD",
		Source: "binance",
		Price:  decimal.NewFromInt(84150),
		TS:     refTime,
	}

	// Give the broadcastLoop time to process
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
	eng.tickCh <- domain.CanonicalTick{
		CanonicalSymbol: "BTC/USD",
		CanonicalPrice:  decimal.NewFromInt(84150),
		TSEvent:         refTime,
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if s.wsHub.seq.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.wsHub.seq.Load() == 0 {
		t.Fatal("expected broadcastLoop to continue after one channel closes")
	}

	select {
	case <-done:
		t.Fatal("broadcastLoop should keep running while other channels remain open")
	default:
	}

	cancel()
	select {
	case <-done:
		// good
	case <-time.After(time.Second):
		t.Fatal("broadcastLoop did not return on context cancel")
	}
}

// =============================================================================
// NewServer
// =============================================================================

func TestNewServer(t *testing.T) {
	eng := newMockEngine(sampleLatestState())
	s := NewServer(":8080", "/ws/price", config.WSConfig{}, config.PricingConfig{}, config.AccessConfig{}, nil, eng, testLogger())
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
	s := NewServer("127.0.0.1:0", "/ws/price", config.WSConfig{HeartbeatIntervalS: 60}, config.PricingConfig{}, config.AccessConfig{}, nil, eng, testLogger())

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
	s := NewServer("999.999.999.999:99999", "/ws/price", config.WSConfig{HeartbeatIntervalS: 60}, config.PricingConfig{}, config.AccessConfig{}, nil, eng, testLogger())

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
	eng.snapshotCh <- domain.Snapshot1s{
		CanonicalSymbol: "BTC/USD",
		CanonicalPrice:  decimal.NewFromInt(84150),
		TSSecond:        refTime,
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if s.wsHub.seq.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.wsHub.seq.Load() == 0 {
		t.Fatal("expected broadcastLoop to continue after tick channel closes")
	}

	select {
	case <-done:
		t.Fatal("broadcastLoop should keep running while other channels remain open")
	default:
	}

	cancel()
	select {
	case <-done:
		// good
	case <-time.After(time.Second):
		t.Fatal("broadcastLoop did not return on context cancel")
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
