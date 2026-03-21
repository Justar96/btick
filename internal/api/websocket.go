package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/justar9/btick/internal/config"
	"github.com/justar9/btick/internal/domain"
)

// WSMessage is a message broadcast to WebSocket clients.
type WSMessage struct {
	Type         string   `json:"type"`
	Seq          uint64   `json:"seq,omitempty"`
	TS           string   `json:"ts"`
	Price        string   `json:"price,omitempty"`
	Basis        string   `json:"basis,omitempty"`
	IsStale      bool     `json:"is_stale,omitempty"`
	QualityScore string   `json:"quality_score,omitempty"`
	SourceCount  int      `json:"source_count,omitempty"`
	SourcesUsed  []string `json:"sources_used,omitempty"`
	Message      string   `json:"message,omitempty"`
}

// subscriptions tracks which message types a client wants.
type subscriptions struct {
	mu          sync.RWMutex
	snapshot1s  bool
	latestPrice bool
	heartbeat   bool
}

func newSubscriptions() *subscriptions {
	return &subscriptions{
		snapshot1s:  true,
		latestPrice: true,
		heartbeat:   true,
	}
}

func (s *subscriptions) wants(msgType string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	switch msgType {
	case "snapshot_1s":
		return s.snapshot1s
	case "latest_price":
		return s.latestPrice
	case "heartbeat":
		return s.heartbeat
	default:
		return true // welcome, unknown types always pass
	}
}

func (s *subscriptions) set(msgType string, val bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch msgType {
	case "snapshot_1s":
		s.snapshot1s = val
	case "latest_price":
		s.latestPrice = val
	case "heartbeat":
		s.heartbeat = val
	}
}

// clientAction is a message sent from client to server.
type clientAction struct {
	Action string   `json:"action"`
	Types  []string `json:"types"`
}

// WSHub manages WebSocket client connections and broadcast.
type WSHub struct {
	mu       sync.RWMutex
	clients  map[*wsClient]struct{}
	logger   *slog.Logger
	wsCfg    config.WSConfig
	seq      atomic.Uint64
	getState func() *domain.LatestState
	marshal  func(v any) ([]byte, error) // overridable for testing
}

type wsClient struct {
	conn      *websocket.Conn
	sendCh    chan []byte
	closeOnce sync.Once
	subs      *subscriptions
	dropCount atomic.Int64
	logger    *slog.Logger
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

func NewWSHub(logger *slog.Logger, wsCfg config.WSConfig, getState func() *domain.LatestState) *WSHub {
	return &WSHub{
		clients:  make(map[*wsClient]struct{}),
		logger:   logger.With("component", "ws_hub"),
		wsCfg:    wsCfg,
		getState: getState,
	}
}

func (h *WSHub) Run(ctx context.Context) {
	ticker := time.NewTicker(h.wsCfg.HeartbeatInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			h.shutdownClients()
			return
		case <-ticker.C:
			h.Broadcast(WSMessage{
				Type: "heartbeat",
				TS:   time.Now().UTC().Format(time.RFC3339Nano),
			})
		}
	}
}

func (h *WSHub) shutdownClients() {
	h.mu.Lock()
	for c := range h.clients {
		close(c.sendCh)
		_ = c.conn.Close()
	}
	h.clients = make(map[*wsClient]struct{})
	h.mu.Unlock()
}

func (h *WSHub) sendHandshake(conn *websocket.Conn, msg WSMessage) bool {
	data, err := json.Marshal(msg)
	if err != nil {
		return false
	}
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return false
	}
	return conn.WriteMessage(websocket.TextMessage, data) == nil
}

