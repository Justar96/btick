# Deployment Guide

## Overview

btick can be deployed in multiple environments:
- Local development
- Railway (recommended for quick setup)
- Docker/Kubernetes
- Bare metal

---

## Prerequisites

- Go 1.23+
- PostgreSQL 17+ with TimescaleDB 2.24+ (or Railway Postgres with TimescaleDB)
- Outbound WebSocket access to:
  - `wss://stream.binance.com:9443`
  - `wss://advanced-trade-ws.coinbase.com`
  - `wss://ws.kraken.com`

---

## Local Development

### 1. Clone and Setup

```bash
git clone https://github.com/justar9/btick.git
cd btick
```

### 2. Copy Config

```bash
cp config.yaml.example config.yaml
# Edit config.yaml with your DATABASE_URL
```

### 3. Run with Local Database

```bash
# Start PostgreSQL (if not running)
docker run -d --name btc-postgres \
  -e POSTGRES_DB=btick \
  -e POSTGRES_USER=postgres \
  -e POSTGRES_PASSWORD=postgres \
  -p 5432:5432 \
  postgres:14

# Update config.yaml
# dsn: "postgres://postgres:postgres@localhost:5432/btick?sslmode=disable"

# Run the service
go run ./cmd/btick
```

### 4. Run with Railway Database

```bash
# Link to Railway project
railway link

# Run with Railway env vars
railway run go run ./cmd/btick
```

---

## Railway Deployment

### 1. Initial Setup

```bash
# Install Railway CLI
npm install -g @railway/cli

# Login
railway login

# Create new project (or link existing)
railway init
# OR
railway link
```

### 2. Add PostgreSQL

In Railway dashboard:
1. Click "New Service" → "Database" → "PostgreSQL"
2. Copy the `DATABASE_URL` from the service variables

### 3. Set Environment Variables

```bash
# Set DATABASE_URL (Railway auto-sets this if Postgres is in same project)
railway variables set DATABASE_URL="postgresql://..."

# Optional: Set PORT for Railway
railway variables set PORT=8080
```

### 4. Deploy

```bash
# Push to Railway
railway up

# Or connect GitHub for auto-deploy
railway service
# Link to GitHub repo
```

### 5. Railway Configuration

Create `railway.toml` in project root:

```toml
[build]
builder = "nixpacks"

[deploy]
startCommand = "./btick"
healthcheckPath = "/v1/health"
healthcheckTimeout = 30
restartPolicyType = "ON_FAILURE"
restartPolicyMaxRetries = 3
```

Or use the project `Dockerfile`:

```dockerfile
FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /btick ./cmd/btick

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /btick /btick
COPY config.yaml.example /config.yaml
COPY migrations/ /migrations/
EXPOSE 8080
CMD ["/btick", "-config", "/config.yaml"]
```

### 6. Verify Deployment

```bash
# Get deployed URL
railway status

# Test health endpoint
curl https://your-app.railway.app/v1/health
```

---

## Docker Deployment

### Build Image

```bash
docker build -t btick:latest .
```

### Run with Docker Compose

Create `docker-compose.yml`:

```yaml
version: '3.8'

services:
  btick:
    build: .
    ports:
      - "8080:8080"
    environment:
      - DATABASE_URL=postgres://postgres:postgres@db:5432/btick?sslmode=disable
    depends_on:
      db:
        condition: service_healthy
    restart: unless-stopped

  db:
    image: postgres:14-alpine
    environment:
      POSTGRES_DB: btick
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: postgres
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 5s
      timeout: 5s
      retries: 5

volumes:
  pgdata:
```

### Run

```bash
docker-compose up -d
```

---

## Kubernetes Deployment

