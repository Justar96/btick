# WS-First Data Alignment Design

**Date:** 2026-04-12
**Status:** Approved
**Goal:** Eliminate price drift between components, remove redundant REST polling, align all real-time data through a single WebSocket stream, and apply TanStack Query best practices.

---

## Problem

The frontend has two parallel data paths for price data:

1. **WebSocket** delivers canonical prices (`latest_price`, `snapshot_1s`) to `PriceDisplay` and `PriceChart`
2. **REST polling** delivers per-source raw prices (`/v1/price/raw` every 2s) and feed health (`/v1/health/feeds` every 5s) to `SourcePanel`

This creates up to 2-second drift between the canonical price and source prices, making delta calculations unreliable. The chart also bypasses TanStack Query entirely with a raw `fetch()` call, missing cache benefits.

## Solution: WS-First Architecture

WebSocket becomes the single real-time source of truth. TanStack Query handles only historical and on-demand data.

---

## 1. Backend — New WS Message Types

### `source_price`

Emitted from `SnapshotEngine.updateVenueState()` when a trade arrives. Throttled to max 1 message per source per 500ms (keeps latest price if multiple trades arrive in the window).

```json
{
  "type": "source_price",
  "seq": 12345,
  "symbol": "BTC/USD",
  "source": "binance",
  "ts": "2026-04-12T10:30:00.123456789Z",
  "price": "84321.50"
}
```

### `source_status`

Emitted on feed connection state transitions only (connected/disconnected/stale).

```json
{
  "type": "source_status",
  "seq": 12346,
  "symbol": "BTC/USD",
  "source": "kraken",
  "ts": "2026-04-12T10:30:01.000000000Z",
  "conn_state": "disconnected",
  "stale": true
}
```

### Subscription

Both types are opt-in (default: not subscribed) so existing clients are unaffected. Frontend subscribes on connect:

```json
{ "action": "subscribe", "types": ["latest_price", "snapshot_1s", "source_price", "source_status"] }
```

### Implementation Touch Points

- `WSMessage` struct: add `Source string` and `ConnState string` fields
- `subscriptions` struct: add `sourcePrice` and `sourceStatus` booleans (default `false`)
- `SnapshotEngine`: new output channel `sourcePriceCh chan domain.SourcePriceEvent` with per-source 500ms throttle
- `MultiEngine`: merge source price channels from all per-symbol engines
- `Server.broadcastLoop`: read from new channel, broadcast `source_price` messages
- Feed health state change detection: adapter or metrics layer broadcasts `source_status` via the hub
- `domain` package: new `SourcePriceEvent` type

---

## 2. Frontend — WS Context Expansion

### New State

```typescript
interface WSContextValue {
  prices: Record<string, PriceState>;                         // existing
  connected: boolean;                                          // existing
  snapshots: Record<string, Array<{ ts: number; price: number }>>;  // existing
  sourcePrices: Record<string, Record<string, SourcePrice>>;  // new: [symbol][source]
  sourceStatus: Record<string, Record<string, SourceStatus>>; // new: [symbol][source]
}

interface SourcePrice {
  source: string;
  price: string;
  ts: string;
}

interface SourceStatus {
  source: string;
  connState: string;
  stale: boolean;
  ts: string;
}
```

### New Hooks

- `useSourcePrices(symbol)` — returns `Record<string, SourcePrice>`
- `useSourceStatus(symbol)` — returns `Record<string, SourceStatus>`

### Message Handling

- `source_price` message → update `sourcePrices[symbol][source]`
- `source_status` message → update `sourceStatus[symbol][source]`

### WS Subscription

On connect, subscribe to all four types: `latest_price`, `snapshot_1s`, `source_price`, `source_status`.

---

## 3. Frontend — TanStack Query Cleanup

### Query Changes

| Query | Before | After |
|-------|--------|-------|
| `rawTicksOptions` | `refetchInterval: 2000ms` | **Delete entirely.** Replaced by `useSourcePrices()` |
| `feedHealthOptions` | `refetchInterval: 5000ms` | **Remove polling.** On-demand only, `staleTime: 30000ms` |
| `latestPriceOptions` | `staleTime: 1000ms` | Add `placeholderData` from WS. Bump `staleTime: 30000ms`. Fix: pass `symbol` to API call |
| `snapshotsOptions` | Defined but unused by chart | **Now used by chart.** `staleTime: Infinity` |
| `ticksOptions` | `staleTime: 2000ms` | Keep as-is |
| `healthOptions` | `staleTime: 3000ms` | Keep as-is |
| `symbolsOptions` | `staleTime: 60000ms` | Keep as-is |

### Default Options

```typescript
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      refetchOnWindowFocus: true,
      retry: 2,
    },
  },
});
```

### WS to Query Cache Bridge

`WebSocketProvider` accesses `queryClient` via `useQueryClient()`:

- `latest_price` → `queryClient.setQueryData(["price", "latest", symbol], data)`
- `snapshot_1s` → append to active snapshots query cache entry

---

## 4. Frontend — Chart Data Unification

### Before

