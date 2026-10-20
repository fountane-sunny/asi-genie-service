# Database (PostgreSQL)

Genie persists chat history, tool audit rows, analytics, and events in **PostgreSQL**. The orchestrator does **not** use an in-memory fallback: if Postgres is missing or unreachable, the process exits at startup.

Schema is defined in Go (`internal/store/store_postgres.go`) and applied with **GORM AutoMigrate** on startup. There are **no SQL migration files** — change the struct tags and restart to evolve tables (use care in shared environments).

## Connection

| Source | Notes |
|--------|--------|
| `POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB`, `POSTGRES_SSLMODE` | Built into a key=value DSN (same pattern as asi-reservation-service) |
| `DATABASE_URL` | Used when `POSTGRES_HOST` is empty |

Local Docker: see [local-development.md](./local-development.md). Default host port **5434** (`POSTGRES_HOST_PORT` in compose).

## Tables

GORM model names map to these table names:

| Table | Model | Purpose |
|-------|--------|---------|
| `conversations` | `Conversation` | One row per client `sessionId`, bound to a property |
| `messages` | `MessageRow` | User and assistant turns for that conversation |
| `tool_executions` | `ToolExecutionRow` | Audit log of each ASI tool HTTP call |
| `genie_events` | `GenieEvent` | Redacted events: intent, denied, unsupported, pci_block |
| `genie_turns` | `GenieTurn` | One analytics row per `POST /chat` (export via `/analytics/turns`) |

### `conversations`

| Column | Type | Description |
|--------|------|-------------|
| `id` | UUID PK | Internal conversation id |
| `session_id` | text, unique | Client session id (e.g. from asi-frontend sessionStorage) |
| `user_id` | text, nullable | From auth when available |
| `property_id` | int, nullable | Property this session is scoped to |
| `title` | text | Optional title |
| `created_at`, `updated_at` | timestamptz | Row timestamps |

**Property switch:** If the same `session_id` is reused for a **different** `property_id`, the old row is archived (`session_id` suffixed with `~p<oldPropertyId>-<unix>`) and a new conversation is created. See `conversation()` in `store_postgres.go`.

### `messages`

| Column | Type | Description |
|--------|------|-------------|
| `id` | UUID PK | |
| `conversation_id` | UUID FK → conversations | |
| `role` | text | `user` or `assistant` (and tool-related roles if stored) |
| `content` | text | Message text (**PAN-masked** for user content when PCI guard runs) |
| `tool_calls` | jsonb | Optional metadata for assistant turns |
| `duration_ms` | bigint, nullable | Server time to produce assistant reply |
| `created_at` | timestamptz | Ordered history for `GET /conversations/{sessionId}` |

Cards are **not** stored on `messages` rows; full turn detail (including cards) is on `genie_turns`.

### `tool_executions`

| Column | Type | Description |
|--------|------|-------------|
| `id` | UUID PK | |
| `conversation_id` | UUID, nullable | Linked when `session_id` is known |
| `tool_name` | text | e.g. `reservation_get_reservations` |
| `method`, `path` | text | HTTP method and path template |
| `args` | jsonb | Request arguments (redacted in logs; stored for audit) |
| `status` | text | e.g. `executed`, `error` |
| `result` | jsonb | Response snapshot when logged |
| `created_at` | timestamptz | |

### `genie_events`

| Column | Type | Description |
|--------|------|-------------|
| `id` | UUID PK | |
| `session_id` | text, nullable | |
| `kind` | text, indexed | `intent`, `denied`, `unsupported`, `pci_block` |
| `intent`, `scenario_id` | text, nullable | Catalogue ids when relevant |
| `property_id` | int, nullable | |
| `message` | text, nullable | Redacted user text (e.g. unsupported asks) |
| `detail` | jsonb | Extra structured payload |
| `created_at` | timestamptz | |

Query: `GET /events?kind=unsupported` (also `denied`, `intent`, `pci_block`).

### `genie_turns`

One row per successful or failed chat turn (written in `engine.persist`).

| Column | Type | Description |
|--------|------|-------------|
| `id` | UUID PK | |
| `created_at` | timestamptz | Turn timestamp |
| `session_id` | text | |
| `property_id` | int | |
| `user_id`, `user_name`, `user_email` | text, nullable | From auth current-user (cached ~10 min per token) |
| `message` | text | User message (**PAN-masked**) |
| `reply` | text | Assistant `text` field |
| `cards` | jsonb | Full card array JSON |
| `intent`, `scenario_id` | text, nullable | |
| `source` | text, nullable | `llm`, `keyword`, `rule` |
| `confidence` | float | Classifier confidence |
| `slots` | jsonb | Extracted slots |
| `tools` | jsonb | Tool call summary (name, status, duration) |
| `llm_model` | text, nullable | |
| `llm_failed` | bool | A planner or classifier LLM call failed; the turn fell back to keyword routing |
| `duration_ms` | bigint | Total `/chat` duration |
| `error_code`, `error_message` | text, nullable | When handler failed |
| `feedback` | text, nullable | `thumbs_up`, `thumbs_down`, or `thumbs_down: comment` via `POST /feedback` |

Export: `GET /analytics/turns?from=&to=&propertyId=&format=csv`.

## What we store vs. what we do not

| Stored | Not stored in Genie DB |
|--------|-------------------------|
| Masked chat text, cards JSON, intent/slots | Raw credit card numbers (blocked before store) |
| Tool names, paths, args, status | ASI business data long-term (folios, guests live in ASI services) |
| Session ↔ property binding | User passwords or refresh tokens |
| Analytics / audit events | LLM API keys (env only) |

Operational data (reservations, reports, config) is read **on demand** from ASI APIs using the user’s JWT; Genie does not replicate those domains into its database.

## Operations

```bash
# Shell into local Postgres
docker exec -it asi-genie-postgres psql -U asi_genie -d asi_genie

# List tables
\dt

# Stop DB, keep volume
docker compose down

# Stop DB and wipe data
docker compose down -v
```

Source of truth for columns: `internal/store/store_postgres.go` (`Models` slice passed to `AutoMigrate`).
