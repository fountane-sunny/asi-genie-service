# Architecture

ASI Genie orchestrator is a **Go HTTP service** that sits between the **asi-frontend** chat UI and existing **ASI microservices** (config, auth, reporting, reservation). It classifies user messages, runs **read-only** backend calls with the user’s token, and returns **typed UI cards**. It does not modify ASI services or perform writes on their behalf.

## System context

```mermaid
flowchart LR
  subgraph client
    FE[asi-frontend Genie panel]
  end
  subgraph genie_svc [asi-genie-service]
    API[cmd/orchestrator HTTP]
    ENG[internal/genie Engine]
    STORE[(PostgreSQL)]
    LLM[llm provider optional]
  end
  subgraph asi [ASI APIs]
    CFG[config]
    AUTH[auth]
    RPT[reporting]
    RES[reservation]
  end
  FE -->|POST /chat X-Token propertyId| API
  API --> ENG
  ENG --> LLM
  ENG --> STORE
  ENG -->|GET + user JWT| CFG
  ENG -->|GET + user JWT| AUTH
  ENG -->|GET + user JWT| RPT
  ENG -->|GET + user JWT| RES
```

## Repository layout

```
asi-genie-service/
├── cmd/orchestrator/       # HTTP server: routes, CORS, chat handler
│   ├── main.go             # Startup: LLM, ASI URLs, Postgres, mux
│   └── chat.go             # POST /chat, history, analytics, events
├── internal/
│   ├── genie/              # Core engine (see below)
│   ├── cards/              # Response card types + Finalize() for legacy fields
│   ├── intents/            # Capability catalogue (catalog_gen.go generated)
│   ├── store/              # Postgres persistence interface + GORM models
│   ├── asienv/             # Required ASI_*_BASE_URL validation
│   └── logx/               # Structured CHAT / LLM / ASI logging
├── toolregistry/
│   ├── generated/          # Swagger-generated HTTP tools (per ASI service)
│   └── hooks.go            # Permission / card mapping hooks (stubs)
├── llm/                    # OpenAI, Anthropic, Gemini behind LLMClient
├── scripts/
│   ├── gen-registry/       # specs → toolregistry/generated
│   └── gen-intents/        # Scenarios.csv → catalog_gen.go
├── specs/                  # Local Swagger 2.0 copies + genie catalogue CSVs
├── docs/                   # This file, response contract, coverage, DB, local dev
├── docker-compose.yml        # Postgres only
└── Dockerfile              # Deploy image (orchestrator binary)
```

### `internal/genie` (engine)

| Area | Files | Role |
|------|--------|------|
| Entry | `engine.go` | `Engine.Chat`: PCI guard → classify → dispatch → persist |
| Routing | `classify.go` | LLM `route_intent` or keyword fallback; slot extraction |
| Handlers | `handlers.go`, `handlers_phase1_extra.go` | Dedicated scenarios (arrivals, folio, night audit, …) |
| Generic | `generic.go` | Catalogue P1/P2: navigate, not supported, LLM-picked GET |
| Tools | `tools.go` | Execute generated tools; timeout; log executions |
| Navigation | `links.go`, `navigate.go` | Deep links aligned with asi-frontend routes |
| Reports | `report_catalog.go` | Live report list + deep link params |
| Errors | `errors.go` | ASI errors → user-facing error cards |
| JSON helpers | `jsonx.go` | Parse list/data from ASI responses |

In-memory **session** state (last entity, last intent, business date cache) lives in `Engine` keyed by `sessionId`; durable history is in Postgres.

## Request pipeline (`POST /chat`)

1. **Validate** — `propertyId` required (body or `X-Property-Id`); group scope rejected. User JWT from `X-Token` / `Authorization` / cookie.
2. **PCI guard** — Luhn card patterns masked; reply is a `denied` card; event `pci_block`.
3. **Session** — Bind conversation to property; reset context if property changes.
4. **Classify** — One LLM tool call `route_intent` over ~168 capability ids + slots, or keyword match if LLM is `none` or failed.
5. **Dispatch**
   - **Dedicated handler** for P0 scenarios (fixed GETs, server-side composition).
   - **Generic** for navigate / not supported / optional LLM-chosen GET.
   - **RBAC** — No Genie-side permission matrix; 401/403 from ASI → `denied` card.
6. **Respond** — `text`, `cards`, `chips`, `intent`, `toolCalls`, `durationMs`, optional `notice`.
7. **Persist** — Append messages, log tools, log turn analytics, log events.

Non-GET tools are **removed at startup** and cannot be invoked.

## Tool registry

- Generated from each service’s Swagger (`scripts/gen-registry`).
- Naming: `reservation_get_reservations` ↔ `ReservationGetReservations`.
- `toolregistry/generated/client.go` forwards **`X-Token`** (user JWT) on outbound calls.
- Base URLs from env: `ASI_CONFIG_BASE_URL`, `ASI_AUTH_BASE_URL`, `ASI_REPORTING_BASE_URL`, `ASI_RESERVATION_BASE_URL`.

Regenerate after API changes: `make gen-registry` or `make gen-registry-local` (uses `./specs`).

## Intent catalogue

- Source: `specs/catalogue/genie-catalogue/Scenarios.csv`
- Generated: `internal/intents/catalog_gen.go` via `make gen-intents`
- Coverage matrix: `docs/phase1-coverage.md` (regenerate with `scripts/gen-intents/gen_coverage.py`)

## Frontend contract

- API shape: [response-contract.md](./response-contract.md)
- asi-frontend: `NEXT_PUBLIC_GENIE_API_URL`, Genie shell on `/property/[id]/…`, session id in sessionStorage per property.

## Code generation

```bash
make gen                 # registry + intents + build + vet
make gen-registry        # read ../asi-*-service specs (default -root ..)
make gen-registry-local  # read ./specs/*.swagger.json
make gen-intents         # Scenarios.csv → catalog_gen.go
```

## Logging

Boxed blocks on stderr (`internal/logx`): `CHAT`, `LLM`, `ASI`. Secrets, tokens, and card numbers are masked. See README → Logging (env: `LOG_LEVEL`, `LOG_BODIES`, `LOG_BODY_MAX`, `LOG_COLOR`).

## Deploy notes

- **Dockerfile** builds `./cmd/orchestrator` (multi-stage Alpine).
- **docker-compose** in repo is for **local Postgres only**; orchestrator is typically run with `go run` or the container image in deploy environments.
- Requires Postgres + all four ASI base URLs + LLM config (or `LLM_PROVIDER=none`).
