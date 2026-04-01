package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/justar9/btick/internal/config"
	"github.com/justar9/btick/internal/domain"
	"github.com/shopspring/decimal"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testHub() *WSHub {
	return NewWSHub(testLogger(), config.WSConfig{}, func(_ string) *domain.LatestState { return nil }, []string{"BTC/USD"})
}

func testHubWithState(state *domain.LatestState) *WSHub {
	return NewWSHub(testLogger(), config.WSConfig{}, func(_ string) *domain.LatestState { return state }, []string{"BTC/USD"})
}

func mustSetReadDeadline(t *testing.T, conn *websocket.Conn, d time.Duration) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(d)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
}

func mustUnmarshalWS(t *testing.T, data []byte, v interface{}) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal ws message: %v", err)
	}
}

func mustWriteMessage(t *testing.T, conn *websocket.Conn, msgType int, data []byte) {
	t.Helper()
	if err := conn.WriteMessage(msgType, data); err != nil {
		t.Fatalf("write message: %v", err)
	}
}

// =============================================================================
// WSHub Basic Tests
// =============================================================================

func TestWSHub_NewHub(t *testing.T) {
	hub := testHub()
	if hub == nil {
		t.Fatal("hub should not be nil")
	}
	if hub.clients == nil {
		t.Fatal("clients map should be initialized")
	}
}

func TestWSHub_Broadcast_NoClients(t *testing.T) {
	hub := testHub()

	// Should not panic with no clients
	hub.Broadcast(WSMessage{
		Type:  "test",
		Price: "84100.00",
	})
}

func TestWSHub_ClientConnect(t *testing.T) {
	hub := testHub()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Drain welcome + initial state
	drainMessages(t, conn, 2)

	hub.mu.RLock()
	clientCount := len(hub.clients)
	hub.mu.RUnlock()

	if clientCount != 1 {
		t.Errorf("expected 1 client, got %d", clientCount)
	}
}

func TestWSHub_ClientDisconnect(t *testing.T) {
	hub := testHub()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}

	// Drain welcome + initial state
	drainMessages(t, conn, 2)

	_ = conn.Close()

	// Wait for cleanup
	time.Sleep(100 * time.Millisecond)

	hub.mu.RLock()
	clientCount := len(hub.clients)
	hub.mu.RUnlock()

	if clientCount != 0 {
		t.Errorf("expected 0 clients after disconnect, got %d", clientCount)
	}
}

func TestWSHub_BroadcastToClient(t *testing.T) {
	hub := testHub()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Drain welcome + initial state
	drainMessages(t, conn, 2)

	// Broadcast message
	hub.Broadcast(WSMessage{
		Type:        "latest_price",
		TS:          "2026-03-19T09:10:00Z",
		Price:       "84100.00",
		SourceCount: 3,
	})

	// Read message
	mustSetReadDeadline(t, conn, time.Second)
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	msgStr := string(msg)
	if !contains(msgStr, "latest_price") {
		t.Errorf("message should contain 'latest_price': %s", msgStr)
	}
	if !contains(msgStr, "84100.00") {
		t.Errorf("message should contain price: %s", msgStr)
	}
}

func TestWSHub_MultipleClients(t *testing.T) {
	hub := testHub()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	numClients := 5
	conns := make([]*websocket.Conn, numClients)

	for i := 0; i < numClients; i++ {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("dial error for client %d: %v", i, err)
		}
		conns[i] = conn
		// Drain welcome + initial state
		drainMessages(t, conn, 2)
	}
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()

	hub.mu.RLock()
	clientCount := len(hub.clients)
	hub.mu.RUnlock()

	if clientCount != numClients {
		t.Errorf("expected %d clients, got %d", numClients, clientCount)
	}

	// Broadcast and verify all clients receive
	hub.Broadcast(WSMessage{
		Type:  "test",
		Price: "84200.00",
	})

	for i, conn := range conns {
		mustSetReadDeadline(t, conn, time.Second)
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("client %d read error: %v", i, err)
			continue
		}
		if !contains(string(msg), "84200.00") {
			t.Errorf("client %d did not receive broadcast: %s", i, msg)
		}
	}
}

// =============================================================================
// Welcome + Initial State Tests
// =============================================================================

