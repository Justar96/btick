# btick

Real-time Bitcoin price oracle for prediction market settlement.

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-14+-336791?style=flat&logo=postgresql)](https://www.postgresql.org/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

## Overview

btick aggregates real-time trade data from multiple exchanges and produces a canonical, manipulation-resistant BTC/USD price. Designed for prediction markets that need reliable settlement prices.

### Features

- **Multi-venue median pricing** — Aggregates Binance, Coinbase, and Kraken
- **Sub-second updates** — Real-time price changes via WebSocket
- **1-second snapshots** — Immutable price records for settlement
- **5-minute settlement API** — Purpose-built for prediction markets
- **Quality scoring** — Track data freshness and source availability
- **Outlier rejection** — Automatic filtering of anomalous prices

## Quick Start

### Prerequisites

- Go 1.22+
- PostgreSQL 14+ (or use Railway)

### Run Locally

```bash
# Clone
git clone https://github.com/justar9/btick.git
cd btick

# Setup config
cp config.yaml.example config.yaml
# Edit config.yaml with your DATABASE_URL

# Run
go run ./cmd/btick
```

### Run with Railway

```bash
# Install Railway CLI
npm install -g @railway/cli

# Link to your Railway project
railway link

# Run locally with Railway's database
railway run go run ./cmd/btick
```

### Verify It's Working

```bash
# Health check
curl http://localhost:8080/v1/health

# Current price
curl http://localhost:8080/v1/price/latest

# WebSocket stream
wscat -c ws://localhost:8080/ws/price
```

## API Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /v1/price/latest` | Current canonical price |
| `GET /v1/price/settlement?ts=` | Settlement price at 5-min boundary |
| `GET /v1/price/snapshots?start=&end=` | Historical 1-second snapshots |
| `GET /v1/price/ticks?limit=` | Recent canonical price changes |
| `GET /v1/health` | System health status |
| `GET /v1/health/feeds` | Per-exchange feed status |
| `WS /ws/price` | Real-time price stream |

### Settlement Example

```bash
# Get settlement price for market closing at 09:10:00 UTC
curl "http://localhost:8080/v1/price/settlement?ts=2026-03-19T09:10:00Z"
```

Response:
```json
{
  "settlement_ts": "2026-03-19T09:10:00Z",
  "symbol": "BTC/USD",
  "price": "70105.45",
  "status": "confirmed",
  "quality_score": 0.9556,
  "source_count": 3,
  "sources_used": ["binance", "coinbase", "kraken"]
}
```

## WebSocket

Connect to `ws://localhost:8080/ws/price` to receive:

```json
{"type":"latest_price","ts":"2026-03-19T09:10:00.123Z","price":"70105.45","source_count":3}
{"type":"snapshot_1s","ts":"2026-03-19T09:10:00Z","price":"70105.45","source_count":3}
```

## Architecture

```
Binance ──┐
          │      ┌─────────────┐      ┌─────────────┐
Coinbase ─┼─────►│  Snapshot   │─────►│  PostgreSQL │
          │      │   Engine    │      │   Database  │
Kraken ───┘      └──────┬──────┘      └─────────────┘
                        │
                        ▼
                 ┌─────────────┐
                 │  REST API   │◄───── Market Service
                 │  WebSocket  │◄───── Trading UI
                 └─────────────┘
```

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for detailed design.

## Configuration

Key settings in `config.yaml`:

```yaml
pricing:
  mode: multi_venue_median     # Aggregation method
  minimum_healthy_sources: 2   # Min sources for "confirmed" status
  outlier_reject_pct: 1.0      # Reject prices >1% from median
```

See [config.yaml.example](config.yaml.example) for full options.

## Documentation

| Document | Description |
|----------|-------------|
| [API.md](docs/API.md) | Full API reference with examples |
| [ARCHITECTURE.md](docs/ARCHITECTURE.md) | System design and data flow |
| [DATABASE.md](docs/DATABASE.md) | Schema and query reference |
| [DEPLOYMENT.md](docs/DEPLOYMENT.md) | Production deployment guide |
| [openapi.yaml](docs/openapi.yaml) | OpenAPI 3.1 specification |

## Project Structure

```
btick/
├── cmd/
│   └── btick/          # Main entry point
├── internal/
│   ├── adapter/          # Exchange WebSocket adapters
│   ├── api/              # HTTP/WebSocket server
│   ├── config/           # Configuration loading
│   ├── domain/           # Domain types
│   ├── engine/           # Snapshot pricing engine
│   └── storage/          # Database layer
├── migrations/           # SQL migrations
├── docs/                 # Documentation
└── config.yaml.example   # Config template
```

## Development

### Run Tests

```bash
go test ./...
```

### Run with Hot Reload

```bash
# Install air
go install github.com/air-verse/air@latest

# Run
air
```

### Build Binary

```bash
go build -o btick ./cmd/btick
```

## Deployment

### Railway (Recommended)

```bash
railway up
```

### Docker

```bash
docker build -t btick .
docker run -e DATABASE_URL=... -p 8080:8080 btick
```

See [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) for production setup.

## Data Sources

| Exchange | Streams | Auth |
|----------|---------|------|
| Binance | `btcusdt@trade`, `btcusdt@bookTicker` | None |
| Coinbase | `market_trades`, `ticker` | Optional JWT |
| Kraken | `trade`, `ticker` | None |

All connections use public WebSocket APIs with no API keys required.

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Run tests: `go test ./...`
5. Submit a pull request

## License

MIT License - see [LICENSE](LICENSE) for details.