func (h *WSHub) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("ws upgrade failed", "error", err)
		return
	}

	client := &wsClient{
		conn:   conn,
		sendCh: make(chan []byte, h.wsCfg.SendBuffer()),
		subs:   newSubscriptions(),
		logger: h.logger,
	}

	h.mu.Lock()
	h.clients[client] = struct{}{}
	clientCount := len(h.clients)
	h.mu.Unlock()

	h.logger.Info("ws client connected", "total", clientCount)

	// Send welcome message before starting goroutines (no concurrent writer yet).
	welcome := WSMessage{
		Type:    "welcome",
		TS:      time.Now().UTC().Format(time.RFC3339Nano),
		Message: "btick/v1",
	}
	if !h.sendHandshake(conn, welcome) {
		h.logger.Warn("ws handshake failed: welcome")
		h.mu.Lock()
		delete(h.clients, client)
		h.mu.Unlock()
		_ = conn.Close()
		return
	}

	// Send initial state.
	if h.getState != nil {
		if state := h.getState(); state != nil {
			initMsg := WSMessage{
				Type:         "latest_price",
				TS:           state.TS.Format(time.RFC3339Nano),
				Price:        state.Price.String(),
				Basis:        state.Basis,
				IsStale:      state.IsStale,
				QualityScore: state.QualityScore.String(),
				SourceCount:  state.SourceCount,
				SourcesUsed:  state.SourcesUsed,
				Message:      "initial_state",
			}
			if !h.sendHandshake(conn, initMsg) {
				h.logger.Warn("ws handshake failed: initial_state")
				h.mu.Lock()
				delete(h.clients, client)
				h.mu.Unlock()
				_ = conn.Close()
				return
			}
		} else {
			noData := WSMessage{
				Type:    "latest_price",
				TS:      time.Now().UTC().Format(time.RFC3339Nano),
				Message: "no_data_yet",
			}
			if !h.sendHandshake(conn, noData) {
				h.logger.Warn("ws handshake failed: no_data_yet")
				h.mu.Lock()
				delete(h.clients, client)
				h.mu.Unlock()
				_ = conn.Close()
				return
			}
		}
	}

	// Writer goroutine
	go func() {
		defer func() {
			client.closeOnce.Do(func() {
				_ = conn.Close()
				h.mu.Lock()
				delete(h.clients, client)
				h.mu.Unlock()
			})
		}()

		pingTicker := time.NewTicker(h.wsCfg.PingInterval())
		defer pingTicker.Stop()

		for {
			select {
			case msg, ok := <-client.sendCh:
				if !ok {
					_ = conn.WriteMessage(websocket.CloseMessage, nil)
					return
				}
				if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
					return
				}
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					return
				}
			case <-pingTicker.C:
				if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
					return
				}
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}()

	// Reader goroutine — parses subscribe/unsubscribe actions
	go func() {
		defer func() {
			client.closeOnce.Do(func() {
				_ = conn.Close()
				h.mu.Lock()
				delete(h.clients, client)
				h.mu.Unlock()
			})
		}()

		conn.SetReadLimit(4096)
		if err := conn.SetReadDeadline(time.Now().Add(h.wsCfg.ReadDeadline())); err != nil {
			return
		}
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(h.wsCfg.ReadDeadline()))
		})

		for {
			_, rawMsg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var action clientAction
			if json.Unmarshal(rawMsg, &action) != nil {
				continue // silently ignore unparseable messages
			}
			switch action.Action {
			case "subscribe":
				for _, t := range action.Types {
					client.subs.set(t, true)
				}
			case "unsubscribe":
				for _, t := range action.Types {
					client.subs.set(t, false)
				}
			}
			// unknown actions silently ignored (forward-compatible)
		}
	}()
}

func (h *WSHub) Broadcast(msg WSMessage) {
	msg.Seq = h.seq.Add(1)

	marshalFn := json.Marshal
	if h.marshal != nil {
		marshalFn = h.marshal
	}
	data, err := marshalFn(msg)
	if err != nil {
		h.logger.Error("marshal ws message failed", "error", err)
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		if !c.subs.wants(msg.Type) {
			continue
		}
		select {
		case c.sendCh <- data:
		default:
			drops := c.dropCount.Add(1)
			if drops%100 == 1 {
				c.logger.Warn("ws client too slow, dropping message",
					"type", msg.Type,
					"total_drops", drops,
				)
			}
		}
	}
}
