import { useState, useEffect } from "react";
import { useLocation } from "@tanstack/react-router";
import styles from "./api.module.css";

export type ApiSection =
  | "latest"
  | "settlement"
  | "snapshots"
  | "websocket"
  | "health"
  | "endpoints";

const DEFAULT_SECTION: ApiSection = "latest";

const LATEST_EXAMPLE = `$ curl /v1/price/latest?symbol=BTC/USD

{
  "symbol": "BTC/USD",
  "ts": "2026-04-12T10:05:00.123456789Z",
  "price": "71455.33",
  "basis": "median_trade",
  "is_stale": false,
  "is_degraded": false,
  "quality_score": 0.9556,
  "source_count": 4,
  "sources_used": ["binance", "coinbase", "kraken", "okx"]
}`;

const SETTLEMENT_EXAMPLE = `$ curl /v1/price/settlement?ts=2026-04-12T10:05:00Z

{
  "settlement_ts": "2026-04-12T10:05:00Z",
  "symbol": "BTC/USD",
  "price": "71455.33",
  "status": "confirmed",
  "basis": "median_trade",
  "quality_score": 0.9556,
  "source_count": 4,
  "sources_used": ["binance", "coinbase", "kraken", "okx"],
  "finalized_at": "2026-04-12T10:05:01.251Z"
}`;

const SNAPSHOTS_EXAMPLE = `$ curl "/v1/price/snapshots?start=2026-04-12T10:00:00Z&end=2026-04-12T10:05:00Z"

[
  {
    "ts_second": "2026-04-12T10:00:00Z",
    "symbol": "BTC/USD",
    "price": "71440.10",
    "basis": "median_trade",
    "quality_score": 0.95,
    "source_count": 4
  },
  ...
]`;

const TICKS_EXAMPLE = `$ curl /v1/price/ticks?limit=5

[
  {
    "ts": "2026-04-12T10:05:00.123Z",
    "symbol": "BTC/USD",
    "price": "71455.33",
    "basis": "median_trade",
    "source_count": 4
  },
  ...
]`;

const WS_EXAMPLE = `const ws = new WebSocket("wss://btick-production.up.railway.app/ws/price")

let lastSeq = 0

ws.onmessage = (e) => {
  const msg = JSON.parse(e.data)

  // Detect missed messages via sequence gaps
  if (msg.seq && lastSeq > 0 && msg.seq > lastSeq + 1) {
    console.warn(\`Missed \${msg.seq - lastSeq - 1} messages\`)
  }
  if (msg.seq) lastSeq = msg.seq

  switch (msg.type) {
    case "welcome":
      console.log("Connected:", msg.message)
      break
    case "latest_price":
      if (msg.message === "initial_state") {
        console.log("Current price:", msg.price)
      } else {
        console.log("Price update:", msg.price)
      }
      break
    case "snapshot_1s":
      console.log("Snapshot:", msg.price, "@", msg.ts)
      break
  }
}

// Reconnect with backoff on close
ws.onclose = () => setTimeout(() => connect(), delay)`;

const WS_SUBSCRIBE_EXAMPLE = `// Only receive snapshots (unsubscribe from the rest)
ws.send(JSON.stringify({
  action: "unsubscribe",
  types: ["latest_price", "heartbeat"]
}))

// Subscribe to source-level data (opt-in)
ws.send(JSON.stringify({
  action: "subscribe",
  types: ["source_price", "source_status"]
}))

// Filter to specific symbols
ws.send(JSON.stringify({
  action: "subscribe",
  types: ["latest_price"],
  symbols: ["ETH/USD"]
}))`;

const HEALTH_EXAMPLE = `$ curl /v1/health

{
  "status": "ok",
  "timestamp": "2026-04-12T10:05:00.123Z",
  "dependencies": { "database": { "ready": true } },
  "latest_price": "71455.33",
  "source_count": 4
}`;