func TestWSHub_WelcomeMessage(t *testing.T) {
	hub := testHub()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// First message should be welcome
	mustSetReadDeadline(t, conn, time.Second)
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	var wsMsg WSMessage
	if err := json.Unmarshal(msg, &wsMsg); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if wsMsg.Type != "welcome" {
		t.Errorf("expected type 'welcome', got %q", wsMsg.Type)
	}
	if wsMsg.Message != "btick/v1" {
		t.Errorf("expected message 'btick/v1', got %q", wsMsg.Message)
	}
	if wsMsg.TS == "" {
		t.Error("welcome message should have a timestamp")
	}
	if wsMsg.Seq != 0 {
		t.Errorf("welcome should have no seq, got %d", wsMsg.Seq)
	}
}

func TestWSHub_InitialState_WithData(t *testing.T) {
	state := &domain.LatestState{
		TS:           time.Date(2026, 3, 19, 9, 10, 0, 0, time.UTC),
		Price:        decimal.NewFromFloat(84150.00),
		Basis:        "median_trade",
		QualityScore: decimal.NewFromFloat(0.95),
		SourceCount:  3,
		SourcesUsed:  []string{"binance", "coinbase", "kraken"},
	}
	hub := testHubWithState(state)

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Skip welcome
	mustSetReadDeadline(t, conn, time.Second)
	_, _, _ = conn.ReadMessage()

	// Second message should be initial state
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	var wsMsg WSMessage
	if err := json.Unmarshal(msg, &wsMsg); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if wsMsg.Type != "latest_price" {
		t.Errorf("expected type 'latest_price', got %q", wsMsg.Type)
	}
	if wsMsg.Message != "initial_state" {
		t.Errorf("expected message 'initial_state', got %q", wsMsg.Message)
	}
	if wsMsg.Price != "84150" {
		t.Errorf("expected price '84150', got %q", wsMsg.Price)
	}
	if wsMsg.SourceCount != 3 {
		t.Errorf("expected source_count 3, got %d", wsMsg.SourceCount)
	}
	if wsMsg.Seq != 0 {
		t.Errorf("initial state should have no seq, got %d", wsMsg.Seq)
	}
}

func TestWSHub_InitialState_NoData(t *testing.T) {
	hub := testHubWithState(nil) // getState returns nil

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Skip welcome
	mustSetReadDeadline(t, conn, time.Second)
	_, _, _ = conn.ReadMessage()

	// Second message should be no_data_yet
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	var wsMsg WSMessage
	if err := json.Unmarshal(msg, &wsMsg); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if wsMsg.Type != "latest_price" {
		t.Errorf("expected type 'latest_price', got %q", wsMsg.Type)
	}
	if wsMsg.Message != "no_data_yet" {
		t.Errorf("expected message 'no_data_yet', got %q", wsMsg.Message)
	}
}

func TestWSHub_InitialState_NilGetState(t *testing.T) {
	// getState callback is nil — no initial state should be sent
	hub := NewWSHub(testLogger(), config.WSConfig{}, nil, []string{"BTC/USD"})

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Should get welcome only
	mustSetReadDeadline(t, conn, time.Second)
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	var wsMsg WSMessage
	mustUnmarshalWS(t, msg, &wsMsg)
	if wsMsg.Type != "welcome" {
		t.Errorf("expected welcome, got %q", wsMsg.Type)
	}

	// Next read should timeout (no initial state sent)
	mustSetReadDeadline(t, conn, 200*time.Millisecond)
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Error("expected timeout with no initial state message")
	}
}

// =============================================================================
// Sequence Number Tests
// =============================================================================

func TestWSHub_SequenceNumbers(t *testing.T) {
	hub := testHub()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Drain welcome + initial state
	drainMessages(t, conn, 2)

	// Small wait for writer goroutine to be fully running
	time.Sleep(50 * time.Millisecond)

	// Broadcast and read one at a time to avoid timing issues
	var lastSeq uint64
	for i := 0; i < 5; i++ {
		hub.Broadcast(WSMessage{
			Type:  "latest_price",
			Price: "84100.00",
		})

		mustSetReadDeadline(t, conn, 2*time.Second)
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read error on message %d: %v", i, err)
		}

		var wsMsg WSMessage
		mustUnmarshalWS(t, msg, &wsMsg)

		if wsMsg.Seq == 0 {
			t.Errorf("message %d should have non-zero seq", i)
		}
		if wsMsg.Seq <= lastSeq {
			t.Errorf("seq should be monotonically increasing: got %d after %d", wsMsg.Seq, lastSeq)
		}
		lastSeq = wsMsg.Seq
	}
}