`PriceChart` uses raw `fetch()` in a `useEffect`, manages `backfill` state, manually merges with WS snapshots using a `Set` dedup.

### After

Chart uses TanStack Query for backfill. WS appends go through `queryClient.setQueryData`.

**Data flow:**

```
Mount / window change
  → useQuery(snapshotsOptions(start, end))
  → returns cached historical points (staleTime: Infinity)

WS snapshot_1s arrives
  → WebSocketProvider calls queryClient.setQueryData to append
  → component re-renders from query cache
```

**Benefits:**
- Switching time windows (1m/5m/1h) hits different cache entries — switching back is instant
- No manual dedup logic in component
- Single source of truth for chart data

**Snapshot append strategy:**
- WebSocketProvider does NOT need to know the chart's active window. Instead, it appends to a dedicated rolling query key: `["price", "snapshots", "live"]`
- `PriceChart` merges two query results: `useQuery(snapshotsOptions(start, end))` for backfill + `queryClient.getQueryData(["price", "snapshots", "live"])` for the WS tail
- On `snapshot_1s`, WebSocketProvider appends to `["price", "snapshots", "live"]` and caps at 3600 points
- Dedup by rounded timestamp happens once in `PriceChart`'s merge, same as today but reading from query cache instead of context state

**On WS reconnect:** `queryClient.invalidateQueries({ queryKey: ["price", "snapshots"] })` to trigger gap-filling refetch.

---

## 5. Frontend — Component Rewiring

### SourcePanel

```
Before: useQuery(rawTicksOptions) → poll REST → parse per source
After:  useSourcePrices(symbol)   → WS real-time → already per source
        useSourceStatus(symbol)   → WS real-time → replaces feed polling for status
        useQuery(feedHealthOptions()) → on-demand, no polling → latency detail
```

- Remove `useQuery(rawTicksOptions(...))` entirely
- Remove `refetchInterval` from `feedHealthOptions`
- Source prices from `useSourcePrices(symbol)` — keyed by source, no parsing loop
- Status dots from `useSourceStatus(symbol)` — `connState` and `stale` map to existing color logic
- Latency (`median_lag_ms`) fetched on-demand via REST (not on WS)

### PriceDisplay

No change. Already reads from `usePrice(symbol)`.

### PriceChart

Covered in Section 4. Uses `useQuery(snapshotsOptions(...))` instead of raw fetch.

### HealthTable

Remove `refetchInterval` from `feedHealthOptions`. On-demand fetch on mount. WS `source_status` provides real-time status.

### Sidebar

No change.

---

## 6. Error Handling & Fallback

### WS Disconnect Behavior

- **Canonical price:** Last WS value persists in context. Not cleared on disconnect. Sidebar shows connection status via `useWSConnected()`. On reconnect, server sends `initial_state`.
- **Source prices:** Last known values persist. On reconnect, fresh data arrives immediately.
- **Chart:** TanStack Query cache persists. Gap in live points is inherent. On reconnect, `invalidateQueries(["price", "snapshots"])` triggers gap-filling refetch.
- **Health detail:** REST-based, independent of WS state. Always available.

### No Automatic REST Polling Fallback

No `refetchInterval` timers spin up when WS drops. If WS is down, the data source is the problem. Polling REST for the same backend doesn't improve the situation. The honest UX is "connection lost, reconnecting..." which the Sidebar already shows.

---

## Files Changed

### Backend (Go)

| File | Change |
|------|--------|
| `internal/domain/types.go` | Add `SourcePriceEvent` type |
| `internal/api/websocket.go` | Add `Source`, `ConnState` to `WSMessage`; add subscription types |
| `internal/engine/snapshot.go` | Add `sourcePriceCh`, per-source 500ms throttle in `updateVenueState` |
| `internal/engine/multi.go` | Merge `sourcePriceCh` channels; expose `SourcePriceCh()` |
| `internal/api/server.go` | Add `Engine.SourcePriceCh()` to interface; read in `broadcastLoop` |
| `internal/adapter/base.go` | Add callback in `setConnState()` to notify hub of state transitions for `source_status` broadcast |

### Frontend (TypeScript/React)

| File | Change |
|------|--------|
| `web/src/ws/types.ts` | Add `source_price`, `source_status` to `WSMessage` union; add `SourcePrice`, `SourceStatus` interfaces |
| `web/src/ws/context.tsx` | Add `sourcePrices`, `sourceStatus` state; new hooks; `queryClient.setQueryData` bridge |
| `web/src/api/queries.ts` | Delete `rawTicksOptions`; remove polling from `feedHealthOptions`; fix `latestPriceOptions` symbol param; bump stale times |
| `web/src/main.tsx` | Update `QueryClient` default `staleTime` to 30s |
| `web/src/components/SourcePanel.tsx` | Replace REST queries with `useSourcePrices()` + `useSourceStatus()` |
| `web/src/components/PriceChart.tsx` | Replace raw fetch with `useQuery(snapshotsOptions(...))` |
| `web/src/components/HealthTable.tsx` | Remove `refetchInterval` from feed health query |
