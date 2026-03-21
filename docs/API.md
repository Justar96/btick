# btick API Documentation

**Version:** 1.0  
**Base URL:** `http://localhost:8080` (development) | `https://your-domain.com` (production)

---

## Overview

btick is a real-time Bitcoin price oracle service that aggregates prices from multiple exchanges (Binance, Coinbase, Kraken) and produces a canonical price using multi-venue median pricing. This service is designed for prediction market settlement and real-time price feeds.

### Key Features

- **Multi-venue median pricing** — Manipulation-resistant canonical price
- **Sub-second updates** — Real-time trade-by-trade price changes
- **1-second snapshots** — Stored for settlement and auditing
- **5-minute settlement prices** — For prediction market resolution
- **Quality scoring** — Data freshness and source count metrics
- **Outlier rejection** — 1% deviation filter

---

## REST API Endpoints

### Health & Status

#### GET /v1/health

System health check with latest price status.

**Response:**

```json
{
  "status": "ok",
  "timestamp": "2026-03-19T09:10:00.123456789Z",
  "latest_price": "70105.45",
  "latest_ts": "2026-03-19T09:09:59.876543Z",
  "source_count": 3
}
```

**Status values:**
| Status | Description |
|--------|-------------|
| `ok` | All systems healthy, fresh data |
| `degraded` | Fewer than minimum sources available |
| `stale` | No fresh data, using carry-forward |
| `no_data` | No price data available yet |

---

#### GET /v1/health/feeds

Per-source feed health status.

**Response:**

```json
[
  {
    "source": "binance",
    "conn_state": "connected",
    "last_message_ts": "2026-03-19T09:10:00.123Z",
    "last_trade_ts": "2026-03-19T09:10:00.100Z",
    "median_lag_ms": 45,
    "reconnect_count_1h": 0,
    "consecutive_errors": 0,
    "stale": false,
    "updated_at": "2026-03-19T09:10:00.123456789Z"
  },
  {
    "source": "coinbase",
    "conn_state": "connected",
    "last_message_ts": "2026-03-19T09:10:00.089Z",
    "stale": false,
    "updated_at": "2026-03-19T09:10:00.123456789Z"
  },
  {
    "source": "kraken",
    "conn_state": "connected",
    "last_message_ts": "2026-03-19T09:09:59.950Z",
    "stale": false,
    "updated_at": "2026-03-19T09:10:00.123456789Z"
  }
]
```

---

### Price Data

#### GET /v1/price/latest

Get the current canonical BTC/USD price (from memory, lowest latency).

**Response:**

```json
{
  "symbol": "BTC/USD",
  "ts": "2026-03-19T09:10:00.123456789Z",
  "price": "70105.45",
  "basis": "median_trade",
  "is_stale": false,
  "is_degraded": false,
  "quality_score": 0.9556,
  "source_count": 3,
  "sources_used": ["binance", "coinbase", "kraken"]
}
```

**Field descriptions:**

| Field | Type | Description |
|-------|------|-------------|
| `symbol` | string | Canonical symbol (always `BTC/USD`) |
| `ts` | string | Timestamp of the price event (RFC3339Nano) |
| `price` | string | Canonical price in USD (decimal string for precision) |
| `basis` | string | How the price was computed (see below) |
| `is_stale` | boolean | True if data is outdated (carry-forward) |
| `is_degraded` | boolean | True if fewer than minimum sources |
| `quality_score` | float | 0-1 quality metric |
| `source_count` | int | Number of sources used |
| `sources_used` | array | List of source names |

**Basis values:**

| Basis | Description |
|-------|-------------|
| `median_trade` | Median of trade prices from multiple venues |
| `median_mixed` | Median including midpoint fallbacks |
| `single_trade` | Only one venue had fresh trade data |
| `single_midpoint` | Only midpoint data available |
| `carry_forward` | No fresh data, using last known price |

---

#### GET /v1/price/settlement

**⭐ PRIMARY ENDPOINT FOR MARKET SETTLEMENT**

Get the official settlement price at a specific 5-minute boundary.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `ts` | string | Yes | Settlement timestamp in RFC3339 format. Must be on a 5-minute boundary (e.g., `2026-03-19T09:05:00Z`, `2026-03-19T09:10:00Z`) |

