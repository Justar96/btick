# WebSocket Operations

This document covers a minimal Grafana dashboard, alert thresholds, and a repeatable load-test workflow for the WebSocket delivery path.

## Dashboard Spec

Target: one small dashboard page focused on fan-out pressure, client quality, and slow-consumer impact.

### Panel 1: Connected Clients

- **Title:** `WS Connected Clients`
- **Type:** Stat + sparkline
- **Query:** `btick_ws_clients`
- **Why:** Confirms active connection count and gives the denominator for any drop or eviction event.

### Panel 2: Subscribers by Symbol

- **Title:** `WS Subscribers by Symbol`
- **Type:** Bar chart
- **Query:** `btick_ws_symbol_subscribers`
- **Why:** Shows whether symbol filtering is actually narrowing fan-out or whether most clients still subscribe to everything.

### Panel 3: Fan-out Rate by Message Type

- **Title:** `WS Fan-out Rate`
- **Type:** Stacked time series
- **Query:** `sum by (type) (rate(btick_ws_fanout_total[5m]))`
- **Why:** Tells you which message families are driving outbound work.

### Panel 4: Drop Ratio by Message Type

- **Title:** `WS Drop Ratio`
- **Type:** Time series
- **Query:** `sum by (type) (rate(btick_ws_type_drops_total[5m])) / clamp_min(sum by (type) (rate(btick_ws_fanout_total[5m])), 1)`
- **Why:** Measures slow-consumer pain before it becomes eviction.

### Panel 5: Eviction Rate

- **Title:** `WS Evictions`
- **Type:** Time series
- **Query:** `sum by (type) (rate(btick_ws_type_evictions_total[5m]))`
- **Why:** Shows when drops crossed from tolerable buffering into forced disconnects.

### Panel 6: Hot-Symbol Pressure

- **Title:** `Hot Symbol Drop/Eviction Pressure`
- **Type:** Table
- **Queries:**
  - `topk(10, sum by (symbol, type) (rate(btick_ws_symbol_drops_total[5m])))`
  - `topk(10, sum by (symbol, type) (rate(btick_ws_symbol_evictions_total[5m])))`
- **Why:** Identifies whether one symbol is creating disproportionate pressure.

### Panel 7: Burst Coalescing

- **Title:** `WS Coalesced Updates`
- **Type:** Time series
- **Queries:**
  - `rate(btick_ws_coalesced_total[5m])`
  - `rate(btick_ws_coalesced_batches_total[5m])`
- **Why:** Confirms the broadcaster is collapsing superseded updates during upstream bursts instead of fanning out every intermediate event.

### Panel 8: Broadcast Latency

- **Title:** `WS Broadcast Duration p95`
- **Type:** Time series
- **Query:** `histogram_quantile(0.95, sum by (le) (rate(btick_ws_broadcast_duration_seconds_bucket[5m])))`
- **Why:** Captures whether outbound write pressure is increasing per-broadcast cost.

## Alert Thresholds

These are intentionally small and operationally conservative for the current defaults:

- `send_buffer_size`: 256 by default
- `slow_client_max_drops`: 500 by default
- `max_clients`: 1000 by default

### Warning

- **Drop ratio elevated:**
  - Expression: `sum(rate(btick_ws_type_drops_total[10m])) / clamp_min(sum(rate(btick_ws_fanout_total[10m])), 1) > 0.005`
  - Meaning: more than `0.5%` of attempted deliveries are being dropped for 10 minutes.
- **Evictions present:**
  - Expression: `sum(increase(btick_ws_type_evictions_total[15m])) > 0`
  - Meaning: any client had to be forcibly removed in the last 15 minutes.
- **Rejections present:**
  - Expression: `increase(btick_ws_rejected_total[10m]) > 0`
  - Meaning: the hub hit its configured max client count.
- **Broadcast latency p95 elevated:**
  - Expression: `histogram_quantile(0.95, sum by (le) (rate(btick_ws_broadcast_duration_seconds_bucket[10m]))) > 0.025`
  - Meaning: p95 broadcast time is above `25ms` for 10 minutes.

### Critical

- **Drop ratio high:**
  - Expression: `sum(rate(btick_ws_type_drops_total[5m])) / clamp_min(sum(rate(btick_ws_fanout_total[5m])), 1) > 0.02`
  - Meaning: more than `2%` of attempted deliveries are being dropped for 5 minutes.
