package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/justar9/btick/internal/config"
	"github.com/justar9/btick/internal/domain"
	"github.com/justar9/btick/internal/metrics"
)

// WSMessage is a message broadcast to WebSocket clients.
type WSMessage struct {
	Type          string          `json:"type"`
	Seq           uint64          `json:"seq,omitempty"`
	Symbol        string          `json:"symbol,omitempty"`
	TS            string          `json:"ts"`
	Price         string          `json:"price,omitempty"`
	Basis         string          `json:"basis,omitempty"`
	IsStale       bool            `json:"is_stale,omitempty"`
	IsDegraded    bool            `json:"is_degraded,omitempty"`
	QualityScore  string          `json:"quality_score,omitempty"`
	SourceCount   int             `json:"source_count,omitempty"`
	SourcesUsed   []string        `json:"sources_used,omitempty"`
	Message       string          `json:"message,omitempty"`
	Source        string          `json:"source,omitempty"`
	ConnState     string          `json:"conn_state,omitempty"`
	Stale         bool            `json:"stale,omitempty"`
	SourceDetails json.RawMessage `json:"source_details,omitempty"`
	LatencyMs     int64           `json:"latency_ms,omitempty"`
}

// subscriptions tracks which message types a client wants.
type subscriptions struct {
	mu           sync.RWMutex
	snapshot1s   bool
	latestPrice  bool
	heartbeat    bool
	sourcePrice  bool
	sourceStatus bool
	symbols      map[string]struct{}
}

func parseRequestedSymbols(values []string, allowedSymbols []string) ([]string, bool) {
	if len(values) == 0 {
		return nil, false
	}

	allowed := make(map[string]struct{}, len(allowedSymbols))
	for _, symbol := range allowedSymbols {
		normalized := normalizeSymbol(symbol)
		if normalized == "" {
			continue
		}
		allowed[normalized] = struct{}{}
	}

	seen := make(map[string]struct{}, len(values))
	selected := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			normalized := normalizeSymbol(part)
			if normalized == "" {
				continue
			}
			if _, ok := allowed[normalized]; !ok {
				continue
			}
			if _, ok := seen[normalized]; ok {
				continue
			}
			seen[normalized] = struct{}{}
			selected = append(selected, normalized)
		}
	}

	return selected, true
}

func newSubscriptions() *subscriptions {
	return &subscriptions{
		snapshot1s:  true,
		latestPrice: true,
		heartbeat:   true,
	}
}

func normalizeSymbol(symbol string) string {
	return strings.ToUpper(strings.TrimSpace(symbol))
}

func (s *subscriptions) wants(msgType string, symbol ...string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	allowedType := false
	switch msgType {
	case "snapshot_1s":
		allowedType = s.snapshot1s
	case "latest_price":
		allowedType = s.latestPrice
	case "heartbeat":
		allowedType = s.heartbeat
	case "source_price":
		allowedType = s.sourcePrice
	case "source_status":
		allowedType = s.sourceStatus
	default:
		allowedType = true // welcome, unknown types always pass
	}

	if !allowedType {
		return false
	}

	selectedSymbol := ""
	if len(symbol) > 0 {
		selectedSymbol = symbol[0]
	}

	if selectedSymbol == "" || s.symbols == nil {
		return true
	}

	_, ok := s.symbols[normalizeSymbol(selectedSymbol)]
	return ok
}

func (s *subscriptions) effectiveSymbols(allSymbols []string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	selected := make([]string, 0, len(allSymbols))
	if s.symbols == nil {
		for _, symbol := range allSymbols {
			normalized := normalizeSymbol(symbol)
			if normalized == "" {
				continue
			}
			selected = append(selected, normalized)
		}
		return selected
	}

	for _, symbol := range allSymbols {
		normalized := normalizeSymbol(symbol)
		if normalized == "" {
			continue
		}
		if _, ok := s.symbols[normalized]; ok {
			selected = append(selected, normalized)
		}
	}

	return selected
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
	case "source_price":
		s.sourcePrice = val
	case "source_status":
		s.sourceStatus = val
	}
}

