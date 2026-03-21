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

	wsClientsGauge        atomic.Int64
	wsDropsTotal          atomic.Uint64
	snapshotLagSeconds    atomic.Uint64
	writerFlushDuration   histogram
	writerBatchSize       histogram
	pipelineLatencyMillis histogram
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
		channelDrops: make(map[string]uint64),
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

func IncWSDrop() {
	defaultRegistry.wsDropsTotal.Add(1)
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