- **Eviction burst:**
  - Expression: `sum(increase(btick_ws_type_evictions_total[5m])) >= 3`
  - Meaning: multiple clients were evicted in a short window.
- **Connection saturation:**
  - Expression: `btick_ws_clients / 1000 > 0.9`
  - Meaning: the hub is above `90%` of the default connection limit.
- **Broadcast latency p95 high:**
  - Expression: `histogram_quantile(0.95, sum by (le) (rate(btick_ws_broadcast_duration_seconds_bucket[5m]))) > 0.1`
  - Meaning: p95 broadcast time is above `100ms` for 5 minutes.

## Load Test Workflow

Run the dedicated websocket load scenarios with:

```bash
RUN_WS_LOAD=1 go test -run TestWebSocketLoadScenarios -count=1 ./internal/api -v
```

The test covers three scenarios:

- `all_fast`: all clients read continuously; expected to show linear fan-out with zero drops and zero evictions.
- `filtered_half`: half the clients connect with `?symbols=ETH/USD` while the load generator broadcasts `BTC/USD`; expected to show the same client count but lower `BTC/USD` fan-out and lower `BTC/USD` subscriber gauge.
- `slow_mix`: a subset of clients never reads and uses a smaller send buffer and drop threshold; expected to show drops first, then evictions, while healthy clients continue to receive traffic.

## Expected Metric Behavior

### `all_fast`

- `btick_ws_clients`: rises to the full client count.
- `btick_ws_symbol_subscribers{symbol="BTC/USD"}`: equals the full client count.
- `btick_ws_fanout_total{type="latest_price"}`: increases by `clients * messages`.
- `btick_ws_type_drops_total`: stays at `0`.
- `btick_ws_type_evictions_total`: stays at `0`.

### `filtered_half`

- `btick_ws_clients`: still rises to the full client count.
- `btick_ws_symbol_subscribers{symbol="BTC/USD"}`: drops to the unfiltered half.
- `btick_ws_symbol_subscribers{symbol="ETH/USD"}`: remains at the full client count because unfiltered clients still accept all symbols.
- `btick_ws_fanout_total{type="latest_price"}`: increases by `(clients - filtered_clients) * messages`.
- `btick_ws_type_drops_total`: stays at `0`.
- `btick_ws_type_evictions_total`: stays at `0`.

### `slow_mix`

- `btick_ws_fanout_total{type="latest_price"}`: remains high, but below the theoretical max because slow clients are eventually evicted.
- `btick_ws_type_drops_total`: rises before evictions do.
- `btick_ws_type_evictions_total`: rises once slow clients exceed `slow_client_max_drops`.
- `btick_ws_symbol_subscribers{symbol="BTC/USD"}`: falls after eviction because the slow clients disappear from the hub.
- `btick_ws_coalesced_total`: usually remains unchanged in this harness because these scenarios exercise hub fan-out directly rather than engine burst coalescing. Validate coalescing separately during upstream burst tests.

## Observed Local Baseline

Local run on `2026-04-13` using:

```bash
RUN_WS_LOAD=1 go test -run TestWebSocketLoadScenarios -count=1 ./internal/api -v
```

| Scenario | Clients | Messages | Fan-out | Drops | Evictions | Final `btick_ws_clients` | Subscribers | Avg broadcast |
|----------|---------|----------|---------|-------|-----------|---------------------------|-------------|---------------|
| `all_fast` | 120 | 500 | 60000 | 0 | 0 | 120 | `BTC/USD=120`, `ETH/USD=120` | `25.6us` |
| `filtered_half` | 120 | 500 | 30000 | 0 | 0 | 120 | `BTC/USD=60`, `ETH/USD=120` | `11.6us` |
| `slow_mix` | 60 | 220 | 12203 | 310 | 10 | 50 | `BTC/USD=50`, `ETH/USD=50` | `7.5us` |

Interpretation:

- The `all_fast` run establishes the healthy baseline: full linear fan-out, no drops, no evictions.
- The `filtered_half` run demonstrates the value of symbol filtering directly: `BTC/USD` fan-out halves while total client count stays flat.
- The `slow_mix` run shows the slow-consumer progression: dropped messages appear first, then the exact slow cohort is evicted, and the subscriber gauge falls by the same amount.