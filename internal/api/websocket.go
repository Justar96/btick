package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WSMessage is a message broadcast to WebSocket clients.
type WSMessage struct {
	Type         string   `json:"type"`
	TS           string   `json:"ts"`
	Price        string   `json:"price,omitempty"`
	Basis        string   `json:"basis,omitempty"`
	IsStale      bool     `json:"is_stale,omitempty"`
	QualityScore string   `json:"quality_score,omitempty"`
	SourceCount  int      `json:"source_count,omitempty"`
	SourcesUsed  []string `json:"sources_used,omitempty"`
	Message      string   `json:"message,omitempty"`
}

// WSHub manages WebSocket client connections and broadcast.
type WSHub struct {
	mu      sync.RWMutex
	clients map[*wsClient]struct{}
	logger  *slog.Logger
}

type wsClient struct {
	conn      *websocket.Conn
	sendCh    chan []byte
	closeOnce sync.Once
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

func NewWSHub(logger *slog.Logger) *WSHub {
	return &WSHub{
		clients: make(map[*wsClient]struct{}),
		logger:  logger.With("component", "ws_hub"),
	}
}

func (h *WSHub) Run(ctx context.Context) {
	<-ctx.Done()
	h.mu.Lock()
	for c := range h.clients {
		close(c.sendCh)
		c.conn.Close()
	}
	h.clients = make(map[*wsClient]struct{})
	h.mu.Unlock()
}

func (h *WSHub) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("ws upgrade failed", "error", err)
		return
	}

	client := &wsClient{
		conn:   conn,
		sendCh: make(chan []byte, 256),
	}

	h.mu.Lock()
	h.clients[client] = struct{}{}
	clientCount := len(h.clients)
	h.mu.Unlock()

	h.logger.Info("ws client connected", "total", clientCount)

	// Writer goroutine
	go func() {
		defer func() {
			client.closeOnce.Do(func() {
				conn.Close()
				h.mu.Lock()
				delete(h.clients, client)
				h.mu.Unlock()
			})
		}()

		pingTicker := time.NewTicker(30 * time.Second)
		defer pingTicker.Stop()

		for {
			select {
			case msg, ok := <-client.sendCh:
				if !ok {
					conn.WriteMessage(websocket.CloseMessage, nil)
					return
				}
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					return
				}
			case <-pingTicker.C:
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}()

	// Reader goroutine (drain incoming messages)
	go func() {
		defer func() {
			client.closeOnce.Do(func() {
				conn.Close()
				h.mu.Lock()
				delete(h.clients, client)
				h.mu.Unlock()
			})
		}()

		conn.SetReadLimit(4096)
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})

		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}()
}

func (h *WSHub) Broadcast(msg WSMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		h.logger.Error("marshal ws message failed", "error", err)
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		select {
		case c.sendCh <- data:
		default:
			// Client too slow, skip
		}
	}
}
