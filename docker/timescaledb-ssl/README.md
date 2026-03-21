# TimescaleDB 2.24.0 + PostGIS + SSL (Railway-compatible)

Custom Docker image: **PostgreSQL 17 + TimescaleDB 2.24.0 + PostGIS + SSL**, built for Railway deployment.

Based on `timescale/timescaledb-ha:pg17-ts2.24-all` with self-signed SSL certificates (same approach as [railwayapp-templates/timescale-postgis-ssl](https://github.com/railwayapp-templates/timescale-postgis-ssl)).

## Build locally

```bash
docker build -t timescale-postgis-ssl:pg17-ts2.24 docker/timescaledb-ssl/
```

## Push to GHCR manually

```bash
# Authenticate
echo $GITHUB_TOKEN | docker login ghcr.io -u USERNAME --password-stdin

# Tag and push
docker tag timescale-postgis-ssl:pg17-ts2.24 ghcr.io/USERNAME/timescale-postgis-ssl:pg17-ts2.24
docker push ghcr.io/USERNAME/timescale-postgis-ssl:pg17-ts2.24
```

Replace `USERNAME` with your GitHub username (lowercase).

## CI/CD

The GitHub Actions workflow at `.github/workflows/build-timescaledb.yml` automatically builds and pushes to GHCR on:
- Push to `main` that modifies `docker/timescaledb-ssl/**`
- Manual trigger via `workflow_dispatch`

The image is published as `ghcr.io/<owner>/timescale-postgis-ssl:pg17-ts2.24`.

## Use on Railway

In your Railway service, set the source image to:

```
ghcr.io/<owner>/timescale-postgis-ssl:pg17-ts2.24
```

> **Note:** Make the GHCR package public, or configure Railway with a registry credential if private.

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SSL_CERT_DAYS` | `820` | Self-signed certificate validity in days |
| `LOG_TO_STDOUT` | — | Set to `true` to redirect all logs to stdout |