const FEEDS_EXAMPLE = `$ curl /v1/health/feeds

[
  {
    "source": "binance",
    "conn_state": "connected",
    "last_trade_ts": "2026-04-12T10:05:00.100Z",
    "median_lag_ms": 45,
    "stale": false
  },
  ...
]`;

const ALL_ENDPOINTS = [
  { method: "GET", path: "/v1/price/latest", desc: "Current canonical price" },
  { method: "GET", path: "/v1/price/settlement", desc: "Settlement price at 5-min boundary" },
  { method: "GET", path: "/v1/price/snapshots", desc: "Historical 1s snapshots" },
  { method: "GET", path: "/v1/price/ticks", desc: "Recent price changes" },
  { method: "GET", path: "/v1/price/raw", desc: "Raw exchange data (audit)" },
  { method: "GET", path: "/v1/health", desc: "System health" },
  { method: "GET", path: "/v1/health/feeds", desc: "Per-source feed health" },
  { method: "GET", path: "/v1/symbols", desc: "Configured symbols" },
  { method: "GET", path: "/metrics", desc: "Prometheus metrics" },
  { method: "WS", path: "/ws/price", desc: "Real-time price stream" },
];

function MethodBadge({ method }: { method: string }) {
  return (
    <span
      className={`${styles.method} ${method === "WS" ? styles.methodWs : styles.methodGet}`}
    >
      {method}
    </span>
  );
}

function parseHash(hash: string): ApiSection {
  const val = hash.replace("#", "") as ApiSection;
  const valid: ApiSection[] = [
    "latest",
    "settlement",
    "snapshots",
    "websocket",
    "health",
    "endpoints",
  ];
  return valid.includes(val) ? val : DEFAULT_SECTION;
}

function LatestSection() {
  return (
    <section className={styles.section}>
      <div className={styles.endpoint}>
        <MethodBadge method="GET" />
        <code className={styles.path}>/v1/price/latest</code>
      </div>
      <p className={styles.sectionDesc}>
        Current canonical price from memory. Lowest latency endpoint — no
        database required.
      </p>
      <div className={styles.params}>
        <div className={styles.paramsLabel}>Parameters</div>
        <div className={styles.param}>
          <code className={styles.paramName}>symbol</code>
          <span className={styles.paramOptional}>optional</span>
          <span className={styles.paramDesc}>
            Canonical symbol (e.g. BTC/USD). Defaults to first configured.
          </span>
        </div>
      </div>
      <pre className={styles.codeBlock}>{LATEST_EXAMPLE}</pre>
      <div className={styles.errors}>
        <span className={styles.errorCode}>503</span> no data available yet
      </div>
    </section>
  );
}

function SettlementSection() {
  return (
    <section className={styles.section}>
      <div className={styles.endpoint}>
        <MethodBadge method="GET" />
        <code className={styles.path}>/v1/price/settlement</code>
      </div>
      <p className={styles.sectionDesc}>
        Official settlement price at a 5-minute boundary.{" "}
        <strong>Primary endpoint for prediction market resolution.</strong>{" "}
        Wait at least 5 seconds after the boundary for finalization.
      </p>
      <div className={styles.params}>
        <div className={styles.paramsLabel}>Parameters</div>
        <div className={styles.param}>
          <code className={styles.paramName}>ts</code>
          <span className={styles.paramRequired}>required</span>
          <span className={styles.paramDesc}>
            RFC3339 timestamp on a 5-minute boundary (e.g.
            2026-04-12T10:05:00Z)
          </span>
        </div>
      </div>
      <pre className={styles.codeBlock}>{SETTLEMENT_EXAMPLE}</pre>
      <table className={styles.statusTable}>
        <thead>
          <tr>
            <th>Status</th>
            <th>Meaning</th>
            <th>Action</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td>confirmed</td>
            <td>Multi-source, high quality</td>
            <td>Safe to auto-settle</td>
          </tr>
          <tr>
            <td>degraded</td>
            <td>Fewer than minimum sources</td>
            <td>Manual review recommended</td>
          </tr>
          <tr>
            <td>stale</td>
            <td>No fresh data at settlement time</td>
            <td>Trigger dispute flow</td>
          </tr>
        </tbody>
      </table>
      <div className={styles.errors}>
        <span className={styles.errorCode}>400</span> invalid ts format{" · "}
        <span className={styles.errorCode}>400</span> not on 5-min boundary{" · "}
        <span className={styles.errorCode}>425</span> not yet finalized{" · "}
        <span className={styles.errorCode}>404</span> no data for timestamp
      </div>
    </section>
  );
}

