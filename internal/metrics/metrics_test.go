package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRegistryHandler(t *testing.T) {
	r := newRegistry()
	r.incChannelDrop("writer")
	r.addWSFanout("latest_price", 11)
	r.addWSSymbolFanout("latest_price", "BTC/USD", 7)
	r.addWSSymbolFanout("heartbeat", "", 3)
	r.addWSDropType("latest_price", 5)
	r.addWSSymbolDrop("latest_price", "BTC/USD", 4)
	r.addWSSymbolDrop("heartbeat", "", 2)
	r.addWSEvictType("latest_price", 2)
	r.addWSSymbolEvict("latest_price", "BTC/USD", 1)
	r.addWSSymbolEvict("heartbeat", "", 1)
	r.setWSSymbolSubscribers(map[string]int{"BTC/USD": 2, "ETH/USD": 1})
	r.writerFlushDuration.observe(0.012)
	r.writerBatchSize.observe(128)
	r.pipelineLatencyMillis.observe(42)
	r.wsClientsGauge.Store(3)
	r.wsDropsTotal.Store(7)
	r.wsCoalescedTotal.Store(5)
	r.wsCoalescedBatches.Store(2)
	r.snapshotLagSeconds.Store(0x3ff0000000000000) // 1.0

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{
		`btick_channel_drops_total{channel="writer"} 1`,
		`btick_ws_fanout_total{type="latest_price"} 11`,
		`btick_ws_type_drops_total{type="latest_price"} 5`,
		`btick_ws_type_evictions_total{type="latest_price"} 2`,
		`btick_ws_symbol_fanout_total{type="heartbeat",symbol="_none"} 3`,
		`btick_ws_symbol_fanout_total{type="latest_price",symbol="BTC/USD"} 7`,
		`btick_ws_symbol_drops_total{type="heartbeat",symbol="_none"} 2`,
		`btick_ws_symbol_drops_total{type="latest_price",symbol="BTC/USD"} 4`,
		`btick_ws_symbol_evictions_total{type="heartbeat",symbol="_none"} 1`,
		`btick_ws_symbol_evictions_total{type="latest_price",symbol="BTC/USD"} 1`,
		`btick_ws_symbol_subscribers{symbol="BTC/USD"} 2`,
		`btick_ws_symbol_subscribers{symbol="ETH/USD"} 1`,
		"btick_writer_flush_duration_seconds_bucket",
		"btick_writer_batch_size_count 1",
		"btick_snapshot_finalize_lag_seconds 1",
		"btick_pipeline_latency_ms_count 1",
		"btick_ws_clients 3",
		"btick_ws_drops_total 7",
		"btick_ws_coalesced_total 5",
		"btick_ws_coalesced_batches_total 2",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected metrics output to contain %q, got %q", want, body)
		}
	}
}

func TestObserveWriterFlushSkipsEmptyBatch(t *testing.T) {
	before := defaultRegistry.writerBatchSize.count
	ObserveWriterFlush(0, time.Second)
	after := defaultRegistry.writerBatchSize.count
	if before != after {
		t.Fatalf("expected empty batch observation to be skipped")
	}
}

func TestAddWSCoalescedSkipsNonPositive(t *testing.T) {
	oldRegistry := defaultRegistry
	r := newRegistry()
	r.wsCoalescedTotal.Store(3)
	r.wsCoalescedBatches.Store(1)
	defaultRegistry = r
	defer func() { defaultRegistry = oldRegistry }()

	AddWSCoalesced(0)

	if got := r.wsCoalescedTotal.Load(); got != 3 {
		t.Fatalf("expected coalesced total to remain unchanged")
	}
	if got := r.wsCoalescedBatches.Load(); got != 1 {
		t.Fatalf("expected coalesced batch count to remain unchanged")
	}
}