// =============================================================================
// Subscription Filtering Tests
// =============================================================================

func TestWSHub_Subscribe_DefaultAllOn(t *testing.T) {
	hub := testHub()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Drain welcome + initial state
	drainMessages(t, conn, 2)

	// Client sends nothing — should get all types
	hub.Broadcast(WSMessage{Type: "snapshot_1s", Price: "84100.00"})
	hub.Broadcast(WSMessage{Type: "latest_price", Price: "84200.00"})

	for _, expected := range []string{"snapshot_1s", "latest_price"} {
		mustSetReadDeadline(t, conn, time.Second)
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("expected to receive %s: %v", expected, err)
		}
		var wsMsg WSMessage
		mustUnmarshalWS(t, msg, &wsMsg)
		if wsMsg.Type != expected {
			t.Errorf("expected type %q, got %q", expected, wsMsg.Type)
		}
	}
}

func TestWSHub_Unsubscribe(t *testing.T) {
	hub := testHub()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Drain welcome + initial state
	drainMessages(t, conn, 2)

	// Unsubscribe from snapshot_1s
	unsubMsg, _ := json.Marshal(clientAction{
		Action: "unsubscribe",
		Types:  []string{"snapshot_1s"},
	})
	mustWriteMessage(t, conn, websocket.TextMessage, unsubMsg)

	// Give the reader goroutine time to process
	time.Sleep(50 * time.Millisecond)

	// Broadcast both types
	hub.Broadcast(WSMessage{Type: "snapshot_1s", Price: "84100.00"})
	hub.Broadcast(WSMessage{Type: "latest_price", Price: "84200.00"})

	// Should only receive latest_price
	mustSetReadDeadline(t, conn, time.Second)
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	var wsMsg WSMessage
	mustUnmarshalWS(t, msg, &wsMsg)
	if wsMsg.Type != "latest_price" {
		t.Errorf("expected 'latest_price' (snapshot_1s unsubscribed), got %q", wsMsg.Type)
	}
}

func TestWSHub_Resubscribe(t *testing.T) {
	hub := testHub()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Drain welcome + initial state
	drainMessages(t, conn, 2)

	// Unsubscribe from snapshot_1s
	unsubMsg, _ := json.Marshal(clientAction{
		Action: "unsubscribe",
		Types:  []string{"snapshot_1s"},
	})
	mustWriteMessage(t, conn, websocket.TextMessage, unsubMsg)
	time.Sleep(50 * time.Millisecond)

	// Re-subscribe
	subMsg, _ := json.Marshal(clientAction{
		Action: "subscribe",
		Types:  []string{"snapshot_1s"},
	})
	mustWriteMessage(t, conn, websocket.TextMessage, subMsg)
	time.Sleep(50 * time.Millisecond)

	// Should receive snapshot_1s again
	hub.Broadcast(WSMessage{Type: "snapshot_1s", Price: "84100.00"})

	mustSetReadDeadline(t, conn, time.Second)
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	var wsMsg WSMessage
	mustUnmarshalWS(t, msg, &wsMsg)
	if wsMsg.Type != "snapshot_1s" {
		t.Errorf("expected 'snapshot_1s' after resubscribe, got %q", wsMsg.Type)
	}
}

// =============================================================================
// Heartbeat Tests
// =============================================================================

func TestWSHub_Heartbeat(t *testing.T) {
	wsCfg := config.WSConfig{
		HeartbeatIntervalS: 1, // 1 second for test speed
	}
	hub := NewWSHub(testLogger(), wsCfg, func(_ string) *domain.LatestState { return nil }, []string{"BTC/USD"})

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)

	wsURL := "ws" + server.URL[4:]

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Drain welcome + no_data_yet
	drainMessages(t, conn, 2)

	// Wait for heartbeat (should arrive within ~1.5s)
	mustSetReadDeadline(t, conn, 3*time.Second)
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("expected heartbeat: %v", err)
	}

	var wsMsg WSMessage
	mustUnmarshalWS(t, msg, &wsMsg)
	if wsMsg.Type != "heartbeat" {
		t.Errorf("expected type 'heartbeat', got %q", wsMsg.Type)
	}
	if wsMsg.Seq == 0 {
		t.Error("heartbeat should have a seq number")
	}
	if wsMsg.TS == "" {
		t.Error("heartbeat should have a timestamp")
	}
}

