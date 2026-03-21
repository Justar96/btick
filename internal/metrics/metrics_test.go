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
	r.writerFlushDuration.observe(0.012)
	r.writerBatchSize.observe(128)
	r.pipelineLatencyMillis.observe(42)
	r.wsClientsGauge.Store(3)
	r.wsDropsTotal.Store(7)
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
		"btick_writer_flush_duration_seconds_bucket",
		"btick_writer_batch_size_count 1",
		"btick_snapshot_finalize_lag_seconds 1",
		"btick_pipeline_latency_ms_count 1",
		"btick_ws_clients 3",
		"btick_ws_drops_total 7",
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