func TestAddWSCoalescedIncrementsTotals(t *testing.T) {
	oldRegistry := defaultRegistry
	r := newRegistry()
	defaultRegistry = r
	defer func() { defaultRegistry = oldRegistry }()

	AddWSCoalesced(4)

	if got := r.wsCoalescedTotal.Load(); got != 4 {
		t.Fatalf("expected coalesced total 4, got %d", got)
	}
	if got := r.wsCoalescedBatches.Load(); got != 1 {
		t.Fatalf("expected coalesced batches 1, got %d", got)
	}
}

func TestAddWSFanoutSkipsNonPositive(t *testing.T) {
	oldRegistry := defaultRegistry
	r := newRegistry()
	r.addWSFanout("latest_price", 3)
	defaultRegistry = r
	defer func() { defaultRegistry = oldRegistry }()

	AddWSFanout("latest_price", 0)

	if got := r.snapshotWSFanout()["latest_price"]; got != 3 {
		t.Fatalf("expected fanout total to remain unchanged, got %d", got)
	}
}

func TestAddWSFanoutIncrementsTotals(t *testing.T) {
	oldRegistry := defaultRegistry
	r := newRegistry()
	defaultRegistry = r
	defer func() { defaultRegistry = oldRegistry }()

	AddWSFanout("latest_price", 4)
	AddWSFanout("", 2)

	fanout := r.snapshotWSFanout()
	if got := fanout["latest_price"]; got != 4 {
		t.Fatalf("expected latest_price fanout 4, got %d", got)
	}
	if got := fanout["unknown"]; got != 2 {
		t.Fatalf("expected unknown fanout 2, got %d", got)
	}
}

func TestAddWSSymbolFanoutSkipsNonPositive(t *testing.T) {
	oldRegistry := defaultRegistry
	r := newRegistry()
	r.addWSSymbolFanout("latest_price", "BTC/USD", 3)
	defaultRegistry = r
	defer func() { defaultRegistry = oldRegistry }()

	AddWSSymbolFanout("latest_price", "BTC/USD", 0)

	if got := r.snapshotWSSymbolFanout()[wsSymbolFanoutKey{messageType: "latest_price", symbol: "BTC/USD"}]; got != 3 {
		t.Fatalf("expected symbol fanout total to remain unchanged, got %d", got)
	}
}

func TestAddWSSymbolFanoutIncrementsTotals(t *testing.T) {
	oldRegistry := defaultRegistry
	r := newRegistry()
	defaultRegistry = r
	defer func() { defaultRegistry = oldRegistry }()

	AddWSSymbolFanout("latest_price", "BTC/USD", 4)
	AddWSSymbolFanout("heartbeat", "", 2)

	fanout := r.snapshotWSSymbolFanout()
	if got := fanout[wsSymbolFanoutKey{messageType: "latest_price", symbol: "BTC/USD"}]; got != 4 {
		t.Fatalf("expected BTC/USD symbol fanout 4, got %d", got)
	}
	if got := fanout[wsSymbolFanoutKey{messageType: "heartbeat", symbol: "_none"}]; got != 2 {
		t.Fatalf("expected _none symbol fanout 2, got %d", got)
	}
}

func TestAddWSDropDetailSkipsNonPositive(t *testing.T) {
	oldRegistry := defaultRegistry
	r := newRegistry()
	r.addWSDropType("latest_price", 3)
	r.addWSSymbolDrop("latest_price", "BTC/USD", 2)
	defaultRegistry = r
	defer func() { defaultRegistry = oldRegistry }()

	AddWSDropDetail("latest_price", "BTC/USD", 0)

	if got := r.snapshotWSDropTypes()["latest_price"]; got != 3 {
		t.Fatalf("expected type drop total unchanged, got %d", got)
	}
	if got := r.snapshotWSSymbolDrops()[wsSymbolFanoutKey{messageType: "latest_price", symbol: "BTC/USD"}]; got != 2 {
		t.Fatalf("expected symbol drop total unchanged, got %d", got)
	}
}

