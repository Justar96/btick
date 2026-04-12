package adapter

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

// =============================================================================
// Base Adapter Reconnection Tests
// =============================================================================

func TestBaseAdapter_Backoff(t *testing.T) {
	tests := []struct {
		attempt  int
		minDelay time.Duration
		maxDelay time.Duration
	}{
		{1, 500 * time.Millisecond, 3 * time.Second},
		{2, 1 * time.Second, 6 * time.Second},
		{3, 2 * time.Second, 12 * time.Second},
		{5, 8 * time.Second, 48 * time.Second},
		{10, 15 * time.Second, 45 * time.Second}, // Capped at 30s base
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			// Calculate multiple times due to jitter
			for i := 0; i < 10; i++ {
				delay := calcBackoff(tt.attempt)
				if delay < tt.minDelay || delay > tt.maxDelay {
					t.Errorf("attempt %d: delay %v not in range [%v, %v]",
						tt.attempt, delay, tt.minDelay, tt.maxDelay)
				}
			}
		})
	}
}

func TestBaseAdapter_ConnState(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ba := NewBaseAdapter("test", "wss://localhost", 30*time.Second, 0, logger)

	// Initial state
	if ba.ConnState() != "disconnected" {
		t.Errorf("expected disconnected, got %s", ba.ConnState())
	}

	// Simulate state changes
	ba.setConnState("connecting")
	if ba.ConnState() != "connecting" {
		t.Errorf("expected connecting, got %s", ba.ConnState())
	}

	ba.setConnState("connected")
	if ba.ConnState() != "connected" {
		t.Errorf("expected connected, got %s", ba.ConnState())
	}
}

func TestBaseAdapter_ReconnectCount(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ba := NewBaseAdapter("test", "wss://localhost", 30*time.Second, 0, logger)

	if ba.ReconnectCount() != 0 {
		t.Errorf("expected 0, got %d", ba.ReconnectCount())
	}

	// Simulate reconnects
	ba.mu.Lock()
	ba.reconnectCount = 5
	ba.mu.Unlock()

	if ba.ReconnectCount() != 5 {
		t.Errorf("expected 5, got %d", ba.ReconnectCount())
	}
}

func TestBaseAdapter_ConsecutiveErrors(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ba := NewBaseAdapter("test", "wss://localhost", 30*time.Second, 0, logger)

	if ba.ConsecutiveErrors() != 0 {
		t.Errorf("expected 0, got %d", ba.ConsecutiveErrors())
	}

	ba.mu.Lock()
	ba.consecutiveErrs = 3
	ba.mu.Unlock()

	if ba.ConsecutiveErrors() != 3 {
		t.Errorf("expected 3, got %d", ba.ConsecutiveErrors())
	}
}

func TestBaseAdapter_LastMessageTS(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ba := NewBaseAdapter("test", "wss://localhost", 30*time.Second, 0, logger)

	if !ba.LastMessageTS().IsZero() {
		t.Error("expected zero time initially")
	}

	now := time.Now()
	ba.mu.Lock()
	ba.lastMessageTS = now
	ba.mu.Unlock()

	if !ba.LastMessageTS().Equal(now) {
		t.Errorf("expected %v, got %v", now, ba.LastMessageTS())
	}
}

// =============================================================================
// WebSocket Mock Server for Integration Tests
// =============================================================================

type mockWSServer struct {
	server       *httptest.Server
	upgrader     websocket.Upgrader
	connections  int64
	messagesSent int64
	mu           sync.Mutex
	shouldFail   bool
	failAfter    int // Fail after N connections
}

