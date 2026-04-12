package metrics

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var defaultRegistry = newRegistry()

type registry struct {
	channelDropsMu sync.RWMutex
	channelDrops   map[string]uint64
	wsFanoutMu     sync.RWMutex
	wsFanout       map[string]uint64
	wsSymbolFanout map[wsSymbolFanoutKey]uint64
	wsDropTypes    map[string]uint64
	wsSymbolDrops  map[wsSymbolFanoutKey]uint64
	wsEvictTypes   map[string]uint64
	wsSymbolEvicts map[wsSymbolFanoutKey]uint64
	wsSymbolSubsMu sync.RWMutex
	wsSymbolSubs   map[string]int64

	wsClientsGauge        atomic.Int64
	wsDropsTotal          atomic.Uint64
	wsEvictedTotal        atomic.Uint64
	wsRejectedTotal       atomic.Uint64
	wsBroadcastsTotal     atomic.Uint64
	wsCoalescedTotal      atomic.Uint64
	wsCoalescedBatches    atomic.Uint64
	snapshotLagSeconds    atomic.Uint64
	writerFlushDuration   histogram
	writerBatchSize       histogram
	pipelineLatencyMillis histogram
	wsBroadcastDuration   histogram
}

type wsSymbolFanoutKey struct {
	messageType string
	symbol      string
}

type histogram struct {
	mu      sync.Mutex
	buckets []float64
	counts  []uint64
	sum     float64
	count   uint64
}

func newRegistry() *registry {
	return &registry{
		channelDrops:   make(map[string]uint64),
		wsFanout:       make(map[string]uint64),
		wsSymbolFanout: make(map[wsSymbolFanoutKey]uint64),
		wsDropTypes:    make(map[string]uint64),
		wsSymbolDrops:  make(map[wsSymbolFanoutKey]uint64),
		wsEvictTypes:   make(map[string]uint64),
		wsSymbolEvicts: make(map[wsSymbolFanoutKey]uint64),
		wsSymbolSubs:   make(map[string]int64),
		writerFlushDuration: histogram{
			buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 5},
			counts:  make([]uint64, 10),
		},
		writerBatchSize: histogram{
			buckets: []float64{1, 10, 50, 100, 250, 500, 1000, 2000, 5000},
			counts:  make([]uint64, 9),
		},
		pipelineLatencyMillis: histogram{
			buckets: []float64{5, 10, 25, 50, 100, 250, 500, 1000, 5000},
			counts:  make([]uint64, 9),
		},
		wsBroadcastDuration: histogram{
			buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5},
			counts:  make([]uint64, 8),
		},
	}
}

func IncChannelDrop(channel string) {
	defaultRegistry.incChannelDrop(channel)
}

func ObserveWriterFlush(batchSize int, d time.Duration) {
	if batchSize <= 0 {
		return
	}
	defaultRegistry.writerBatchSize.observe(float64(batchSize))
	defaultRegistry.writerFlushDuration.observe(d.Seconds())
}

func SetSnapshotFinalizeLag(d time.Duration) {
	defaultRegistry.snapshotLagSeconds.Store(math.Float64bits(d.Seconds()))
}

func ObservePipelineLatency(d time.Duration) {
	defaultRegistry.pipelineLatencyMillis.observe(float64(d.Milliseconds()))
}

func SetWSClients(n int) {
	defaultRegistry.wsClientsGauge.Store(int64(n))
}

func SetWSSymbolSubscribers(counts map[string]int) {
	defaultRegistry.setWSSymbolSubscribers(counts)
}

func IncWSDrop() {
	defaultRegistry.wsDropsTotal.Add(1)
}

func IncWSEvicted() {
	defaultRegistry.wsEvictedTotal.Add(1)
}

func IncWSRejected() {
	defaultRegistry.wsRejectedTotal.Add(1)
}

func IncWSBroadcast() {
	defaultRegistry.wsBroadcastsTotal.Add(1)
}

func AddWSFanout(messageType string, n int) {
	if n <= 0 {
		return
	}
	defaultRegistry.addWSFanout(messageType, uint64(n))
}

func AddWSSymbolFanout(messageType, symbol string, n int) {
	if n <= 0 {
		return
	}
	defaultRegistry.addWSSymbolFanout(messageType, symbol, uint64(n))
}

func AddWSDropDetail(messageType, symbol string, n int) {
	if n <= 0 {
		return
	}
	defaultRegistry.addWSDropType(messageType, uint64(n))
	defaultRegistry.addWSSymbolDrop(messageType, symbol, uint64(n))
}

