package adapter

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// MessageHandler is called for each received WebSocket message.
type MessageHandler func(msgType int, data []byte, recvTS time.Time)

// BaseAdapter provides common WebSocket lifecycle management.
type BaseAdapter struct {
	name            string
	url             string
	pingInterval    time.Duration
	maxConnLifetime time.Duration
	onMessage       MessageHandler
	onConnected     func(conn *websocket.Conn) error // called after connect to send subscribe messages
	logger          *slog.Logger

	mu              sync.Mutex
	conn            *websocket.Conn
	connState       string
	lastMessageTS   time.Time
	reconnectCount  int
	consecutiveErrs int
	cancel          context.CancelFunc
}

// NewBaseAdapter creates a new adapter with reconnect/backoff behavior.
func NewBaseAdapter(name, url string, pingInterval, maxConnLifetime time.Duration, logger *slog.Logger) *BaseAdapter {
	return &BaseAdapter{
		name:            name,
		url:             url,
		pingInterval:    pingInterval,
		maxConnLifetime: maxConnLifetime,
		connState:       "disconnected",
		logger:          logger.With("source", name),
	}
}

func (a *BaseAdapter) SetMessageHandler(h MessageHandler)           { a.onMessage = h }
func (a *BaseAdapter) SetOnConnected(h func(*websocket.Conn) error) { a.onConnected = h }

func (a *BaseAdapter) Name() string { return a.name }

func (a *BaseAdapter) ConnState() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.connState
}

func (a *BaseAdapter) LastMessageTS() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastMessageTS
}

func (a *BaseAdapter) ReconnectCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reconnectCount
}

func (a *BaseAdapter) ConsecutiveErrors() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.consecutiveErrs
}

func (a *BaseAdapter) setConnState(state string) {
	a.mu.Lock()
	a.connState = state
	a.mu.Unlock()
}

// Run starts the adapter loop, reconnecting on failure. Blocks until ctx is cancelled.
func (a *BaseAdapter) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			a.setConnState("disconnected")
			return
		default:
		}

		err := a.connectAndRead(ctx)
		if err != nil && ctx.Err() == nil {
			a.mu.Lock()
			a.consecutiveErrs++
			errCount := a.consecutiveErrs
			a.mu.Unlock()

			backoff := calcBackoff(errCount)
			a.logger.Warn("connection lost, reconnecting",
				"error", err,
				"backoff", backoff,
				"reconnect_count", errCount,
			)

			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
		}
	}
}

func (a *BaseAdapter) connectAndRead(ctx context.Context) error {
	a.setConnState("connecting")
	a.logger.Info("connecting", "url", a.url)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.DialContext(ctx, a.url, http.Header{})
	if err != nil {
		a.setConnState("disconnected")
		return fmt.Errorf("dial: %w", err)
	}

	a.mu.Lock()
	a.conn = conn
	a.connState = "connected"
	a.consecutiveErrs = 0
	a.reconnectCount++
	a.mu.Unlock()

	a.logger.Info("connected")

	// Send subscribe messages
	if a.onConnected != nil {
		if err := a.onConnected(conn); err != nil {
			_ = conn.Close()
			a.setConnState("disconnected")
			return fmt.Errorf("subscribe: %w", err)
		}
	}

	// Set up pong handler
	conn.SetPongHandler(func(appData string) error {
		a.mu.Lock()
		a.lastMessageTS = time.Now()
		a.mu.Unlock()
		return nil
	})

	// Context for this connection
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	a.mu.Lock()
	a.cancel = connCancel
	a.mu.Unlock()

	go func() {
		<-connCtx.Done()
		_ = conn.Close()
	}()

	// Ping ticker
	if a.pingInterval > 0 {
		go a.pingLoop(connCtx, conn)
	}

	// Max connection lifetime
	if a.maxConnLifetime > 0 {
		go func() {
			select {
			case <-time.After(a.maxConnLifetime):
				a.logger.Info("max connection lifetime reached, closing")
				_ = conn.Close()
			case <-connCtx.Done():
			}
		}()
	}

	// Read loop
	const readTimeout = 60 * time.Second
	const readDeadlineRefresh = 15 * time.Second
	nextDeadlineRefresh := time.Time{}
	for {
		now := time.Now()
		if nextDeadlineRefresh.IsZero() || now.After(nextDeadlineRefresh) {
			if err := conn.SetReadDeadline(now.Add(readTimeout)); err != nil {
				_ = conn.Close()
				a.setConnState("disconnected")
				return fmt.Errorf("set read deadline: %w", err)
			}
			nextDeadlineRefresh = now.Add(readDeadlineRefresh)
		}

		msgType, data, err := conn.ReadMessage()
		recvTS := time.Now()

		if err != nil {
			_ = conn.Close()
			a.setConnState("disconnected")
			if ctx.Err() != nil || connCtx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read: %w", err)
		}

		a.mu.Lock()
		a.lastMessageTS = recvTS
		a.mu.Unlock()

		if a.onMessage != nil {
			a.onMessage(msgType, data, recvTS)
		}
	}
}

func (a *BaseAdapter) pingLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(a.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := conn.WriteControl(
				websocket.PingMessage,
				nil,
				time.Now().Add(5*time.Second),
			); err != nil {
				a.logger.Warn("ping failed", "error", err)
				return
			}
		}
	}
}

func calcBackoff(attempt int) time.Duration {
	base := time.Second
	maxDur := 30 * time.Second

	shift := attempt
	if shift > 5 {
		shift = 5
	}
	delay := base * time.Duration(1<<shift)
	if delay > maxDur {
		delay = maxDur
	}
	// Add jitter: 0.5x to 1.5x
	jitter := 0.5 + rand.Float64()
	return time.Duration(float64(delay) * jitter)
}
