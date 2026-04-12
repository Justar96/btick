package api

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/justar9/btick/internal/config"
	"github.com/justar9/btick/internal/domain"
	"github.com/justar9/btick/internal/metrics"
)

type wsLoadScenario struct {
	name            string
	clients         int
	slowClients     int
	filteredClients int
	messages        int
	sendBuffer      int
	maxDrops        int
	pauseEvery      int
	pauseDuration   time.Duration
	broadcastSymbol string
	filterSymbol    string
	allowedSymbols  []string
}

type wsLoadResult struct {
	clientsGauge           float64
	broadcasts             float64
	fanout                 float64
	symbolFanout           float64
	typeDrops              float64
	symbolDrops            float64
	typeEvictions          float64
	symbolEvictions        float64
	broadcastDurationCount float64
	broadcastDurationSum   float64
	subscribersBySymbol    map[string]float64
}

type wsMetricSnapshot map[string]float64

func TestWebSocketLoadScenarios(t *testing.T) {
	if os.Getenv("RUN_WS_LOAD") != "1" {
		t.Skip("set RUN_WS_LOAD=1 to run websocket load scenarios")
	}

	scenarios := []wsLoadScenario{
		{
			name:            "all_fast",
			clients:         120,
			slowClients:     0,
			filteredClients: 0,
			messages:        500,
			sendBuffer:      256,
			maxDrops:        500,
			pauseEvery:      25,
			pauseDuration:   time.Millisecond,
			broadcastSymbol: "BTC/USD",
			filterSymbol:    "ETH/USD",
			allowedSymbols:  []string{"BTC/USD", "ETH/USD"},
		},
		{
			name:            "filtered_half",
			clients:         120,
			slowClients:     0,
			filteredClients: 60,
			messages:        500,
			sendBuffer:      256,
			maxDrops:        500,
			pauseEvery:      25,
			pauseDuration:   time.Millisecond,
			broadcastSymbol: "BTC/USD",
			filterSymbol:    "ETH/USD",
			allowedSymbols:  []string{"BTC/USD", "ETH/USD"},
		},
		{
			name:            "slow_mix",
			clients:         60,
			slowClients:     10,
			filteredClients: 0,
			messages:        220,
			sendBuffer:      128,
			maxDrops:        20,
			pauseEvery:      0,
			pauseDuration:   0,
			broadcastSymbol: "BTC/USD",
			filterSymbol:    "ETH/USD",
			allowedSymbols:  []string{"BTC/USD", "ETH/USD"},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			result := runWSLoadScenario(t, scenario)
			assertWSLoadScenario(t, scenario, result)
			logWSLoadScenario(t, scenario, result)
		})
	}
}