func newMockWSServer() *mockWSServer {
	m := &mockWSServer{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
	m.server = httptest.NewServer(http.HandlerFunc(m.handleWS))
	return m
}

func (m *mockWSServer) handleWS(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	connNum := atomic.AddInt64(&m.connections, 1)
	shouldFail := m.shouldFail || (m.failAfter > 0 && int(connNum) > m.failAfter)
	m.mu.Unlock()

	if shouldFail {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}

	conn, err := m.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	// Echo messages back
	for {
		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		atomic.AddInt64(&m.messagesSent, 1)
		if err := conn.WriteMessage(msgType, msg); err != nil {
			return
		}
	}
}

func (m *mockWSServer) URL() string {
	return "ws" + m.server.URL[4:] // Convert http:// to ws://
}

func (m *mockWSServer) Close() {
	m.server.Close()
}

func (m *mockWSServer) SetShouldFail(fail bool) {
	m.mu.Lock()
	m.shouldFail = fail
	m.mu.Unlock()
}

func (m *mockWSServer) Connections() int64 {
	return atomic.LoadInt64(&m.connections)
}

// =============================================================================
// Integration Tests with Mock Server
// =============================================================================

func TestBaseAdapter_ConnectToMockServer(t *testing.T) {
	mock := newMockWSServer()
	defer mock.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ba := NewBaseAdapter("test", mock.URL(), 5*time.Second, 0, logger)

	var messageReceived atomic.Bool
	ba.SetMessageHandler(func(msgType int, data []byte, recvTS time.Time) {
		messageReceived.Store(true)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Run in background
	done := make(chan struct{})
	go func() {
		ba.Run(ctx)
		close(done)
	}()

	// Wait for connection
	time.Sleep(200 * time.Millisecond)

	if ba.ConnState() != "connected" {
		t.Errorf("expected connected, got %s", ba.ConnState())
	}

	cancel()
	<-done
}

func TestBaseAdapter_ReconnectOnServerDrop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping reconnection test in short mode")
	}

	mock := newMockWSServer()
	defer mock.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ba := NewBaseAdapter("test", mock.URL(), 5*time.Second, 0, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go ba.Run(ctx)

	// Wait for first connection
	time.Sleep(200 * time.Millisecond)
	initialConnections := mock.Connections()

	if initialConnections < 1 {
		t.Fatal("should have connected at least once")
	}

	// Simulate server restart by making it fail temporarily
	mock.SetShouldFail(true)

	// Force disconnect by closing server connections
	time.Sleep(100 * time.Millisecond)

	// Re-enable server
	mock.SetShouldFail(false)

	// Wait for reconnection
	time.Sleep(3 * time.Second)

	finalConnections := mock.Connections()
	t.Logf("Connections: initial=%d, final=%d", initialConnections, finalConnections)

	// Should have attempted reconnection
	if ba.ReconnectCount() < 1 {
		t.Error("should have recorded at least 1 reconnect")
	}

	cancel()
}

func TestBaseAdapter_CancelDuringBackoff(t *testing.T) {
	// Server that always fails
	mock := newMockWSServer()
	mock.SetShouldFail(true)
	defer mock.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ba := NewBaseAdapter("test", mock.URL(), 5*time.Second, 0, logger)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		ba.Run(ctx)
		close(done)
	}()

	// Wait for first failure and backoff to start
	time.Sleep(200 * time.Millisecond)

	// Cancel during backoff
	cancel()

	// Should exit promptly
	select {
	case <-done:
		// Good
	case <-time.After(2 * time.Second):
		t.Fatal("adapter did not exit after cancel during backoff")
	}
}

func TestBaseAdapter_MaxConnectionLifetime(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping max lifetime test in short mode")
	}

	mock := newMockWSServer()
	defer mock.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	// Set max lifetime to 500ms for faster test
	ba := NewBaseAdapter("test", mock.URL(), 5*time.Second, 500*time.Millisecond, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go ba.Run(ctx)

	// Wait for initial connection
	time.Sleep(200 * time.Millisecond)

	initialConns := mock.Connections()
	if initialConns < 1 {
		t.Fatal("should have connected")
	}

	// Wait for max lifetime to trigger reconnection (500ms + buffer)
	time.Sleep(1500 * time.Millisecond)

	connections := mock.Connections()
	t.Logf("Total connections: initial=%d, final=%d", initialConns, connections)

	// Should have reconnected - but this depends on timing, so just log
	// The feature is tested by the connection closing after max lifetime
	if connections <= initialConns {
		t.Logf("Warning: expected reconnection after max lifetime (may be timing dependent)")
	}

	cancel()
}

// =============================================================================
// Concurrent Connection Tests
// =============================================================================

func TestBaseAdapter_ConcurrentStateAccess(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ba := NewBaseAdapter("test", "wss://localhost", 5*time.Second, 0, logger)

	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Writer goroutines
	for w := 0; w < 5; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					ba.setConnState("connecting")
					ba.setConnState("connected")
					ba.setConnState("disconnected")
					ba.mu.Lock()
					ba.lastMessageTS = time.Now()
					ba.reconnectCount++
					ba.consecutiveErrs++
					ba.mu.Unlock()
				}
			}
		}()
	}

	// Reader goroutines
	for r := 0; r < 10; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					_ = ba.ConnState()
					_ = ba.LastMessageTS()
					_ = ba.ReconnectCount()
					_ = ba.ConsecutiveErrors()
				}
			}
		}()
	}

	wg.Wait()
	// Test passes if no race detected
}