**Example Request:**

```
GET /v1/price/settlement?ts=2026-03-19T09:10:00Z
```

**Success Response (200):**

```json
{
  "settlement_ts": "2026-03-19T09:10:00Z",
  "symbol": "BTC/USD",
  "price": "70105.45",
  "status": "confirmed",
  "basis": "median_trade",
  "quality_score": 0.9556,
  "source_count": 3,
  "sources_used": ["binance", "coinbase", "kraken"],
  "finalized_at": "2026-03-19T09:10:01.251996789Z",
  "source_details": "eyJiaW5hbmNlIjp7InByaWNlIjoiNzAxMDUuNDUiLCJ0cyI6IjIwMjYtMDMtMTlUMDk6MDk6NTkuOTk5WiJ9fQ=="
}
```

**Status values:**

| Status | Description | Action |
|--------|-------------|--------|
| `confirmed` | High quality, multi-source price | ✅ Safe to use for settlement |
| `degraded` | Fewer than minimum sources | ⚠️ Use with caution, may want manual review |
| `stale` | No fresh data at settlement time | ❌ Consider dispute/manual resolution |

**Error Responses:**

| Code | Error | Description |
|------|-------|-------------|
| 400 | `ts parameter required (RFC3339 format, e.g. 2026-03-19T09:05:00Z)` | Missing timestamp parameter |
| 400 | `invalid ts format, use RFC3339 (e.g. 2026-03-19T09:05:00Z)` | Use RFC3339 format |
| 400 | `ts must be on a 5-minute boundary (e.g. 09:05:00, 09:10:00)` | e.g., 09:05:00, 09:10:00 |
| 400 | `ts cannot be in the future` | Cannot query future prices |
| 425 | `settlement price not yet finalized, wait a few seconds` | Wait at least 5 seconds after the boundary |
| 404 | `settlement price not found for this timestamp` | No data for this timestamp |

**Integration Example (Go):**

```go
func getSettlementPrice(marketCloseTime time.Time) (*SettlementPrice, error) {
    url := fmt.Sprintf("https://price-oracle.example.com/v1/price/settlement?ts=%s", 
        marketCloseTime.UTC().Format(time.RFC3339))
    
    resp, err := http.Get(url)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != 200 {
        return nil, fmt.Errorf("settlement failed: %d", resp.StatusCode)
    }
    
    var result SettlementPrice
    json.NewDecoder(resp.Body).Decode(&result)
    
    // Validate quality
    if result.Status == "stale" {
        return nil, errors.New("settlement price is stale, manual review required")
    }
    
    return &result, nil
}
```

---

#### GET /v1/price/snapshots

Query historical 1-second snapshots.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `start` | string | Yes | Start time (RFC3339) |
| `end` | string | No | End time (RFC3339), defaults to now |

**Example Request:**

```
GET /v1/price/snapshots?start=2026-03-19T09:00:00Z&end=2026-03-19T09:05:00Z
```

**Response:**

```json
[
  {
    "ts_second": "2026-03-19T09:00:00Z",
    "symbol": "BTC/USD",
    "price": "70100.00",
    "basis": "median_trade",
    "is_stale": false,
    "is_degraded": false,
    "quality_score": 0.95,
    "source_count": 3,
    "sources_used": ["binance", "coinbase", "kraken"],
    "finalized_at": "2026-03-19T09:00:01.250Z"
  },
  {
    "ts_second": "2026-03-19T09:00:01Z",
    "symbol": "BTC/USD",
    "price": "70101.50",
    "basis": "median_trade",
    "quality_score": 0.94,
    "source_count": 3,
    "sources_used": ["binance", "coinbase", "kraken"],
    "finalized_at": "2026-03-19T09:00:02.250Z"
  }
]
```

---

#### GET /v1/price/ticks

Query recent canonical price change events.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `limit` | int | No | Number of ticks to return (default: 100, max: 1000) |

**Response:**

```json
[
  {
    "ts": "2026-03-19T09:10:00.123456789Z",
    "symbol": "BTC/USD",
    "price": "70105.45",
    "basis": "median_trade",
    "is_stale": false,
    "is_degraded": false,
    "quality_score": 0.9556,
    "source_count": 3,
    "sources_used": ["binance", "coinbase", "kraken"]
  }
]
```