func runWSLoadScenario(t *testing.T, scenario wsLoadScenario) wsLoadResult {
	t.Helper()

	hub := NewWSHub(testLogger(), config.WSConfig{
		SendBufferSize:     scenario.sendBuffer,
		SlowClientMaxDrops: scenario.maxDrops,
	}, func(_ string) *domain.LatestState { return nil }, scenario.allowedSymbols)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/price", hub.HandleWS)
	mux.Handle("GET /metrics", metrics.Handler())
	server := httptest.NewServer(mux)
	defer server.Close()

	baseWSURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/price"
	metricsURL := server.URL + "/metrics"

	before := scrapeWSMetricSnapshot(t, metricsURL)

	type fastReader struct {
		conn *websocket.Conn
		done chan struct{}
	}

	fastReaders := make([]fastReader, 0, scenario.clients-scenario.slowClients)
	slowConns := make([]*websocket.Conn, 0, scenario.slowClients)
	allConns := make([]*websocket.Conn, 0, scenario.clients)

	for i := 0; i < scenario.clients; i++ {
		wsURL := baseWSURL
		isFiltered := i >= scenario.slowClients && i < scenario.slowClients+scenario.filteredClients
		if isFiltered {
			wsURL += "?symbols=" + strings.ReplaceAll(scenario.filterSymbol, "/", "%2F")
		}

		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("dial client %d: %v", i, err)
		}
		allConns = append(allConns, conn)

		if i < scenario.slowClients {
			slowConns = append(slowConns, conn)
			continue
		}

		drainMessages(t, conn, 2)
		done := make(chan struct{})
		fastReaders = append(fastReaders, fastReader{conn: conn, done: done})
		go func(conn *websocket.Conn, done chan struct{}) {
			defer close(done)
			for {
				if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
					return
				}
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}(conn, done)
	}

	defer func() {
		for _, conn := range allConns {
			_ = conn.Close()
		}
		for _, reader := range fastReaders {
			select {
			case <-reader.done:
			case <-time.After(2 * time.Second):
			}
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hub.ClientCount() == scenario.clients {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if hub.ClientCount() != scenario.clients {
		t.Fatalf("expected %d clients connected, got %d", scenario.clients, hub.ClientCount())
	}

	for i := 0; i < scenario.messages; i++ {
		hub.Broadcast(WSMessage{
			Type:         "latest_price",
			Symbol:       scenario.broadcastSymbol,
			TS:           time.Now().UTC().Format(time.RFC3339Nano),
			Price:        strconv.Itoa(84_000 + i%250),
			Basis:        "median_trade",
			QualityScore: "0.95",
			SourceCount:  4,
			SourcesUsed:  []string{"binance", "coinbase", "kraken", "okx"},
		})
		if scenario.pauseEvery > 0 && (i+1)%scenario.pauseEvery == 0 {
			time.Sleep(scenario.pauseDuration)
		}
	}

	time.Sleep(200 * time.Millisecond)
	after := scrapeWSMetricSnapshot(t, metricsURL)

	result := wsLoadResult{
		clientsGauge:           metricValue(after, "btick_ws_clients"),
		broadcasts:             metricDiff(before, after, `btick_ws_broadcasts_total`),
		fanout:                 metricDiff(before, after, `btick_ws_fanout_total{type="latest_price"}`),
		symbolFanout:           metricDiff(before, after, `btick_ws_symbol_fanout_total{type="latest_price",symbol="`+scenario.broadcastSymbol+`"}`),
		typeDrops:              metricDiff(before, after, `btick_ws_type_drops_total{type="latest_price"}`),
		symbolDrops:            metricDiff(before, after, `btick_ws_symbol_drops_total{type="latest_price",symbol="`+scenario.broadcastSymbol+`"}`),
		typeEvictions:          metricDiff(before, after, `btick_ws_type_evictions_total{type="latest_price"}`),
		symbolEvictions:        metricDiff(before, after, `btick_ws_symbol_evictions_total{type="latest_price",symbol="`+scenario.broadcastSymbol+`"}`),
		broadcastDurationCount: metricDiff(before, after, `btick_ws_broadcast_duration_seconds_count`),
		broadcastDurationSum:   metricDiff(before, after, `btick_ws_broadcast_duration_seconds_sum`),
		subscribersBySymbol: map[string]float64{
			scenario.broadcastSymbol: metricValue(after, `btick_ws_symbol_subscribers{symbol="`+scenario.broadcastSymbol+`"}`),
			scenario.filterSymbol:    metricValue(after, `btick_ws_symbol_subscribers{symbol="`+scenario.filterSymbol+`"}`),
		},
	}

	return result
}

func assertWSLoadScenario(t *testing.T, scenario wsLoadScenario, result wsLoadResult) {
	t.Helper()

	if result.broadcasts != float64(scenario.messages) {
		t.Fatalf("expected %d broadcasts, got %.0f", scenario.messages, result.broadcasts)
	}
	if result.symbolFanout != result.fanout {
		t.Fatalf("expected symbol fanout %.0f to match type fanout %.0f", result.symbolFanout, result.fanout)
	}

	switch scenario.name {
	case "all_fast":
		expectedFanout := float64(scenario.clients * scenario.messages)
		if result.fanout != expectedFanout {
			t.Fatalf("expected fanout %.0f, got %.0f", expectedFanout, result.fanout)
		}
		if result.typeDrops != 0 || result.typeEvictions != 0 {
			t.Fatalf("expected no drops or evictions, got drops=%.0f evictions=%.0f", result.typeDrops, result.typeEvictions)
		}
		if result.subscribersBySymbol[scenario.broadcastSymbol] != float64(scenario.clients) {
			t.Fatalf("expected %.0f subscribers on %s, got %.0f", float64(scenario.clients), scenario.broadcastSymbol, result.subscribersBySymbol[scenario.broadcastSymbol])
		}
	case "filtered_half":
		expectedFanout := float64((scenario.clients - scenario.filteredClients) * scenario.messages)
		if result.fanout != expectedFanout {
			t.Fatalf("expected filtered fanout %.0f, got %.0f", expectedFanout, result.fanout)
		}
		if result.typeDrops != 0 || result.typeEvictions != 0 {
			t.Fatalf("expected no drops or evictions, got drops=%.0f evictions=%.0f", result.typeDrops, result.typeEvictions)
		}
		if result.subscribersBySymbol[scenario.broadcastSymbol] != float64(scenario.clients-scenario.filteredClients) {
			t.Fatalf("expected %.0f subscribers on %s, got %.0f", float64(scenario.clients-scenario.filteredClients), scenario.broadcastSymbol, result.subscribersBySymbol[scenario.broadcastSymbol])
		}
		if result.subscribersBySymbol[scenario.filterSymbol] != float64(scenario.clients) {
			t.Fatalf("expected %.0f subscribers on %s, got %.0f", float64(scenario.clients), scenario.filterSymbol, result.subscribersBySymbol[scenario.filterSymbol])
		}
	case "slow_mix":
		if result.typeDrops <= 0 {
			t.Fatal("expected slow mix to produce drops")
		}
		if result.typeEvictions <= 0 {
			t.Fatal("expected slow mix to evict slow clients")
		}
		if result.typeEvictions != float64(scenario.slowClients) {
			t.Fatalf("expected %.0f evictions, got %.0f", float64(scenario.slowClients), result.typeEvictions)
		}
		if result.subscribersBySymbol[scenario.broadcastSymbol] != float64(scenario.clients-scenario.slowClients) {
			t.Fatalf("expected %.0f remaining subscribers on %s, got %.0f", float64(scenario.clients-scenario.slowClients), scenario.broadcastSymbol, result.subscribersBySymbol[scenario.broadcastSymbol])
		}
		minFanout := float64((scenario.clients - scenario.slowClients) * scenario.messages)
		maxFanout := float64(scenario.clients * scenario.messages)
		if result.fanout < minFanout || result.fanout >= maxFanout {
			t.Fatalf("expected slow mix fanout in [%.0f, %.0f), got %.0f", minFanout, maxFanout, result.fanout)
		}
	}
}

func logWSLoadScenario(t *testing.T, scenario wsLoadScenario, result wsLoadResult) {
	t.Helper()

	avgBroadcastMicros := 0.0
	if result.broadcastDurationCount > 0 {
		avgBroadcastMicros = (result.broadcastDurationSum / result.broadcastDurationCount) * float64(time.Second/time.Microsecond)
	}

	t.Logf(
		"scenario=%s clients=%d slow=%d filtered=%d messages=%d fanout=%.0f drops=%.0f evictions=%.0f ws_clients=%.0f subs=%s avg_broadcast_us=%.1f",
		scenario.name,
		scenario.clients,
		scenario.slowClients,
		scenario.filteredClients,
		scenario.messages,
		result.fanout,
		result.typeDrops,
		result.typeEvictions,
		result.clientsGauge,
		formatSubscriberSummary(result.subscribersBySymbol),
		avgBroadcastMicros,
	)
}

func scrapeWSMetricSnapshot(t *testing.T, metricsURL string) wsMetricSnapshot {
	t.Helper()

	resp, err := http.Get(metricsURL)
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}

	return parseWSMetricSnapshot(string(data))
}

func parseWSMetricSnapshot(body string) wsMetricSnapshot {
	metrics := make(wsMetricSnapshot)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		idx := strings.LastIndexByte(line, ' ')
		if idx <= 0 || idx >= len(line)-1 {
			continue
		}

		value, err := strconv.ParseFloat(strings.TrimSpace(line[idx+1:]), 64)
		if err != nil {
			continue
		}
		metrics[strings.TrimSpace(line[:idx])] = value
	}
	return metrics
}

func metricDiff(before, after wsMetricSnapshot, key string) float64 {
	return after[key] - before[key]
}

func metricValue(snapshot wsMetricSnapshot, key string) float64 {
	return snapshot[key]
}

func formatSubscriberSummary(subscribers map[string]float64) string {
	keys := make([]string, 0, len(subscribers))
	for symbol := range subscribers {
		keys = append(keys, symbol)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, symbol := range keys {
		parts = append(parts, fmt.Sprintf("%s=%.0f", symbol, subscribers[symbol]))
	}
	return strings.Join(parts, ",")
}