func TestWSHub_Heartbeat_Unsubscribe(t *testing.T) {
	wsCfg := config.WSConfig{
		HeartbeatIntervalS: 1,
	}
	hub := NewWSHub(testLogger(), wsCfg, func(_ string) *domain.LatestState { return nil }, []string{"BTC/USD"})

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)

	wsURL := "ws" + server.URL[4:]

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Drain welcome + no_data_yet
	drainMessages(t, conn, 2)

	// Unsubscribe from heartbeat
	unsubMsg, _ := json.Marshal(clientAction{
		Action: "unsubscribe",
		Types:  []string{"heartbeat"},
	})
	mustWriteMessage(t, conn, websocket.TextMessage, unsubMsg)
	time.Sleep(50 * time.Millisecond)

	// Should NOT receive heartbeat
	mustSetReadDeadline(t, conn, 2*time.Second)
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Error("expected timeout — heartbeat should not arrive when unsubscribed")
	}
}

// =============================================================================
// Subscription set() coverage
// =============================================================================

func TestSubscriptions_SetHeartbeat(t *testing.T) {
	subs := newSubscriptions()
	if !subs.wants("heartbeat") {
		t.Fatal("heartbeat should be on by default")
	}
	subs.set("heartbeat", false)
	if subs.wants("heartbeat") {
		t.Fatal("heartbeat should be off after set(false)")
	}
	subs.set("heartbeat", true)
	if !subs.wants("heartbeat") {
		t.Fatal("heartbeat should be on after set(true)")
	}
}

func TestSubscriptions_SetLatestPrice(t *testing.T) {
	subs := newSubscriptions()
	if !subs.wants("latest_price") {
		t.Fatal("latest_price should be on by default")
	}
	subs.set("latest_price", false)
	if subs.wants("latest_price") {
		t.Fatal("latest_price should be off after set(false)")
	}
	subs.set("latest_price", true)
	if !subs.wants("latest_price") {
		t.Fatal("latest_price should be on after set(true)")
	}
}

func TestSubscriptions_UnknownType(t *testing.T) {
	subs := newSubscriptions()
	// set with unknown type is a no-op
	subs.set("unknown_type", false)
	// wants unknown type always returns true
	if !subs.wants("unknown_type") {
		t.Error("unknown type should always pass wants()")
	}
}

// =============================================================================
// HandleWS edge cases
// =============================================================================

func TestWSHub_PingPong(t *testing.T) {
	// Use very short ping interval so the writer goroutine's ping ticker fires
	wsCfg := config.WSConfig{
		PingIntervalS:      1,
		HeartbeatIntervalS: 60, // don't interfere
		ReadDeadlineS:      5,
	}
	hub := NewWSHub(testLogger(), wsCfg, func(_ string) *domain.LatestState { return nil }, []string{"BTC/USD"})

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	// Set up a pong handler on client side to verify ping was received
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// The gorilla client automatically responds to pings with pongs.
	// The server's pong handler resets the read deadline.
	// Wait for the ping to be sent (1 second interval) and verify connection stays alive.
	drainMessages(t, conn, 2) // welcome + no_data_yet

	// Wait for at least one ping cycle
	time.Sleep(1500 * time.Millisecond)

	// Connection should still be alive — broadcast a message and receive it
	hub.Broadcast(WSMessage{Type: "latest_price", Price: "84100.00"})
	mustSetReadDeadline(t, conn, 2*time.Second)
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("connection should be alive after ping/pong: %v", err)
	}
	if !contains(string(msg), "84100.00") {
		t.Errorf("unexpected message: %s", msg)
	}
}

func TestWSHub_HandleWS_UpgradeFail(t *testing.T) {
	hub := testHub()
	// Call HandleWS without a proper websocket upgrade — should fail gracefully
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ws/price", nil)
	hub.HandleWS(rr, req)
	// No panic, upgrade fails, client not added
	hub.mu.RLock()
	count := len(hub.clients)
	hub.mu.RUnlock()
	if count != 0 {
		t.Errorf("expected 0 clients after failed upgrade, got %d", count)
	}
}