---

#### GET /v1/price/raw

Query raw tick data from individual exchanges (for debugging/auditing).

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `source` | string | No | Filter by source (binance, coinbase, kraken) |
| `start` | string | No | Start time (RFC3339) |
| `end` | string | No | End time (RFC3339) |
| `limit` | int | No | Number of events (default: 100) |

**Response:**

```json
[
  {
    "event_id": "01956789-abcd-7000-8000-000000000001",
    "source": "binance",
    "event_type": "trade",
    "exchange_ts": "2026-03-19T09:10:00.123456789Z",
    "recv_ts": "2026-03-19T09:10:00.145678901Z",
    "price": "70105.45",
    "size": "0.5",
    "side": "buy",
    "trade_id": "123456789"
  }
]
```

---

## WebSocket API

### Connection

```
ws://localhost:8080/ws/price
wss://your-domain.com/ws/price
```

### Connection Lifecycle

On connect, the server sends two messages before any live broadcast data:

**1. Welcome message** (always first):
```json
{
  "type": "welcome",
  "ts": "2026-03-19T09:10:00.123456789Z",
  "message": "btick/v1"
}
```

**2. Initial state** (current price, or no-data indicator):
```json
{
  "type": "latest_price",
  "ts": "2026-03-19T09:10:00.123456789Z",
  "price": "70105.45",
  "basis": "median_trade",
  "quality_score": "0.9556",
  "source_count": 3,
  "sources_used": ["binance", "coinbase", "kraken"],
  "message": "initial_state"
}
```

If no data is available yet:
```json
{
  "type": "latest_price",
  "ts": "2026-03-19T09:10:00.123456789Z",
  "message": "no_data_yet"
}
```

Welcome and initial state messages do **not** carry a `seq` field — they are per-connection, not broadcast.

After these two messages, live broadcast data begins flowing.

---

### Message Types (Server → Client)

All broadcast messages carry a monotonically increasing `seq` (uint64) for gap detection.

#### 1. Real-time Price Updates (`latest_price`)

Sent on every trade that changes the canonical price (sub-second).

```json
{
  "type": "latest_price",
  "seq": 42,
  "ts": "2026-03-19T09:10:00.123456789Z",
  "price": "70105.45",
  "basis": "median_trade",
  "quality_score": "0.9556",
  "source_count": 3,
  "sources_used": ["binance", "coinbase", "kraken"],
  "is_stale": false
}
```

#### 2. 1-Second Snapshots (`snapshot_1s`)

Sent every second with the finalized price for that second.

```json
{
  "type": "snapshot_1s",
  "seq": 43,
  "ts": "2026-03-19T09:10:00Z",
  "price": "70105.45",
  "basis": "median_trade",
  "quality_score": "0.9556",
  "source_count": 3,
  "sources_used": ["binance", "coinbase", "kraken"],
  "is_stale": false
}
```

#### 3. Heartbeat (`heartbeat`)

Sent at a configurable interval (default 5 seconds). Carries `seq` so clients can confirm their sequence is current during quiet periods.

```json
{
  "type": "heartbeat",
  "seq": 44,
  "ts": "2026-03-19T09:10:05.000000000Z"
}
```

---

### Sequence Numbers

All broadcast messages (including heartbeats) share a single monotonically increasing `seq` counter. Clients detect gaps to know when messages were missed:

- Received `seq: 10` then `seq: 13` → missed 2 messages
- Action: call `GET /v1/price/latest` to resync current state

With subscription filtering, clients subscribed to a subset of message types will naturally see `seq` gaps — this is expected and not an indication of dropped messages.

---

### Subscription Filtering (Client → Server)

Clients can subscribe/unsubscribe from specific message types. By default, all types are subscribed (backward-compatible — a client that sends nothing receives everything).

**Subscribe:**
```json
{"action": "subscribe", "types": ["snapshot_1s", "latest_price", "heartbeat"]}
```

**Unsubscribe:**
```json
{"action": "unsubscribe", "types": ["snapshot_1s"]}
```

**Available types:** `snapshot_1s`, `latest_price`, `heartbeat`