func (s *subscriptions) subscribeSymbols(symbols []string) {
	if len(symbols) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.symbols == nil {
		s.symbols = make(map[string]struct{}, len(symbols))
	}

	for _, symbol := range symbols {
		normalized := normalizeSymbol(symbol)
		if normalized == "" {
			continue
		}
		s.symbols[normalized] = struct{}{}
	}
}

func (s *subscriptions) setSymbols(symbols []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.symbols = make(map[string]struct{}, len(symbols))
	for _, symbol := range symbols {
		normalized := normalizeSymbol(symbol)
		if normalized == "" {
			continue
		}
		s.symbols[normalized] = struct{}{}
	}
}

func (s *subscriptions) unsubscribeSymbols(symbols []string, allSymbols []string) {
	if len(symbols) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.symbols == nil {
		s.symbols = make(map[string]struct{}, len(allSymbols))
		for _, symbol := range allSymbols {
			normalized := normalizeSymbol(symbol)
			if normalized == "" {
				continue
			}
			s.symbols[normalized] = struct{}{}
		}
	}

	for _, symbol := range symbols {
		delete(s.symbols, normalizeSymbol(symbol))
	}
}

// clientAction is a message sent from client to server.
type clientAction struct {
	Action  string   `json:"action"`
	Types   []string `json:"types"`
	Symbols []string `json:"symbols,omitempty"`
}

// WSHub manages WebSocket client connections and broadcast.
type WSHub struct {
	mu       sync.RWMutex
	clients  map[*wsClient]struct{}
	logger   *slog.Logger
	wsCfg    config.WSConfig
	seq      atomic.Uint64
	getState func(symbol string) *domain.LatestState
	symbols  []string
	marshal  func(v any) ([]byte, error) // overridable for testing
}

