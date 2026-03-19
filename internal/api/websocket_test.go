package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// =============================================================================
// WSHub Basic Tests
// =============================================================================

func TestWSHub_NewHub(t *testing.T) {
	hub := NewWSHub(testLogger())
	if hub == nil {
		t.Fatal("hub should not be nil")
	}
	if hub.clients == nil {
		t.Fatal("clients map should be initialized")
	}
}

func TestWSHub_Broadcast_NoClients(t *testing.T) {
	hub := NewWSHub(testLogger())

	// Should not panic with no clients
	hub.Broadcast(WSMessage{
		Type:  "test",
		Price: "84100.00",
	})
}

func TestWSHub_ClientConnect(t *testing.T) {
	hub := NewWSHub(testLogger())

	// Create test server
	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	// Convert http URL to ws URL
	wsURL := "ws" + server.URL[4:]

	// Connect client
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer conn.Close()

	// Wait for connection to be registered
	time.Sleep(50 * time.Millisecond)

	hub.mu.RLock()
	clientCount := len(hub.clients)
	hub.mu.RUnlock()

	if clientCount != 1 {
		t.Errorf("expected 1 client, got %d", clientCount)
	}
}

func TestWSHub_ClientDisconnect(t *testing.T) {
	hub := NewWSHub(testLogger())

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}

	// Wait for connection
	time.Sleep(50 * time.Millisecond)

	// Close connection
	conn.Close()

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
	hub := NewWSHub(testLogger())

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer conn.Close()

	// Wait for connection
	time.Sleep(50 * time.Millisecond)

	// Broadcast message
	hub.Broadcast(WSMessage{
		Type:        "latest_price",
		TS:          "2026-03-19T09:10:00Z",
		Price:       "84100.00",
		SourceCount: 3,
	})

	// Read message
	conn.SetReadDeadline(time.Now().Add(time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	// Verify message contains expected data
	if len(msg) == 0 {
		t.Fatal("received empty message")
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
	hub := NewWSHub(testLogger())

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	// Connect multiple clients
	numClients := 5
	conns := make([]*websocket.Conn, numClients)

	for i := 0; i < numClients; i++ {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("dial error for client %d: %v", i, err)
		}
		conns[i] = conn
	}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()

	// Wait for connections
	time.Sleep(100 * time.Millisecond)

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
		conn.SetReadDeadline(time.Now().Add(time.Second))
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
// Stress Tests
// =============================================================================

func TestWSHub_HighFrequencyBroadcast(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	hub := NewWSHub(testLogger())

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	// Connect client
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer conn.Close()

	time.Sleep(50 * time.Millisecond)

	// Broadcast many messages rapidly
	numMessages := 1000
	var wg sync.WaitGroup
	wg.Add(1)

	var received int64
	go func() {
		defer wg.Done()
		for {
			conn.SetReadDeadline(time.Now().Add(2 * time.Second))
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

	// Wait for messages to be received
	time.Sleep(time.Second)
	conn.Close()
	wg.Wait()

	count := atomic.LoadInt64(&received)
	t.Logf("Received %d/%d messages (%.1f%%)", count, numMessages, float64(count)/float64(numMessages)*100)

	// Should receive most messages (may drop some if client is slow)
	if count < int64(numMessages/2) {
		t.Errorf("received too few messages: %d/%d", count, numMessages)
	}
}

func TestWSHub_SlowClient_DoesNotBlock(t *testing.T) {
	hub := NewWSHub(testLogger())

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	// Connect "slow" client that doesn't read
	slowConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer slowConn.Close()

	// Connect fast client
	fastConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer fastConn.Close()

	time.Sleep(50 * time.Millisecond)

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
	fastConn.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err = fastConn.ReadMessage()
	if err != nil {
		t.Errorf("fast client should receive messages: %v", err)
	}
}

func TestWSHub_ManyClientsConnect(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	hub := NewWSHub(testLogger())

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
		c.Close()
	}
	mu.Unlock()

	// Verify all clients disconnected
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
	hub := NewWSHub(testLogger())

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	// Connect clients
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

	// Original connections should be closed
	for i, conn := range conns {
		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, _, err := conn.ReadMessage()
		if err == nil {
			t.Errorf("client %d connection should be closed", i)
		}
	}
}

// =============================================================================
// Edge Cases
// =============================================================================

func TestWSHub_CloseOnce_NoPanic(t *testing.T) {
	hub := NewWSHub(testLogger())

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Close multiple times - should not panic
	conn.Close()
	conn.Close()
	conn.Close()

	time.Sleep(100 * time.Millisecond)

	// Hub should still be functional
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

	hub := NewWSHub(testLogger())

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Continuously connect/disconnect clients
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
				conn.Close()
			}
		}
	}()

	// Continuously broadcast
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
// Helper
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