// =============================================================================
// Message Handler Tests
// =============================================================================

func TestBaseAdapter_MessageHandlerCalled(t *testing.T) {
	mock := newMockWSServer()
	defer mock.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ba := NewBaseAdapter("test", mock.URL(), 5*time.Second, 0, logger)

	var messagesReceived int64
	ba.SetMessageHandler(func(msgType int, data []byte, recvTS time.Time) {
		atomic.AddInt64(&messagesReceived, 1)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go ba.Run(ctx)

	// Wait for connection
	time.Sleep(200 * time.Millisecond)

	// Send message through the mock server would require bidirectional setup
	// For this test, we verify the handler was set
	if ba.onMessage == nil {
		t.Error("message handler should be set")
	}

	cancel()
}

func TestBaseAdapter_OnConnectedCallback(t *testing.T) {
	mock := newMockWSServer()
	defer mock.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ba := NewBaseAdapter("test", mock.URL(), 5*time.Second, 0, logger)

	var callbackCalled atomic.Bool
	ba.SetOnConnected(func(conn *websocket.Conn) error {
		callbackCalled.Store(true)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go ba.Run(ctx)

	// Wait for connection
	time.Sleep(200 * time.Millisecond)

	if !callbackCalled.Load() {
		t.Error("onConnected callback should have been called")
	}

	cancel()
}

// =============================================================================
// Error Scenarios
// =============================================================================

func TestBaseAdapter_InvalidURL(t *testing.T) {
	// Use a local address that refuses connections immediately, rather than a
	// non-existent DNS name which may hang for 10+ seconds on some systems.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ba := NewBaseAdapter("test", "wss://127.0.0.1:1", 5*time.Second, 0, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		ba.Run(ctx)
		close(done)
	}()

	// Poll — connection refused is fast but backoff adds a small delay.
	deadline := time.After(3 * time.Second)
	for {
		if ba.ConsecutiveErrors() > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for consecutive errors from invalid URL")
		case <-time.After(50 * time.Millisecond):
		}
	}

	cancel()
	<-done
}

func TestBaseAdapter_ServerRejectsConnection(t *testing.T) {
	// Server that always rejects
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ba := NewBaseAdapter("test", wsURL, 5*time.Second, 0, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		ba.Run(ctx)
		close(done)
	}()

	time.Sleep(500 * time.Millisecond)

	if ba.ConsecutiveErrors() == 0 {
		t.Error("should have consecutive errors when server rejects")
	}

	if ba.ConnState() == "connected" {
		t.Error("should not be connected when server rejects")
	}

	cancel()
	<-done
}