func AddWSEvictionDetail(messageType, symbol string, n int) {
	if n <= 0 {
		return
	}
	defaultRegistry.addWSEvictType(messageType, uint64(n))
	defaultRegistry.addWSSymbolEvict(messageType, symbol, uint64(n))
}

func AddWSCoalesced(n int) {
	if n <= 0 {
		return
	}
	defaultRegistry.wsCoalescedTotal.Add(uint64(n))
	defaultRegistry.wsCoalescedBatches.Add(1)
}

func ObserveWSBroadcastDuration(d time.Duration) {
	defaultRegistry.wsBroadcastDuration.observe(d.Seconds())
}

func Handler() http.Handler {
	return defaultRegistry
}

func (r *registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	var b strings.Builder
	r.writeCounterVec(&b,
		"btick_channel_drops_total",
		"Total dropped events by pipeline channel.",
		"channel",
		r.snapshotChannelDrops(),
	)
	r.writeCounterVec(&b,
		"btick_ws_fanout_total",
		"Total WebSocket client deliveries by message type.",
		"type",
		r.snapshotWSFanout(),
	)
	r.writeCounterVec(&b,
		"btick_ws_type_drops_total",
		"Total dropped WebSocket messages by message type.",
		"type",
		r.snapshotWSDropTypes(),
	)
	r.writeCounterVec(&b,
		"btick_ws_type_evictions_total",
		"Total WebSocket client evictions triggered by message type.",
		"type",
		r.snapshotWSEvictTypes(),
	)
	r.writeCounterVec2(&b,
		"btick_ws_symbol_fanout_total",
		"Total WebSocket client deliveries by message type and symbol.",
		"type",
		"symbol",
		r.snapshotWSSymbolFanout(),
	)
	r.writeCounterVec2(&b,
		"btick_ws_symbol_drops_total",
		"Total dropped WebSocket messages by message type and symbol.",
		"type",
		"symbol",
		r.snapshotWSSymbolDrops(),
	)
	r.writeCounterVec2(&b,
		"btick_ws_symbol_evictions_total",
		"Total WebSocket client evictions triggered by message type and symbol.",
		"type",
		"symbol",
		r.snapshotWSSymbolEvicts(),
	)
	r.writeGaugeVec(&b,
		"btick_ws_symbol_subscribers",
		"Currently connected WebSocket clients subscribed to each symbol.",
		"symbol",
		r.snapshotWSSymbolSubscribers(),
	)
	r.writerFlushDuration.writePrometheus(&b,
		"btick_writer_flush_duration_seconds",
		"Duration of raw writer batch flushes in seconds.",
	)
	r.writerBatchSize.writePrometheus(&b,
		"btick_writer_batch_size",
		"Rows flushed per raw writer batch.",
	)
	r.writeGauge(&b,
		"btick_snapshot_finalize_lag_seconds",
		"Age in seconds between snapshot second and finalization time.",
		math.Float64frombits(r.snapshotLagSeconds.Load()),
	)
	r.pipelineLatencyMillis.writePrometheus(&b,
		"btick_pipeline_latency_ms",
		"Latency in milliseconds from exchange timestamp to receive timestamp.",
	)
	r.writeGauge(&b,
		"btick_ws_clients",
		"Currently connected WebSocket clients.",
		float64(r.wsClientsGauge.Load()),
	)
	r.writeCounter(&b,
		"btick_ws_drops_total",
		"Total dropped WebSocket broadcast messages.",
		float64(r.wsDropsTotal.Load()),
	)
	r.writeCounter(&b,
		"btick_ws_evicted_total",
		"Total WebSocket clients evicted for being too slow.",
		float64(r.wsEvictedTotal.Load()),
	)
	r.writeCounter(&b,
		"btick_ws_rejected_total",
		"Total WebSocket connections rejected due to max client limit.",
		float64(r.wsRejectedTotal.Load()),
	)
	r.writeCounter(&b,
		"btick_ws_broadcasts_total",
		"Total WebSocket broadcast operations.",
		float64(r.wsBroadcastsTotal.Load()),
	)
	r.writeCounter(&b,
		"btick_ws_coalesced_total",
		"Total WebSocket updates superseded by burst coalescing before broadcast.",
		float64(r.wsCoalescedTotal.Load()),
	)
	r.writeCounter(&b,
		"btick_ws_coalesced_batches_total",
		"Total WebSocket broadcast batches where at least one update was coalesced.",
		float64(r.wsCoalescedBatches.Load()),
	)
	r.wsBroadcastDuration.writePrometheus(&b,
		"btick_ws_broadcast_duration_seconds",
		"Duration of WebSocket broadcast operations in seconds.",
	)

	_, _ = w.Write([]byte(b.String()))
}

