# API Reference Page — Design Spec

## Context

The `/api` route currently has a latency bar header and an `IntegrateSection` component with hand-built `<div>` code blocks showing curl examples and a bare endpoint table. It needs to become a professional, curated API reference page — not a full OpenAPI renderer, but polished docs for the endpoints integrators actually use. Aligned to the existing paper-tone minimal design (660px content, `#fafafa` bg, thin dividers).

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Scope | Curated highlights, not full reference | Focus on the 4-5 endpoints integrators use. Not every field of every schema. |
| Endpoint layout | Compact inline | Method badge + path, description, inline params, `<pre><code>` block, error codes. No bordered cards. Matches the document flow. |
| Page structure | Drop latency bars, pure reference | Latency header removed. Clean title + base URL header instead. Professional. |
| WebSocket section | Connection flow + message table | Narrative lifecycle (connect → welcome → initial state → live), JS example, message types as a compact table. Protocol-doc feel. |

## Page Structure (top to bottom)

### 1. Header

- Title: "API Reference"
- One-line description: "REST endpoints and WebSocket streams for integrating btick price data."
- Base URL displayed in a mono code block: `https://btick-production.up.railway.app`
- Transport note: all responses `application/json`, CORS `*`

### 2. Latest Price — `GET /v1/price/latest`

- Description: current canonical price from memory, lowest latency
- Params: `symbol` (optional, defaults to first configured)
- Code block: curl request + JSON response showing symbol, price, basis, quality_score, source_count, sources_used
- Error: `503` no data yet

### 3. Settlement Price — `GET /v1/price/settlement`

- Description: emphasize this is the **primary endpoint for prediction market resolution**
- Params: `ts` (required) — RFC3339, must be on 5-minute boundary
- Code block: curl + full response with settlement_ts, price, status, basis, source_count
- Status values table: confirmed (safe to auto-settle), degraded (manual review), stale (dispute flow)
- Error codes: compact line — `400` invalid format, `425` not finalized, `404` not found

### 4. Snapshots & Ticks

**`GET /v1/price/snapshots`**
- Params: `start` (required), `end` (optional, defaults to now)
- Code block: curl + abbreviated response

**`GET /v1/price/ticks`**
- Params: `limit` (optional, default 100, max 1000)
- Code block: curl + abbreviated response

### 5. WebSocket — `WS /ws/price`

- Connection URL in code block
- Connection lifecycle narrative: connect → welcome message → initial state (or `no_data_yet`) → live broadcast begins
- JS connect example: `new WebSocket(url)`, `onmessage` handler parsing message types, reconnect with backoff
- Message types table:

| Type | Direction | Key Fields |
|------|-----------|------------|
| `welcome` | server→client | message ("btick/v1") |
| `latest_price` | server→client | price, basis, quality_score, source_count |
| `snapshot_1s` | server→client | price, basis, ts |
| `heartbeat` | server→client | seq, ts |
| `source_price` | server→client | source, price |
| `source_status` | server→client | source, state |

- Subscribe/unsubscribe example: JSON for filtering message types and symbols
- Sequence numbers note: monotonic `seq` on broadcast messages, gaps indicate missed messages

### 6. Health — `GET /v1/health` + `GET /v1/health/feeds`

Light treatment:
- `GET /v1/health`: response example, status values (ok, degraded, stale, no_data)
- `GET /v1/health/feeds`: brief description, response example showing per-source conn_state + staleness

### 7. All Endpoints — Summary Table

Improved version of the current table. Every endpoint in one glanceable list:

| Method | Path | Description |
|--------|------|-------------|
| GET | /v1/price/latest | Current canonical price |
| GET | /v1/price/settlement | Settlement price at 5-min boundary |
| GET | /v1/price/snapshots | Historical 1s snapshots |
| GET | /v1/price/ticks | Recent price changes |
| GET | /v1/price/raw | Raw exchange data |
| GET | /v1/health | System health |
| GET | /v1/health/feeds | Per-source feed health |
| GET | /v1/symbols | Configured symbols |
| WS | /ws/price | Real-time price stream |

Method badges use green for GET, indigo for WS (existing style).

## Per-Endpoint Format

Each endpoint section follows this structure:

1. **Method badge + path** — `<span>GET</span> <code>/v1/price/latest</code>` on one line
2. **Description** — 1-2 sentences, 12-13px muted text
3. **Parameters** — inline list, not a full table. Each param: `name` + required/optional badge + type + description on one line
4. **Code block** — `<pre><code>` with curl request + response JSON. Proper preformatted text, not `<div>` elements
5. **Error codes** — compact single line: `400 description · 503 description`

Sections separated by `1px solid var(--border-light)` with `32px` vertical padding.

## Implementation Details

### Files Changed

- `web/src/routes/api.tsx` — rewrite. Remove `useApiLatency`, remove `IntegrateSection` import. All content inline as a single component with proper semantic HTML.
- `web/src/routes/api.module.css` — rewrite. Remove latency styles. Add styles for endpoint sections, param lists, code blocks, method badges, message type table, summary table.

### Files Removed

- `web/src/components/IntegrateSection.tsx` — content absorbed into api.tsx
- `web/src/components/IntegrateSection.module.css` — no longer needed

### Style Tokens Used

All from `global.css`:
- `--bg`, `--surface`, `--text`, `--text-secondary`, `--text-muted`, `--text-faint`
- `--border`, `--border-light`
- `--mono`, `--sans`
- `--green`, `--red`

### Code Blocks

Replace hand-built `<div className={styles.indent}>` with proper:
```html
<pre class={styles.codeBlock}><code>{`curl .../v1/price/latest

{
  "symbol": "BTC/USD",
  "price": "71455.33",
  ...
}`}</code></pre>
```

### No New Dependencies

Pure React + CSS Modules. No syntax highlighting library needed — the code blocks are short JSON examples, readability comes from the mono font and indentation.

## What This Does NOT Include

- Full OpenAPI rendering or every response field documented
- Interactive "try it" functionality
- Syntax highlighting (unnecessary for short JSON examples)
- Dark/light theme toggle
- Raw exchange data (`/v1/price/raw`) gets mentioned in the summary table but no dedicated section — it's an audit/debug endpoint, not an integration endpoint