type wsClient struct {
	conn        *websocket.Conn
	sendCh      chan []byte
	closeOnce   sync.Once
	subs        *subscriptions
	dropCount   atomic.Int64
	logger      *slog.Logger
	connectedAt time.Time
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

func NewWSHub(logger *slog.Logger, wsCfg config.WSConfig, getState func(string) *domain.LatestState, symbols []string) *WSHub {
	return &WSHub{
		clients:  make(map[*wsClient]struct{}),
		logger:   logger.With("component", "ws_hub"),
		wsCfg:    wsCfg,
		getState: getState,
		symbols:  symbols,
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
	h.refreshClientMetrics()
}

func (h *WSHub) refreshClientMetrics() {
	h.mu.RLock()
	clientCount := len(h.clients)
	symbolSubscribers := make(map[string]int, len(h.symbols))
	for client := range h.clients {
		for _, symbol := range client.subs.effectiveSymbols(h.symbols) {
			symbolSubscribers[symbol]++
		}
	}
	h.mu.RUnlock()

	metrics.SetWSClients(clientCount)
	metrics.SetWSSymbolSubscribers(symbolSubscribers)
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
	requestedSymbols, hasRequestedSymbols := parseRequestedSymbols(r.URL.Query()["symbols"], h.symbols)

	// Early check before upgrading (avoids wasting an upgrade on a full hub).
	h.mu.RLock()
	currentCount := len(h.clients)
	h.mu.RUnlock()

	if currentCount >= h.wsCfg.MaxClientCount() {
		h.logger.Warn("ws connection rejected: max clients reached", "max", h.wsCfg.MaxClientCount())
		metrics.IncWSRejected()
		http.Error(w, `{"error":"too many connections"}`, http.StatusServiceUnavailable)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("ws upgrade failed", "error", err)
		return
	}

	client := &wsClient{
		conn:        conn,
		sendCh:      make(chan []byte, h.wsCfg.SendBuffer()),
		subs:        newSubscriptions(),
		logger:      h.logger,
		connectedAt: time.Now().UTC(),
	}
	if hasRequestedSymbols {
		client.subs.setSymbols(requestedSymbols)
	}

	// Atomically check limit and add under the same lock to prevent overcount.
	h.mu.Lock()
	if len(h.clients) >= h.wsCfg.MaxClientCount() {
		h.mu.Unlock()
		h.logger.Warn("ws connection rejected: max clients reached (post-upgrade)", "max", h.wsCfg.MaxClientCount())
		metrics.IncWSRejected()
		_ = conn.Close()
		return
	}
	h.clients[client] = struct{}{}
	h.mu.Unlock()
	h.refreshClientMetrics()

	h.logger.Info("ws client connected", "total", h.ClientCount())

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
		h.refreshClientMetrics()
		_ = conn.Close()
		return
	}

	// Send initial state for each configured symbol.
	if h.getState != nil {
		sentAny := false
		for _, sym := range h.symbols {
			if !client.subs.wants("latest_price", sym) {
				continue
			}
			state := h.getState(sym)
			if state == nil {
				continue
			}
			sentAny = true
			initMsg := WSMessage{
				Type:         "latest_price",
				Symbol:       state.Symbol,
				TS:           state.TS.Format(time.RFC3339Nano),
				Price:        state.Price.String(),
				Basis:        state.Basis,
				IsStale:      state.IsStale,
				IsDegraded:   state.IsDegraded,
				QualityScore: state.QualityScore.String(),
				SourceCount:  state.SourceCount,
				SourcesUsed:  state.SourcesUsed,
				Message:      "initial_state",
			}
			if !h.sendHandshake(conn, initMsg) {
				h.logger.Warn("ws handshake failed: initial_state", "symbol", sym)
				h.mu.Lock()
				delete(h.clients, client)
				h.mu.Unlock()
				h.refreshClientMetrics()
				_ = conn.Close()
				return
			}
		}
		if !sentAny {
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
				h.refreshClientMetrics()
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
				h.refreshClientMetrics()
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
				h.refreshClientMetrics()
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
				client.subs.subscribeSymbols(action.Symbols)
			case "unsubscribe":
				for _, t := range action.Types {
					client.subs.set(t, false)
				}
				client.subs.unsubscribeSymbols(action.Symbols, h.symbols)
			}
			h.refreshClientMetrics()
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

	maxDrops := int64(h.wsCfg.SlowClientMaxDropCount())
	broadcastStart := time.Now()

	metrics.IncWSBroadcast()

	h.mu.RLock()
	var evict []*wsClient
	delivered := 0
	for c := range h.clients {
		if !c.subs.wants(msg.Type, msg.Symbol) {
			continue
		}
		select {
		case c.sendCh <- data:
			delivered++
		default:
			drops := c.dropCount.Add(1)
			metrics.IncWSDrop()
			metrics.AddWSDropDetail(msg.Type, msg.Symbol, 1)
			if drops >= maxDrops {
				metrics.AddWSEvictionDetail(msg.Type, msg.Symbol, 1)
				evict = append(evict, c)
			} else if drops%100 == 1 {
				c.logger.Warn("ws client too slow, dropping message",
					"type", msg.Type,
					"total_drops", drops,
				)
			}
		}
	}
	h.mu.RUnlock()
	metrics.AddWSFanout(msg.Type, delivered)
	metrics.AddWSSymbolFanout(msg.Type, msg.Symbol, delivered)

	metrics.ObserveWSBroadcastDuration(time.Since(broadcastStart))

	// Evict slow clients outside the read lock.
	if len(evict) > 0 {
		h.evictClients(evict)
	}
}

// evictClients forcefully disconnects slow clients that exceeded the drop threshold.
func (h *WSHub) evictClients(clients []*wsClient) {
	h.mu.Lock()
	for _, c := range clients {
		if _, ok := h.clients[c]; !ok {
			continue // already removed
		}
		delete(h.clients, c)
		c.logger.Warn("evicting slow ws client",
			"total_drops", c.dropCount.Load(),
			"connected_for", time.Since(c.connectedAt).Round(time.Second),
		)
		close(c.sendCh)
		if c.conn != nil {
			_ = c.conn.Close()
		}
		metrics.IncWSEvicted()
	}
	h.mu.Unlock()
	h.refreshClientMetrics()
}

// ClientCount returns the current number of connected clients.
func (h *WSHub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