function SnapshotsSection() {
  return (
    <section className={styles.section}>
      <div className={styles.endpoint}>
        <MethodBadge method="GET" />
        <code className={styles.path}>/v1/price/snapshots</code>
      </div>
      <p className={styles.sectionDesc}>
        Historical 1-second price snapshots in a time range.
      </p>
      <div className={styles.params}>
        <div className={styles.paramsLabel}>Parameters</div>
        <div className={styles.param}>
          <code className={styles.paramName}>start</code>
          <span className={styles.paramRequired}>required</span>
          <span className={styles.paramDesc}>Start time (RFC3339)</span>
        </div>
        <div className={styles.param}>
          <code className={styles.paramName}>end</code>
          <span className={styles.paramOptional}>optional</span>
          <span className={styles.paramDesc}>
            End time (RFC3339), defaults to now
          </span>
        </div>
      </div>
      <pre className={styles.codeBlock}>{SNAPSHOTS_EXAMPLE}</pre>

      <div className={styles.subEndpoint}>
        <MethodBadge method="GET" />
        <code className={styles.path}>/v1/price/ticks</code>
      </div>
      <p className={styles.sectionDesc}>
        Recent canonical price change events. Each tick represents a price
        movement.
      </p>
      <div className={styles.params}>
        <div className={styles.paramsLabel}>Parameters</div>
        <div className={styles.param}>
          <code className={styles.paramName}>limit</code>
          <span className={styles.paramOptional}>optional</span>
          <span className={styles.paramDesc}>
            Number of ticks (default 100, max 1000)
          </span>
        </div>
      </div>
      <pre className={styles.codeBlock}>{TICKS_EXAMPLE}</pre>
      <div className={styles.errors}>
        <span className={styles.errorCode}>503</span> database not available
      </div>
    </section>
  );
}

function WebSocketSection() {
  return (
    <section className={styles.section}>
      <div className={styles.endpoint}>
        <MethodBadge method="WS" />
        <code className={styles.path}>/ws/price</code>
      </div>
      <p className={styles.sectionDesc}>
        Real-time price stream over WebSocket. On connect, the server sends a{" "}
        <code>welcome</code> message followed by <code>initial_state</code>{" "}
        (the current price). Live broadcast data then flows continuously. All
        broadcast messages carry a monotonic <code>seq</code> number for gap
        detection.
      </p>

      <div className={styles.sectionSubtitle}>Connect</div>
      <pre className={styles.codeBlock}>{WS_EXAMPLE}</pre>

      <div className={styles.sectionSubtitle}>Message Types</div>
      <table className={styles.msgTable}>
        <thead>
          <tr>
            <th>Type</th>
            <th>Direction</th>
            <th>Key Fields</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td>welcome</td>
            <td>server → client</td>
            <td>message ("btick/v1")</td>
          </tr>
          <tr>
            <td>latest_price</td>
            <td>server → client</td>
            <td>price, basis, quality_score, source_count</td>
          </tr>
          <tr>
            <td>snapshot_1s</td>
            <td>server → client</td>
            <td>price, basis, ts</td>
          </tr>
          <tr>
            <td>heartbeat</td>
            <td>server → client</td>
            <td>seq, ts</td>
          </tr>
          <tr>
            <td>source_price</td>
            <td>server → client</td>
            <td>source, price (opt-in)</td>
          </tr>
          <tr>
            <td>source_status</td>
            <td>server → client</td>
            <td>source, state (opt-in)</td>
          </tr>
          <tr>
            <td>subscribe</td>
            <td>client → server</td>
            <td>types, symbols</td>
          </tr>
          <tr>
            <td>unsubscribe</td>
            <td>client → server</td>
            <td>types, symbols</td>
          </tr>
        </tbody>
      </table>

      <div className={styles.sectionSubtitle}>Subscription Filtering</div>
      <p className={styles.sectionDesc}>
        By default, clients receive <code>latest_price</code>,{" "}
        <code>snapshot_1s</code>, and <code>heartbeat</code>. Source-level
        types (<code>source_price</code>, <code>source_status</code>) are
        opt-in. Unknown types and symbols are silently ignored.
      </p>
      <pre className={styles.codeBlock}>{WS_SUBSCRIBE_EXAMPLE}</pre>
    </section>
  );
}