Unknown actions and types are silently ignored (forward-compatible).

**Example — chart client that only needs snapshots:**
```json
{"action": "unsubscribe", "types": ["latest_price", "heartbeat"]}
```

---

### Connection Handling

| Feature | Default | Configurable |
|---------|---------|--------------|
| Send buffer per client | 256 messages | `server.ws.send_buffer_size` |
| Application heartbeat | Every 5s | `server.ws.heartbeat_interval_sec` |
| WebSocket ping | Every 30s | `server.ws.ping_interval_sec` |
| Read deadline (pong timeout) | 60s | `server.ws.read_deadline_sec` |

- **Drop handling:** If a client's send buffer fills (slow consumer), messages are dropped silently rather than blocking other clients. Drops are logged server-side every 100 occurrences.
- **Reconnection:** Client should implement exponential backoff. On reconnect, the client receives a fresh welcome + initial state.

---

### JavaScript Example

```javascript
class BTCPriceSocket {
  constructor(url) {
    this.url = url;
    this.reconnectDelay = 1000;
    this.maxReconnectDelay = 30000;
    this.lastSeq = 0;
    this.connect();
  }

  connect() {
    this.ws = new WebSocket(this.url);

    this.ws.onopen = () => {
      console.log('Connected to BTC price feed');
      this.reconnectDelay = 1000;

      // Optional: only subscribe to what you need
      // this.ws.send(JSON.stringify({
      //   action: 'unsubscribe',
      //   types: ['snapshot_1s']
      // }));
    };

    this.ws.onmessage = (event) => {
      const msg = JSON.parse(event.data);

      // Detect gaps in sequence numbers
      if (msg.seq) {
        if (this.lastSeq > 0 && msg.seq > this.lastSeq + 1) {
          console.warn(`Missed ${msg.seq - this.lastSeq - 1} messages`);
          // Optionally resync: fetch('/v1/price/latest')
        }
        this.lastSeq = msg.seq;
      }

      switch (msg.type) {
        case 'welcome':
          console.log('Server:', msg.message);
          break;
        case 'latest_price':
          if (msg.message === 'initial_state') {
            this.onInitialState(msg);
          } else if (msg.message === 'no_data_yet') {
            console.log('Waiting for first price data...');
          } else {
            this.onPriceUpdate(msg);
          }
          break;
        case 'snapshot_1s':
          this.onSnapshot(msg);
          break;
        case 'heartbeat':
          // Connection alive confirmation
          break;
      }
    };

    this.ws.onclose = () => {
      console.log(`Reconnecting in ${this.reconnectDelay}ms...`);
      setTimeout(() => this.connect(), this.reconnectDelay);
      this.reconnectDelay = Math.min(this.reconnectDelay * 2, this.maxReconnectDelay);
    };

    this.ws.onerror = (err) => {
      console.error('WebSocket error:', err);
      this.ws.close();
    };
  }

  onInitialState(msg) {
    console.log(`Initial price: $${msg.price} (${msg.source_count} sources)`);
  }

  onPriceUpdate(msg) {
    console.log(`Price: $${msg.price} [seq:${msg.seq}]`);
  }

  onSnapshot(msg) {
    console.log(`Snapshot: $${msg.price} @ ${msg.ts} [seq:${msg.seq}]`);
  }
}

// Usage
const priceSocket = new BTCPriceSocket('wss://price-oracle.example.com/ws/price');
```

---

## Integration Guide for Market Service

### Prediction Market Flow

```
┌──────────────────────────────────────────────────────────────────────┐
│                        5-Minute Market Lifecycle                      │
├──────────────────────────────────────────────────────────────────────┤
│                                                                       │
│  09:05:00 ─────────────────────────────────────────────── 09:10:00   │
│     │                                                         │       │
│     │  Market Open                                   Market Close    │
│     │                                                         │       │
│     │  ┌─────────────────────────────────────────────────┐   │       │
│     │  │ WS /ws/price → Display live price to traders    │   │       │
│     │  └─────────────────────────────────────────────────┘   │       │
│     │                                                         │       │
│     │                                           GET /v1/price/settlement
│     │                                           ?ts=2026-03-19T09:10:00Z
│     │                                                         │       │
│     │                                                         ▼       │
│     │                                              ┌──────────────┐   │
│     │                                              │ Settlement   │   │
│     │                                              │ price=$70105 │   │
│     │                                              └──────────────┘   │
│     │                                                         │       │
│     │                                                         ▼       │
│     │                                              ┌──────────────┐   │
│     │                                              │ Resolve bets │   │
│     │                                              │ Pay winners  │   │
│     │                                              └──────────────┘   │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘
```

