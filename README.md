# chataibot2api

OpenAI-compatible image and text proxy with an account-pool admin console, Docker/GHCR deployment support, and Zeabur-ready runtime.

## Features

- OpenAI-compatible endpoints:
  - `GET /v1/models`
  - `POST /v1/chat/completions`
  - `POST /v1/images/generations`
- Account-pool admin console:
  - pool overview / export / restore
  - manual fill
  - quota snapshot and live probe
  - legacy pool migration helpers
- Persistent pool storage for container deployments
- GHCR image publishing via GitHub Actions

## Runtime model notes

- `gpt-image-1.5-high` supports normal image generation through `chat/completions`.
- As of the current runtime verification, **edit-style requests** for both `gpt-image-1.5` and `gpt-image-1.5-high` may return upstream `403` membership errors on:
  - `POST /v1/chat/completions` with `image_url`
  - `POST /v1/images/generations` with `image`
- The admin catalog therefore marks these GPT image edit capabilities as **subscription-gated**, instead of treating them as unconditional edit support.

## Required environment variables

| Variable | Purpose |
| --- | --- |
| `MAIL_API_BASE_URL` | mail worker base URL |
| `MAIL_DOMAIN` | mail domain used for registration |
| `MAIL_ADMIN_TOKEN` | mail worker admin token |
| `API_BEARER_TOKEN` | bearer token for public API routes |
| `ADMIN_TOKEN` | bearer token for admin API routes |

## Common optional environment variables

| Variable | Default / Example | Purpose |
| --- | --- | --- |
| `POOL_SIZE` | `1000` | pool target size for autofill |
| `POOL_LOW_WATERMARK` | `500` | when healthy pool count drops below this, refill back toward target |
| `POOL_STORE_PATH` | `/app/data/holo-image-api/pool-accounts.json` | persistent pool snapshot path |
| `POOL_PRUNE_INTERVAL_SECONDS` | `300` | prune interval |
| `POOL_REGISTRATION_INTERVAL_SECONDS` | `15` | minimum delay after successful registrations |
| `POOL_FAILURE_BACKOFF_SECONDS` | `60` | registration failure backoff |
| `POOL_FAILURE_BACKOFF_MAX_SECONDS` | `900` | max failure backoff |
| `INSTANCE_NAME` | `holo-image-api` | instance label |
| `SERVICE_LABEL` | `holo-image-api-ghcr` | deployment service label |
| `DEPLOY_SOURCE` | `ghcr` / `zeabur` | deployment source tag |
| `IMAGE_REF` | `ghcr.io/thewisewolfholo/chataibot2api:main` | runtime image label |
| `PUBLIC_BASE_URL` | `https://holo-image-api.zeabur.app` | current public URL |
| `PRIMARY_PUBLIC_BASE_URL` | `https://holo-image-api.zeabur.app` | primary public URL |
| `LEGACY_POOL_EXPORT_BASE_URL` | optional | legacy pool migration source |

## Docker usage

### Build locally

```bash
docker build -t chataibot2api:local .
```

### Run locally

```bash
docker run --rm -p 8080:8080 \
  -e MAIL_API_BASE_URL=https://your-mail-worker.example.com \
  -e MAIL_DOMAIN=example.com \
  -e MAIL_ADMIN_TOKEN=replace-me \
  -e API_BEARER_TOKEN=replace-me \
  -e ADMIN_TOKEN=replace-me \
  -e POOL_SIZE=1000 \
  -e POOL_LOW_WATERMARK=500 \
  -e POOL_STORE_PATH=/app/data/holo-image-api/pool-accounts.json \
  -v chataibot2api-data:/app/data \
  ghcr.io/thewisewolfholo/chataibot2api:main
```

### Health check

```bash
curl http://127.0.0.1:8080/healthz
```

## GHCR image

GitHub Actions publishes images on every push to `main`:

- `ghcr.io/thewisewolfholo/chataibot2api:main`
- `ghcr.io/thewisewolfholo/chataibot2api:latest`
- `ghcr.io/thewisewolfholo/chataibot2api:sha-<short>`

Workflow file:

- `.github/workflows/docker-publish.yml`

## Zeabur deployment notes

- Deploy from the GHCR image instead of rebuilding inside Zeabur when you want a single reproducible artifact.
- Mount persistent storage and point `POOL_STORE_PATH` at `/app/data/holo-image-api/pool-accounts.json`.
- Keep `POOL_SIZE=1000` and `POOL_LOW_WATERMARK=500` if you want the current refill policy.

## Admin console

- Login page: `/admin/login`
- Main console: `/admin`
- Useful admin routes:
  - `GET /v1/admin/pool`
  - `GET /v1/admin/pool/export`
  - `POST /v1/admin/pool/restore`
  - `GET /v1/admin/quota/snapshot`
  - `POST /v1/admin/quota/probe`