function HealthSection() {
  return (
    <section className={styles.section}>
      <div className={styles.endpoint}>
        <MethodBadge method="GET" />
        <code className={styles.path}>/v1/health</code>
      </div>
      <p className={styles.sectionDesc}>
        System health check. Returns overall status, database readiness, and
        latest price info.
      </p>
      <pre className={styles.codeBlock}>{HEALTH_EXAMPLE}</pre>
      <table className={styles.statusTable}>
        <thead>
          <tr>
            <th>Status</th>
            <th>Description</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td>ok</td>
            <td>All systems healthy, fresh data</td>
          </tr>
          <tr>
            <td>degraded</td>
            <td>Fewer than minimum sources available</td>
          </tr>
          <tr>
            <td>stale</td>
            <td>No fresh data, carry-forward active</td>
          </tr>
          <tr>
            <td>no_data</td>
            <td>No price data available yet</td>
          </tr>
        </tbody>
      </table>

      <div className={styles.subEndpoint}>
        <MethodBadge method="GET" />
        <code className={styles.path}>/v1/health/feeds</code>
      </div>
      <p className={styles.sectionDesc}>
        Per-source feed health. Shows connection state, last trade time, median
        lag, and staleness for each exchange.
      </p>
      <pre className={styles.codeBlock}>{FEEDS_EXAMPLE}</pre>
      <div className={styles.errors}>
        <span className={styles.errorCode}>503</span> database not available
      </div>
    </section>
  );
}

function EndpointsSection() {
  return (
    <section className={styles.section}>
      <div className={styles.sectionSubtitle}>All Endpoints</div>
      <div>
        {ALL_ENDPOINTS.map((ep) => (
          <div key={ep.path} className={styles.summaryRow}>
            <span
              className={`${styles.summaryMethod} ${ep.method === "WS" ? styles.methodWs : styles.methodGet}`}
            >
              {ep.method}
            </span>
            <span className={styles.summaryPath}>{ep.path}</span>
            <span className={styles.summaryDesc}>{ep.desc}</span>
          </div>
        ))}
      </div>
    </section>
  );
}

const SECTIONS: Record<ApiSection, React.FC> = {
  latest: LatestSection,
  settlement: SettlementSection,
  snapshots: SnapshotsSection,
  websocket: WebSocketSection,
  health: HealthSection,
  endpoints: EndpointsSection,
};

export function ApiPage() {
  const location = useLocation();
  const [active, setActive] = useState<ApiSection>(() =>
    parseHash(location.hash),
  );

  useEffect(() => {
    setActive(parseHash(location.hash));
  }, [location.hash]);

  const Section = SECTIONS[active];

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <h1 className={styles.title}>API Reference</h1>
        <p className={styles.desc}>
          REST endpoints and WebSocket streams for integrating btick price data.
        </p>
        <code className={styles.baseUrl}>
          https://btick-production.up.railway.app
        </code>
        <p className={styles.transport}>
          All responses <code>application/json</code>. CORS enabled for all
          origins.
        </p>
      </header>

      <Section />
    </div>
  );
}