func TestAddWSDropDetailIncrementsTotals(t *testing.T) {
	oldRegistry := defaultRegistry
	r := newRegistry()
	defaultRegistry = r
	defer func() { defaultRegistry = oldRegistry }()

	AddWSDropDetail("latest_price", "BTC/USD", 4)
	AddWSDropDetail("", "", 2)

	dropTypes := r.snapshotWSDropTypes()
	if got := dropTypes["latest_price"]; got != 4 {
		t.Fatalf("expected latest_price type drops 4, got %d", got)
	}
	if got := dropTypes["unknown"]; got != 2 {
		t.Fatalf("expected unknown type drops 2, got %d", got)
	}
	symbolDrops := r.snapshotWSSymbolDrops()
	if got := symbolDrops[wsSymbolFanoutKey{messageType: "latest_price", symbol: "BTC/USD"}]; got != 4 {
		t.Fatalf("expected BTC/USD symbol drops 4, got %d", got)
	}
	if got := symbolDrops[wsSymbolFanoutKey{messageType: "unknown", symbol: "_none"}]; got != 2 {
		t.Fatalf("expected unknown/_none symbol drops 2, got %d", got)
	}
}

func TestAddWSEvictionDetailSkipsNonPositive(t *testing.T) {
	oldRegistry := defaultRegistry
	r := newRegistry()
	r.addWSEvictType("latest_price", 3)
	r.addWSSymbolEvict("latest_price", "BTC/USD", 2)
	defaultRegistry = r
	defer func() { defaultRegistry = oldRegistry }()

	AddWSEvictionDetail("latest_price", "BTC/USD", 0)

	if got := r.snapshotWSEvictTypes()["latest_price"]; got != 3 {
		t.Fatalf("expected type eviction total unchanged, got %d", got)
	}
	if got := r.snapshotWSSymbolEvicts()[wsSymbolFanoutKey{messageType: "latest_price", symbol: "BTC/USD"}]; got != 2 {
		t.Fatalf("expected symbol eviction total unchanged, got %d", got)
	}
}

func TestAddWSEvictionDetailIncrementsTotals(t *testing.T) {
	oldRegistry := defaultRegistry
	r := newRegistry()
	defaultRegistry = r
	defer func() { defaultRegistry = oldRegistry }()

	AddWSEvictionDetail("latest_price", "BTC/USD", 4)
	AddWSEvictionDetail("", "", 2)

	evictTypes := r.snapshotWSEvictTypes()
	if got := evictTypes["latest_price"]; got != 4 {
		t.Fatalf("expected latest_price type evictions 4, got %d", got)
	}
	if got := evictTypes["unknown"]; got != 2 {
		t.Fatalf("expected unknown type evictions 2, got %d", got)
	}
	symbolEvicts := r.snapshotWSSymbolEvicts()
	if got := symbolEvicts[wsSymbolFanoutKey{messageType: "latest_price", symbol: "BTC/USD"}]; got != 4 {
		t.Fatalf("expected BTC/USD symbol evictions 4, got %d", got)
	}
	if got := symbolEvicts[wsSymbolFanoutKey{messageType: "unknown", symbol: "_none"}]; got != 2 {
		t.Fatalf("expected unknown/_none symbol evictions 2, got %d", got)
	}
}

func TestSetWSSymbolSubscribersFiltersInvalidValues(t *testing.T) {
	r := newRegistry()
	r.setWSSymbolSubscribers(map[string]int{"BTC/USD": 2, "": 5, "ETH/USD": 0})

	subs := r.snapshotWSSymbolSubscribers()
	if got := subs["BTC/USD"]; got != 2 {
		t.Fatalf("expected BTC/USD subscribers 2, got %d", got)
	}
	if _, ok := subs[""]; ok {
		t.Fatal("expected empty symbol to be omitted")
	}
	if _, ok := subs["ETH/USD"]; ok {
		t.Fatal("expected non-positive subscriber count to be omitted")
	}
}