func (r *registry) incChannelDrop(channel string) {
	r.channelDropsMu.Lock()
	defer r.channelDropsMu.Unlock()
	r.channelDrops[channel]++
}

func (r *registry) snapshotChannelDrops() map[string]uint64 {
	r.channelDropsMu.RLock()
	defer r.channelDropsMu.RUnlock()

	out := make(map[string]uint64, len(r.channelDrops))
	for channel, total := range r.channelDrops {
		out[channel] = total
	}
	return out
}

func (r *registry) addWSFanout(messageType string, count uint64) {
	messageType = strings.TrimSpace(messageType)
	if messageType == "" {
		messageType = "unknown"
	}

	r.wsFanoutMu.Lock()
	defer r.wsFanoutMu.Unlock()
	r.wsFanout[messageType] += count
}

func (r *registry) snapshotWSFanout() map[string]uint64 {
	r.wsFanoutMu.RLock()
	defer r.wsFanoutMu.RUnlock()

	out := make(map[string]uint64, len(r.wsFanout))
	for messageType, total := range r.wsFanout {
		out[messageType] = total
	}
	return out
}

func normalizeMetricType(messageType string) string {
	messageType = strings.TrimSpace(messageType)
	if messageType == "" {
		return "unknown"
	}
	return messageType
}

func normalizeMetricSymbol(symbol string) string {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return "_none"
	}
	return symbol
}

func (r *registry) addWSSymbolFanout(messageType, symbol string, count uint64) {
	messageType = normalizeMetricType(messageType)
	symbol = normalizeMetricSymbol(symbol)

	r.wsFanoutMu.Lock()
	defer r.wsFanoutMu.Unlock()
	r.wsSymbolFanout[wsSymbolFanoutKey{messageType: messageType, symbol: symbol}] += count
}

func (r *registry) addWSDropType(messageType string, count uint64) {
	messageType = normalizeMetricType(messageType)

	r.wsFanoutMu.Lock()
	defer r.wsFanoutMu.Unlock()
	r.wsDropTypes[messageType] += count
}

func (r *registry) snapshotWSDropTypes() map[string]uint64 {
	r.wsFanoutMu.RLock()
	defer r.wsFanoutMu.RUnlock()

	out := make(map[string]uint64, len(r.wsDropTypes))
	for messageType, total := range r.wsDropTypes {
		out[messageType] = total
	}
	return out
}

func (r *registry) addWSEvictType(messageType string, count uint64) {
	messageType = normalizeMetricType(messageType)

	r.wsFanoutMu.Lock()
	defer r.wsFanoutMu.Unlock()
	r.wsEvictTypes[messageType] += count
}

func (r *registry) snapshotWSEvictTypes() map[string]uint64 {
	r.wsFanoutMu.RLock()
	defer r.wsFanoutMu.RUnlock()

	out := make(map[string]uint64, len(r.wsEvictTypes))
	for messageType, total := range r.wsEvictTypes {
		out[messageType] = total
	}
	return out
}

func (r *registry) snapshotWSSymbolFanout() map[wsSymbolFanoutKey]uint64 {
	r.wsFanoutMu.RLock()
	defer r.wsFanoutMu.RUnlock()

	out := make(map[wsSymbolFanoutKey]uint64, len(r.wsSymbolFanout))
	for key, total := range r.wsSymbolFanout {
		out[key] = total
	}
	return out
}

func (r *registry) addWSSymbolDrop(messageType, symbol string, count uint64) {
	messageType = normalizeMetricType(messageType)
	symbol = normalizeMetricSymbol(symbol)

	r.wsFanoutMu.Lock()
	defer r.wsFanoutMu.Unlock()
	r.wsSymbolDrops[wsSymbolFanoutKey{messageType: messageType, symbol: symbol}] += count
}

func (r *registry) snapshotWSSymbolDrops() map[wsSymbolFanoutKey]uint64 {
	r.wsFanoutMu.RLock()
	defer r.wsFanoutMu.RUnlock()

	out := make(map[wsSymbolFanoutKey]uint64, len(r.wsSymbolDrops))
	for key, total := range r.wsSymbolDrops {
		out[key] = total
	}
	return out
}

func (r *registry) addWSSymbolEvict(messageType, symbol string, count uint64) {
	messageType = normalizeMetricType(messageType)
	symbol = normalizeMetricSymbol(symbol)

	r.wsFanoutMu.Lock()
	defer r.wsFanoutMu.Unlock()
	r.wsSymbolEvicts[wsSymbolFanoutKey{messageType: messageType, symbol: symbol}] += count
}

