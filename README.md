# ASI Genie — Orchestrator

Go service that powers **ASI Genie** in the hotel PMS: it routes natural-language requests, calls existing **ASI read APIs** with the signed-in user’s token, and returns structured **cards** for the frontend. It does not own reservation/config data and does not perform writes to ASI services.

**Module name:** `asi-genie-poc` (Go import path). **Default port:** `8090`.

## Documentation

| Doc | Contents |
|-----|----------|
| [docs/architecture.md](docs/architecture.md) | System diagram, folder layout, `/chat` pipeline, tools & intents |
| [docs/local-development.md](docs/local-development.md) | Prerequisites, `.env`, Docker Postgres, frontend wiring, troubleshooting |
| [docs/database.md](docs/database.md) | Postgres tables, what is stored, AutoMigrate, analytics export |
| [docs/response-contract.md](docs/response-contract.md) | `POST /chat` request/response and card types |
| [docs/phase1-coverage.md](docs/phase1-coverage.md) | Scenario catalogue vs implementation |

## Quick start

```bash
cp .env.example .env          # set POSTGRES_PASSWORD, ASI_*_BASE_URL, LLM if needed
docker compose up -d          # Postgres on localhost:5434
make run                      # loads .env and starts :8090
curl -s http://localhost:8090/health
```

For live reload: `make dev-air` (requires [Air](https://github.com/air-verse/air)). See [local-development.md](docs/local-development.md) for full setup including **asi-frontend** (`NEXT_PUBLIC_GENIE_API_URL`).

## How it works (summary)

1. **Frontend** sends `POST /chat` with `message`, **`propertyId`** (required), optional `sessionId`, `businessDate`, `screen`, and the user JWT (`X-Token`).
2. **Orchestrator** classifies intent (LLM tool call or keyword fallback), runs **GET-only** tools against config/auth/reporting/reservation, builds **cards** + short **text**.
3. **Postgres** stores conversation messages, per-turn analytics (`genie_turns`), tool audit rows, and events (denied / unsupported / PCI).

Details: [architecture.md](docs/architecture.md). Data model: [database.md](docs/database.md).

## HTTP API

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/chat` | Main Genie turn |
| `GET` | `/health`, `/v1/genie/healthcheck` | Liveness + tool/intent counts |
| `GET` | `/conversations/{sessionId}` | Stored message history (+ cards via message meta) |
| `POST` | `/feedback` | Thumbs up/down on a turn (`turnId` from `/chat`) |
| `GET` | `/intents` | Capability registry |
| `GET` | `/analytics/turns` | Turn export (CSV JSON) |
| `GET` | `/events?kind=` | Audit events (`intent`, `denied`, `unsupported`, `pci_block`) |

Contract: [response-contract.md](docs/response-contract.md).

## Project structure (top level)

```
cmd/orchestrator/     HTTP entrypoint
internal/genie/       Chat engine, handlers, classification
internal/cards/       Card JSON shapes
internal/store/       Postgres models + Store interface
internal/intents/     Generated capability catalogue
toolregistry/         Generated ASI HTTP clients + tool metadata
llm/                  Provider clients (OpenAI, Anthropic, Gemini)
scripts/              Code generators (tools, intents)
specs/                Swagger + catalogue CSVs
docs/                 Architecture, DB, local dev, API contract
```

## Configuration (env)

Copy [.env.example](.env.example). Highlights:

| Area | Variables |
|------|-----------|
| Server | `PORT`, `GENIE_CORS_ORIGIN`, `APP_ENV` |
| Postgres | `POSTGRES_*` or `DATABASE_URL` (**required**) |
| ASI APIs | `ASI_CONFIG_BASE_URL`, `ASI_AUTH_BASE_URL`, `ASI_REPORTING_BASE_URL`, `ASI_RESERVATION_BASE_URL` (**required**) |
| LLM | `LLM_PROVIDER` (`none` \| `openai` \| `anthropic` \| `gemini`), `LLM_MODEL`, provider API keys, `LLM_TIMEOUT`, `LLM_ATTEMPT_TIMEOUT` |
| Logging | `LOG_LEVEL`, `LOG_BODIES`, `LOG_BODY_MAX`, `LOG_COLOR` |

API keys and DB passwords are **env-only**, never committed.

### LLM providers

| `LLM_PROVIDER` | Key env | Default model if `LLM_MODEL` empty |
|----------------|---------|----------------------------------|
| `openai` | `OPENAI_API_KEY` | `gpt-4o` |
| `anthropic` | `ANTHROPIC_API_KEY` | `claude-sonnet-4-5` |
| `gemini` | `GEMINI_API_KEY` | `gemini-2.5-flash` |
| `none` | — | Keyword routing only |

## Code generation

```bash
make gen                 # tools + intents + build + vet
make gen-registry        # from sibling asi-*-service specs
make gen-registry-local  # from ./specs/*.swagger.json
make gen-intents         # Scenarios.csv → internal/intents/catalog_gen.go
```

Generated tools live in `toolregistry/generated/` (~380 read-only GET operations). Names follow `service_method_path` (e.g. `reservation_get_reservations_id`). Regenerate when Swagger changes.

## Makefile targets

```bash
make help          # full list
make run / dev     # run with Postgres
make test          # go test ./...
make db-up / db-down
make docker-build  # production image
```

## Logging

Structured stderr blocks: **`CHAT`** (request/response), **`LLM`** (classifier), **`ASI`** (tool HTTP). Tokens and card numbers are masked. Default verbosity: `debug` when `APP_ENV=local`, else `info`.

## Docker

- **docker-compose.yml** — Postgres 17 only (`asi_genie` DB, host port **5434**).
- **Dockerfile** — multi-stage build of `./cmd/orchestrator` for deployment.

The orchestrator process is intended to run **natively** in dev (`go run` / `make run`), not inside compose.

## Related repos

- **asi-frontend** — Genie panel, `NEXT_PUBLIC_GENIE_API_URL`, report deep links.
- **asi-config-service**, **asi-auth-service**, **asi-reporting-service**, **asi-reservation-service** — upstream read APIs and Swagger sources for `make gen-registry`.