### Settlement Integration Checklist

1. **Wait for finalization** — Call settlement endpoint at least 5 seconds after market close
2. **Check status** — Only auto-settle if `status === "confirmed"`
3. **Handle degraded** — Queue for manual review if `status === "degraded"`
4. **Handle stale** — Trigger dispute flow if `status === "stale"`
5. **Store audit trail** — Save `source_details` and `finalized_at` for disputes

### Sample Settlement Logic

```go
func settleMarket(marketID string, closeTime time.Time) error {
    // Wait for finalization
    time.Sleep(5 * time.Second)
    
    // Get settlement price
    settlement, err := priceOracle.GetSettlement(closeTime)
    if err != nil {
        return fmt.Errorf("failed to get settlement: %w", err)
    }
    
    // Validate quality
    switch settlement.Status {
    case "confirmed":
        // Auto-settle
        return db.SettleMarket(marketID, settlement.Price, settlement)
        
    case "degraded":
        // Queue for review
        return db.QueueForReview(marketID, settlement, "degraded_quality")
        
    case "stale":
        // Trigger dispute
        return db.TriggerDispute(marketID, settlement, "stale_price_data")
        
    default:
        return fmt.Errorf("unknown settlement status: %s", settlement.Status)
    }
}
```

---

## Data Quality

### Quality Score

The `quality_score` (0-1) is computed based on:

| Factor | Weight | Description |
|--------|--------|-------------|
| Source count | 50% | More sources = higher score (max at 3) |
| Data freshness | 30% | Newer data = higher score |
| Data type | 20% | Trade prices preferred over midpoints |

**Recommended thresholds:**

| Score | Quality | Recommended Action |
|-------|---------|-------------------|
| ≥ 0.8 | High | Auto-settle |
| 0.5-0.8 | Medium | Auto-settle with monitoring |
| < 0.5 | Low | Manual review recommended |

### Stale Data Handling

Data is considered stale when:
- No fresh trades within the trade freshness window (default 2 seconds, configurable via `trade_freshness_window_ms`)
- The canonical price age exceeds the stale threshold (default 3 seconds, configurable via `canonical_stale_after_ms`)
- All venue connections are down

When stale, the system carries forward the last known price for up to 10 seconds (configurable via `carry_forward_max_seconds`).

---

## Rate Limits

No application-level rate limiting is currently implemented. Consider adding a reverse proxy (e.g., nginx, Caddy) for production rate limiting.

| Endpoint | Notes |
|----------|-------|
| REST APIs | No built-in rate limit |
| WebSocket | No connection limit enforced (slow consumers are dropped) |

---

## Error Handling

All errors return JSON with an `error` field:

```json
{
  "error": "description of the error"
}
```

Common HTTP status codes:

| Code | Meaning |
|------|---------|
| 200 | Success |
| 400 | Bad request (invalid parameters) |
| 404 | Not found (no data for timestamp) |
| 425 | Too early (data not finalized) |
| 500 | Internal server error |
| 503 | Service unavailable (database down) |

---

## Changelog

### v1.1 (2026-03-21)
- WebSocket: welcome message + initial state on connect (no more ~1s wait)
- WebSocket: sequence numbers on all broadcast messages for gap detection
- WebSocket: subscription filtering (`subscribe`/`unsubscribe` actions)
- WebSocket: application-level heartbeat (default 5s, configurable)
- WebSocket: per-client drop logging
- WebSocket: configurable send buffer, ping interval, read deadline
- API: `Store`/`Engine` interfaces for testability (100% test coverage)

### v1.0 (2026-03-19)
- Initial release
- Multi-venue median pricing (Binance, Coinbase, Kraken)
- WebSocket real-time feed
- Settlement price endpoint for 5-minute markets
- Quality scoring and stale detection