func TestWSHub_Broadcast_MarshalError(t *testing.T) {
	hub := testHub()
	hub.marshal = func(v any) ([]byte, error) {
		return nil, errors.New("forced marshal error")
	}
	// Should not panic, just log and return
	hub.Broadcast(WSMessage{Type: "test", Price: "84100.00"})
}

func TestWSHub_Subscribe_UnknownAction(t *testing.T) {
	hub := testHub()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer func() { _ = conn.Close() }()

	drainMessages(t, conn, 2)

	// Send unknown action — should be silently ignored
	mustWriteMessage(t, conn, websocket.TextMessage, []byte(`{"action":"reset","types":["all"]}`))
	// Send garbage — should be silently ignored
	mustWriteMessage(t, conn, websocket.TextMessage, []byte(`not json`))

	time.Sleep(50 * time.Millisecond)

	// Client should still work
	hub.Broadcast(WSMessage{Type: "latest_price", Price: "84100.00"})
	mustSetReadDeadline(t, conn, time.Second)
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("client should still receive messages: %v", err)
	}
	if !contains(string(msg), "84100.00") {
		t.Errorf("unexpected message: %s", msg)
	}
}

// =============================================================================
// Stress Tests
// =============================================================================

func TestWSHub_HighFrequencyBroadcast(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	hub := testHub()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Drain welcome + initial state
	drainMessages(t, conn, 2)

	numMessages := 1000
	var wg sync.WaitGroup
	wg.Add(1)

	var received int64
	go func() {
		defer wg.Done()
		for {
			if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				return
			}
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
			atomic.AddInt64(&received, 1)
		}
	}()

	for i := 0; i < numMessages; i++ {
		hub.Broadcast(WSMessage{
			Type:  "latest_price",
			Price: "84100.00",
		})
	}

	time.Sleep(time.Second)
	_ = conn.Close()
	wg.Wait()

	count := atomic.LoadInt64(&received)
	t.Logf("Received %d/%d messages (%.1f%%)", count, numMessages, float64(count)/float64(numMessages)*100)

	// Buffer is 256, so at minimum we should receive that many from 1000 broadcasts
	if count < int64(numMessages/4) {
		t.Errorf("received too few messages: %d/%d", count, numMessages)
	}
}

func TestWSHub_SlowClient_DoesNotBlock(t *testing.T) {
	hub := testHub()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	// Connect "slow" client that doesn't read
	slowConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer func() { _ = slowConn.Close() }()

	// Connect fast client
	fastConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer func() { _ = fastConn.Close() }()

	// Drain welcome + initial state from fast client
	drainMessages(t, fastConn, 2)

	// Broadcast many messages - should not block even with slow client
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			hub.Broadcast(WSMessage{
				Type:  "test",
				Price: "84100.00",
			})
		}
		close(done)
	}()

	select {
	case <-done:
		// Good - broadcast didn't block
	case <-time.After(5 * time.Second):
		t.Fatal("broadcast blocked due to slow client")
	}

	// Fast client should still receive messages
	mustSetReadDeadline(t, fastConn, time.Second)
	_, _, err = fastConn.ReadMessage()
	if err != nil {
		t.Errorf("fast client should receive messages: %v", err)
	}
}

func TestWSHub_ManyClientsConnect(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	hub := testHub()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	numClients := 100
	conns := make([]*websocket.Conn, 0, numClients)
	var mu sync.Mutex

	var wg sync.WaitGroup
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}()
	}

	wg.Wait()
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	connected := len(conns)
	mu.Unlock()

	t.Logf("Connected %d/%d clients", connected, numClients)

	// Cleanup
	mu.Lock()
	for _, c := range conns {
		_ = c.Close()
	}
	mu.Unlock()

	time.Sleep(200 * time.Millisecond)

	hub.mu.RLock()
	remaining := len(hub.clients)
	hub.mu.RUnlock()

	if remaining != 0 {
		t.Errorf("expected 0 clients after cleanup, got %d", remaining)
	}
}

// =============================================================================
// Graceful Shutdown Tests
// =============================================================================