func (r *registry) snapshotWSSymbolEvicts() map[wsSymbolFanoutKey]uint64 {
	r.wsFanoutMu.RLock()
	defer r.wsFanoutMu.RUnlock()

	out := make(map[wsSymbolFanoutKey]uint64, len(r.wsSymbolEvicts))
	for key, total := range r.wsSymbolEvicts {
		out[key] = total
	}
	return out
}

func (r *registry) setWSSymbolSubscribers(counts map[string]int) {
	next := make(map[string]int64, len(counts))
	for symbol, count := range counts {
		if count <= 0 {
			continue
		}
		symbol = strings.TrimSpace(symbol)
		if symbol == "" {
			continue
		}
		next[symbol] = int64(count)
	}

	r.wsSymbolSubsMu.Lock()
	defer r.wsSymbolSubsMu.Unlock()
	r.wsSymbolSubs = next
}

func (r *registry) snapshotWSSymbolSubscribers() map[string]int64 {
	r.wsSymbolSubsMu.RLock()
	defer r.wsSymbolSubsMu.RUnlock()

	out := make(map[string]int64, len(r.wsSymbolSubs))
	for symbol, count := range r.wsSymbolSubs {
		out[symbol] = count
	}
	return out
}

func (r *registry) writeCounterVec(
	b *strings.Builder,
	name string,
	help string,
	labelName string,
	values map[string]uint64,
) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s counter\n", name)

	if len(values) == 0 {
		return
	}

	labels := make([]string, 0, len(values))
	for label := range values {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	for _, label := range labels {
		fmt.Fprintf(b, "%s{%s=\"%s\"} %d\n", name, labelName, escapeLabelValue(label), values[label])
	}
}

func (r *registry) writeCounter(b *strings.Builder, name, help string, value float64) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s counter\n", name)
	fmt.Fprintf(b, "%s %s\n", name, formatFloat(value))
}

func (r *registry) writeGaugeVec(
	b *strings.Builder,
	name string,
	help string,
	labelName string,
	values map[string]int64,
) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s gauge\n", name)

	if len(values) == 0 {
		return
	}

	labels := make([]string, 0, len(values))
	for label := range values {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	for _, label := range labels {
		fmt.Fprintf(b, "%s{%s=\"%s\"} %d\n", name, labelName, escapeLabelValue(label), values[label])
	}
}

func (r *registry) writeCounterVec2(
	b *strings.Builder,
	name string,
	help string,
	labelName1 string,
	labelName2 string,
	values map[wsSymbolFanoutKey]uint64,
) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s counter\n", name)

	if len(values) == 0 {
		return
	}

	keys := make([]wsSymbolFanoutKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].messageType != keys[j].messageType {
			return keys[i].messageType < keys[j].messageType
		}
		return keys[i].symbol < keys[j].symbol
	})

	for _, key := range keys {
		fmt.Fprintf(
			b,
			"%s{%s=\"%s\",%s=\"%s\"} %d\n",
			name,
			labelName1,
			escapeLabelValue(key.messageType),
			labelName2,
			escapeLabelValue(key.symbol),
			values[key],
		)
	}
}

func (r *registry) writeGauge(b *strings.Builder, name, help string, value float64) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s gauge\n", name)
	fmt.Fprintf(b, "%s %s\n", name, formatFloat(value))
}

func (h *histogram) observe(value float64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.count++
	h.sum += value
	for i, upper := range h.buckets {
		if value <= upper {
			h.counts[i]++
		}
	}
}

func (h *histogram) writePrometheus(b *strings.Builder, name, help string) {
	h.mu.Lock()
	counts := append([]uint64(nil), h.counts...)
	count := h.count
	sum := h.sum
	buckets := append([]float64(nil), h.buckets...)
	h.mu.Unlock()

	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s histogram\n", name)
	for i, upper := range buckets {
		fmt.Fprintf(
			b,
			"%s_bucket{le=%q} %d\n",
			name,
			formatFloat(upper),
			counts[i],
		)
	}
	fmt.Fprintf(b, "%s_bucket{le=%q} %d\n", name, "+Inf", count)
	fmt.Fprintf(b, "%s_sum %s\n", name, formatFloat(sum))
	fmt.Fprintf(b, "%s_count %d\n", name, count)
}

func escapeLabelValue(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return strings.ReplaceAll(value, "\n", "\\n")
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
