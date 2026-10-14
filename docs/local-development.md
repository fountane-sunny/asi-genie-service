# Local development

Run the orchestrator on your machine with Docker providing Postgres only (same pattern as asi-reservation-service).

## Prerequisites

| Requirement | Version / notes |
|-------------|-----------------|
| Go | 1.24+ (`go.mod`) |
| Docker | For Postgres (`docker compose`) |
| ASI services | Config, auth, reporting, reservation running locally or reachable URLs |
| asi-frontend (optional) | For end-to-end Genie UI |

## 1. Environment file

```bash
cp .env.example .env
# Edit .env: POSTGRES_PASSWORD, ASI_*_BASE_URL, LLM keys if not using LLM_PROVIDER=none
```

The Go binary **does not load `.env`**. Use one of:

```bash
make run          # sources .env then go run
make dev          # docker compose up + make run
make dev-air      # postgres + air live reload
```

Manual:

```bash
set -a; source .env; set +a
go run ./cmd/orchestrator
```

## 2. Start Postgres

```bash
docker compose up -d
make db-ps        # optional: check container
```

Default mapping: host **5434** → container 5432 (`POSTGRES_HOST_PORT`). Adjust if 5434 is taken.

On first orchestrator start, GORM **AutoMigrate** creates tables (see [database.md](./database.md)).

## 3. Start the orchestrator

```bash
curl -s http://localhost:8090/health | jq
```

Expect `store: postgres`, `readTools` count, `intents` count.

## 4. ASI service URLs

All four are **required** at startup (`internal/asienv`):

| Variable | Typical local value |
|----------|---------------------|
| `ASI_CONFIG_BASE_URL` | `http://localhost:8080/v1` |
| `ASI_AUTH_BASE_URL` | `http://localhost:8081/v1/auth` |
| `ASI_REPORTING_BASE_URL` | `http://localhost:8082/v1/reporting` |
| `ASI_RESERVATION_BASE_URL` | `http://localhost:8083/api/v1` |

Match your actual ports from each service’s run config.

## 5. LLM

| `LLM_PROVIDER` | Behavior |
|----------------|----------|
| `none` | Keyword intent routing only; no API key |
| `openai` / `anthropic` / `gemini` | Requires provider API key + `LLM_MODEL` (or provider-specific `*_MODEL`) |

Startup **fails** if a real provider is selected without a key. Use `none` for offline handler testing.

Timeouts: `LLM_TIMEOUT` (per chat), `LLM_ATTEMPT_TIMEOUT` (per LLM retry).

## 6. Connect asi-frontend

In `asi-frontend/apps/web/.env`:

```bash
NEXT_PUBLIC_GENIE_API_URL=http://localhost:8090
```

Log in to the web app, open a property, use the Genie FAB. The browser sends the user JWT as **`X-token`** and **`propertyId`** in the chat body (see [response-contract.md](./response-contract.md)).

Ensure `GENIE_CORS_ORIGIN` matches the web origin (default `http://localhost:3000`).

## 7. Useful commands

```bash
make test              # unit tests
make gen               # regenerate tools + intents
make db-logs           # postgres logs
docker exec -it asi-genie-postgres psql -U asi_genie -d asi_genie

# Sample chat (replace token and property)
curl -s -X POST http://localhost:8090/chat \
  -H 'Content-Type: application/json' \
  -H 'X-Token: YOUR_JWT' \
  -d '{"message":"Who arrives today?","propertyId":1,"businessDate":"2026-10-13"}' | jq
```

## 8. Troubleshooting

| Symptom | Check |
|---------|--------|
| `store: postgres unreachable` | `docker compose ps`, `DATABASE_URL`, port 5434 |
| `ASI base URLs are required` | All four `ASI_*_BASE_URL` set in env |
| 401 on chat cards | Valid `X-Token`; user logged in on frontend |
| Empty report links | Reservation catalog API up; property id valid |
| CORS errors | `GENIE_CORS_ORIGIN` vs frontend URL |

Logs: run with `APP_ENV=local` for debug-level boxed logs on stderr.