### ConfigMap

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: btick-config
data:
  config.yaml: |
    canonical_symbol: BTC/USD
    server:
      http_addr: ":8080"
      ws_path: /ws/price
      ws:
        send_buffer_size: 256
        heartbeat_interval_sec: 5
        ping_interval_sec: 30
        read_deadline_sec: 60
    database:
      dsn: "${DATABASE_URL}"
      max_conns: 20
      run_migrations: true
    sources:
      - name: binance
        enabled: true
        ws_url: "wss://stream.binance.com:9443/stream?streams=btcusdt@trade/btcusdt@bookTicker"
        native_symbol: btcusdt
        ping_interval_sec: 15
      - name: coinbase
        enabled: true
        ws_url: "wss://advanced-trade-ws.coinbase.com"
        native_symbol: BTC-USD
        ping_interval_sec: 25
      - name: kraken
        enabled: true
        ws_url: "wss://ws.kraken.com/v2"
        native_symbol: BTC/USD
        ping_interval_sec: 30
    pricing:
      mode: multi_venue_median
      minimum_healthy_sources: 2
```

### Secret

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: btick-secrets
type: Opaque
stringData:
  DATABASE_URL: "postgresql://user:pass@host:5432/db"
```

### Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: btick
spec:
  replicas: 1  # Only 1 replica - see note below
  selector:
    matchLabels:
      app: btick
  template:
    metadata:
      labels:
        app: btick
    spec:
      containers:
      - name: btick
        image: your-registry/btick:latest
        ports:
        - containerPort: 8080
        env:
        - name: DATABASE_URL
          valueFrom:
            secretKeyRef:
              name: btick-secrets
              key: DATABASE_URL
        volumeMounts:
        - name: config
          mountPath: /app/config.yaml
          subPath: config.yaml
        livenessProbe:
          httpGet:
            path: /v1/health
            port: 8080
          initialDelaySeconds: 10
          periodSeconds: 30
        readinessProbe:
          httpGet:
            path: /v1/health
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 10
        resources:
          requests:
            memory: "128Mi"
            cpu: "100m"
          limits:
            memory: "512Mi"
            cpu: "500m"
      volumes:
      - name: config
        configMap:
          name: btick-config
```

### Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: btick
spec:
  selector:
    app: btick
  ports:
  - port: 80
    targetPort: 8080
  type: ClusterIP
```

> **⚠️ Important:** Run only **1 replica**. Multiple replicas will create duplicate snapshots and compete for exchange connections. For HA, use a standby deployment with leader election (future enhancement).

---

## Configuration Reference

### Environment Variables

| Variable | Description | Required |
|----------|-------------|----------|
| `DATABASE_URL` | PostgreSQL connection string (overrides `database.dsn` in config) | No (app runs without DB) |
| `PORT` | HTTP server port (overrides config) | No |

Config path is set via the `-config` flag (default: `./config.yaml`), not an environment variable.

### Config File

See `config.yaml.example` for full reference:

```yaml
canonical_symbol: BTC/USD

server:
  http_addr: ":8080"          # HTTP/WS listen address
  ws_path: /ws/price          # WebSocket endpoint path
  ws:
    send_buffer_size: 256       # Per-client message buffer
    heartbeat_interval_sec: 5   # App-level heartbeat period
    ping_interval_sec: 30       # WebSocket-level ping period
    read_deadline_sec: 60       # Pong timeout

database:
  dsn: "${DATABASE_URL}"      # Env var substitution supported
  max_conns: 20               # Connection pool size
  run_migrations: true        # Auto-run migrations on startup

sources:
  - name: binance
    enabled: true
    ws_url: "wss://stream.binance.com:9443/stream?streams=btcusdt@trade/btcusdt@bookTicker"
    native_symbol: btcusdt
    use_book_ticker_fallback: true
    ping_interval_sec: 15
    max_conn_lifetime_sec: 86000

  - name: coinbase
    enabled: true
    ws_url: "wss://advanced-trade-ws.coinbase.com"
    native_symbol: BTC-USD
    use_book_ticker_fallback: true
    jwt: ""                   # Optional for higher rate limits
    ping_interval_sec: 25
    max_conn_lifetime_sec: 0  # 0 = no limit

  - name: kraken
    enabled: true
    ws_url: "wss://ws.kraken.com/v2"
    native_symbol: BTC/USD
    use_ticker_fallback: true
    ping_interval_sec: 30
    max_conn_lifetime_sec: 0  # 0 = no limit

pricing:
  mode: multi_venue_median    # Pricing algorithm
  minimum_healthy_sources: 2  # Min sources for non-degraded
  trade_freshness_window_ms: 2000
  quote_freshness_window_ms: 1000
  late_arrival_grace_ms: 250  # Watermark delay
  outlier_reject_pct: 1.0     # Reject prices >1% from median
  carry_forward_max_seconds: 10

storage:
  raw_retention_days: 1
  canonical_retention_days: 1
  snapshots_retention_days: 365
  batch_insert_max_rows: 2000
  batch_insert_max_delay_ms: 100

health:
  source_stale_after_ms: 3000
  canonical_stale_after_ms: 3000
```