func TestWSHub_GracefulShutdown(t *testing.T) {
	hub := testHub()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	numClients := 5
	conns := make([]*websocket.Conn, numClients)
	for i := 0; i < numClients; i++ {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("dial error: %v", err)
		}
		conns[i] = conn
	}

	time.Sleep(50 * time.Millisecond)

	// Shutdown hub
	ctx, cancel := context.WithCancel(context.Background())
	go hub.Run(ctx)

	cancel()
	time.Sleep(100 * time.Millisecond)

	hub.mu.RLock()
	remaining := len(hub.clients)
	hub.mu.RUnlock()

	if remaining != 0 {
		t.Errorf("expected 0 clients after shutdown, got %d", remaining)
	}

	for _, conn := range conns {
		// Drain any pending welcome/initial state, then verify connection is closed
		for {
			if err := conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
				break
			}
			_, _, err := conn.ReadMessage()
			if err != nil {
				break // connection closed or timed out — expected
			}
		}
	}
}

// =============================================================================
// Edge Cases
// =============================================================================

func TestWSHub_CloseOnce_NoPanic(t *testing.T) {
	hub := testHub()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Close multiple times - should not panic
	_ = conn.Close()
	_ = conn.Close()
	_ = conn.Close()

	time.Sleep(100 * time.Millisecond)

	hub.mu.RLock()
	clientCount := len(hub.clients)
	hub.mu.RUnlock()

	if clientCount != 0 {
		t.Errorf("expected 0 clients, got %d", clientCount)
	}
}

func TestWSHub_BroadcastDuringClientChurn(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	hub := testHub()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
				if err != nil {
					continue
				}
				time.Sleep(10 * time.Millisecond)
				_ = conn.Close()
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				hub.Broadcast(WSMessage{
					Type:  "test",
					Price: "84100.00",
				})
				time.Sleep(time.Millisecond)
			}
		}
	}()

	wg.Wait()
	// Test passes if no panic
}

// =============================================================================
// Connection Limit Tests
// =============================================================================

func TestWSHub_MaxConnectionLimit(t *testing.T) {
	hub := NewWSHub(testLogger(), config.WSConfig{
		MaxClients: 3,
	}, func(_ string) *domain.LatestState { return nil }, []string{"BTC/USD"})

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	// Connect 3 clients (should succeed)
	conns := make([]*websocket.Conn, 0, 3)
	for i := 0; i < 3; i++ {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("dial %d error: %v", i, err)
		}
		conns = append(conns, conn)
		drainMessages(t, conn, 2) // welcome + no_data_yet
	}

	time.Sleep(50 * time.Millisecond)

	if hub.ClientCount() != 3 {
		t.Fatalf("expected 3 clients, got %d", hub.ClientCount())
	}

	// 4th connection should be rejected (HTTP 503)
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("expected 4th connection to be rejected")
	}
	if resp != nil && resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}

	// Close one, then 4th should succeed
	_ = conns[0].Close()
	time.Sleep(100 * time.Millisecond)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial after slot freed: %v", err)
	}
	_ = conn.Close()

	for _, c := range conns[1:] {
		_ = c.Close()
	}
}

// =============================================================================
// Slow Client Eviction Tests
// =============================================================================

func TestWSHub_SlowClientEviction(t *testing.T) {
	hub := NewWSHub(testLogger(), config.WSConfig{
		SendBufferSize:     4,  // tiny buffer
		SlowClientMaxDrops: 10, // evict after 10 drops
	}, func(_ string) *domain.LatestState { return nil }, []string{"BTC/USD"})

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	// Connect slow client (don't read from it)
	slowConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer func() { _ = slowConn.Close() }()

	time.Sleep(50 * time.Millisecond)

	if hub.ClientCount() != 1 {
		t.Fatalf("expected 1 client, got %d", hub.ClientCount())
	}

	// Broadcast enough messages to fill buffer and trigger eviction
	for i := 0; i < 20; i++ {
		hub.Broadcast(WSMessage{
			Type:  "latest_price",
			Price: "84100.00",
		})
	}

	time.Sleep(100 * time.Millisecond)

	if hub.ClientCount() != 0 {
		t.Errorf("expected slow client to be evicted, got %d clients", hub.ClientCount())
	}
}

func TestWSHub_ClientCount(t *testing.T) {
	hub := testHub()
	if hub.ClientCount() != 0 {
		t.Errorf("expected 0 clients, got %d", hub.ClientCount())
	}
}

// =============================================================================
// Helpers
// =============================================================================

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// drainMessages reads and discards n messages from conn.
func drainMessages(t *testing.T, conn *websocket.Conn, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		mustSetReadDeadline(t, conn, time.Second)
		_, _, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("drain message %d: %v", i, err)
		}
	}
}