---

## Production Checklist

### Pre-Deployment

- [ ] PostgreSQL 17+ with TimescaleDB 2.24+ provisioned with sufficient storage
- [ ] Database backups configured
- [ ] `DATABASE_URL` set securely (not in git)
- [ ] Outbound network allows WebSocket to exchanges
- [ ] Health check endpoint accessible

### Post-Deployment

- [ ] Verify all 3 sources connect: `GET /v1/health/feeds`
- [ ] Verify prices streaming: `GET /v1/price/latest`
- [ ] Test WebSocket: `websocat wss://your-domain/ws/price` — verify welcome, initial state, heartbeats
- [ ] Set up monitoring/alerting
- [ ] Test settlement endpoint

### Monitoring Setup

1. **Health Check Endpoint:**
   ```
   GET /v1/health
   Expected: {"status":"ok"}
   ```

2. **Key Metrics to Monitor:**
   - Response of `/v1/health`
   - `source_count` from `/v1/price/latest`
   - Database connection pool
   - Memory/CPU usage

3. **Alerting Thresholds:**
   | Metric | Warning | Critical |
   |--------|---------|----------|
   | source_count | < 3 for 5m | < 2 for 5m |
   | Health status | degraded | stale or no_data |
   | API latency | > 100ms | > 500ms |

---

## Troubleshooting

### Service Won't Start

```bash
# Check logs
railway logs
# OR
docker logs btick

# Common issues:
# 1. DATABASE_URL not set
# 2. Can't connect to PostgreSQL
# 3. Port already in use
```

### No Exchange Data

```bash
# Check feed health
curl https://your-app/v1/health/feeds

# Common issues:
# 1. Outbound WebSocket blocked by firewall
# 2. Exchange IP rate limiting
# 3. DNS resolution issues
```

### Database Connection Errors

```bash
# Verify connection
psql $DATABASE_URL -c "SELECT 1"

# Check pool exhaustion
# Increase max_conns in config if needed

# SSL issues
# Add ?sslmode=require or ?sslmode=disable as needed
```

### High Memory Usage

```bash
# Check event channel backlog
# If consistently full, increase batch_insert frequency
# or reduce sources

# Normal memory: 50-150MB
# High memory: Check for goroutine leaks
```

---

## Scaling Considerations

### Current Limitations

- **Single instance only** — No HA support yet
- **All sources in one process** — Can't distribute load
- **No horizontal scaling** — Would create duplicates

### Future HA Architecture

```
                    ┌─────────────────┐
                    │  Load Balancer  │
                    └────────┬────────┘
                             │
              ┌──────────────┼──────────────┐
              │              │              │
              ▼              ▼              ▼
        ┌──────────┐  ┌──────────┐  ┌──────────┐
        │  API     │  │  API     │  │  API     │
        │ (read)   │  │ (read)   │  │ (read)   │
        └────┬─────┘  └────┬─────┘  └────┬─────┘
             │             │             │
             └─────────────┼─────────────┘
                           │
                    ┌──────┴──────┐
                    │  PostgreSQL  │
                    │   (shared)   │
                    └──────────────┘
                           ▲
                           │
                    ┌──────┴──────┐
                    │   Ingester   │  ◄── Single instance
                    │   (leader)   │      with standby
                    └─────────────┘
```

For now, rely on Railway's automatic restart on failure.
