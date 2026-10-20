# Genie API response contract

Types live in `internal/cards` (cards) and `internal/genie/engine.go` (request/response). Persistence: [database.md](./database.md).
One contract serves the web panel, ⌘K bar and mobile sheet. Phase 1 is read-only: no card ever triggers a write.

## `POST /chat`

Headers: `X-Token` (or `Authorization: Bearer`, or `token` cookie) — the user's own ASI token, forwarded to every ASI read. **Required**: missing → `401 {"error":"Sign in to use Genie."}`. `X-Property-Id` (same as body `propertyId`; one of them is required).

```json
{ "sessionId": "web-…", "message": "Who arrives today?", "propertyId": 12, "businessDate": "2026-10-12", "localHour": 9, "screen": "/property/12/dashboard" }
```
`propertyId` is **required** (Genie is property-specific; no group chat). Missing → `400 {"error":"propertyId is required. Open a property to chat with Genie."}`; `groupId`/`X-Group-Id` → `400` too. Before the turn runs, Genie checks the property is in the user's auth `/permissions`: auth rejects the token → `401`, property not in the list → `403 {"error":"You don't have access to this property."}`. A session is bound to its property: a different property starts a new conversation. Optional: `businessDate` (without it Genie reads the night-audit working date), `localHour` (0–23 on the user's clock, used for the greeting's time of day), `screen`. The frontend scopes `sessionId` per property (`web-p<propertyId>-…`) and shows the chat only on `/property/<id>/…` pages.

## `POST /chat/stream`

Same body, headers, auth checks and engine as `/chat`. The finished turn is sent as Server-Sent Events, in this order:

| Event | Data |
|-------|------|
| `tool_start` | `{ id, name, label }` per ASI read (label e.g. "Checking revenue") |
| `tool_end` | `{ id, status, ms }` |
| `text_delta` | `{ text }` |
| `card` | one `Card` per event |
| `chips` | `Chip[]` (only when there are chips) |
| `done` | `{ turnId, sessionId }` |
| `error` | `{ error }` — auth/validation failure (with the same HTTP status as `/chat`) or engine failure |

The web client calls `/chat/stream` first and falls back to `/chat` only when no SSE event arrives.

### Response
```json
{
  "sessionId": "web-…",
  "text": "14 arrivals on 2026-10-12.",
  "cards": [ Card ],
  "chips": [ { "label": "Who's departing today?", "message": "Who's departing today?" } ],
  "citations": [ { "id": "policy", "source": "config", "title": "Cancellation policy" } ],
  "intent": { "id": "todays_arrivals", "scenarioId": "G-008", "responseType": "List card", "handler": "dedicated", "confidence": 0.93, "source": "llm", "slots": { "date": "2026-10-12" } },
  "propertyId": 12,
  "businessDate": "2026-10-12",
  "toolCalls": [ { "name": "reservation_get_reservations", "args": {…}, "status": "executed" } ],
  "turnId": "uuid-of-genie_turns-row"
}
```
Also on every response: `createdAt` (ISO 8601, UTC), `durationMs` (server time to answer), optional `notice` ("The assistant is busy right now. Try again in a moment if this answer looks off." when an LLM call failed and the fallback router answered). `toolCalls[].error` is an error **code** only, and each tool call has `durationMs`. Saved messages (`GET /conversations/{id}`) carry `createdAt` and, for replies, `durationMs`.

`handler`: `planner` (model-planned reads) | `dedicated` | `generic` | `navigate` | `fallback`. `source`: `llm` | `keyword` (LLM unavailable) | `rule` (PCI / property scope / verified create screen). Planner turns use `intent.id` `agent` for a data answer, `unsupported` for a refusal, `smalltalk` for a reply with no read.

## Card

Common: `type`, `intent`, `title`, optional `subtitle`, `message`, `link`.
`fields` (string map) is a legacy flattening of every card for the first chat panel; new renderers should use the typed fields.

| Catalogue response type | `type` | Typed fields |
|---|---|---|
| Text answer | `text` | `message` |
| Data card | `data` | `rows: Field[]`, optional `items` (e.g. last postings) |
| List card | `list` | `items: ListItem[]`, `total` (show "N more" when `total > items.length`), `link` to the full screen |
| Comparison card | `comparison` | `comparison: { metric, current: Period, previous: Period, delta?, deltaPct? }` (deltas computed server-side) |
| Checklist / readiness card | `checklist` | `checklist: CheckItem[]` (`status`: `ok` \| `warn` \| `fail` \| `unknown`), per-item `link` |
| Navigate (deep link) | `navigate` | `link`, optional `rows` (e.g. current room status) |
| Guide + citation | `not_supported` (Phase 1) | no docs corpus yet: `message` + `link` to the screen where the task is done |
| Clarifying question | `clarify` | `options: Option[]` |
| Disambiguation | `disambiguation` | `options: Option[]` |
| Denied (RBAC) | `denied` | `message` only — never data or hints. Emitted when an ASI read returns 401/403 (Genie does no RBAC of its own) |
| Error (readable failure) | `error` | `code`, `title`, `message`, `retry` (what to do), optional `link`. Codes: `session_expired` (ASI 401: "Your session has expired, please log in again"), `forbidden` (403: "You don't have access to …"), `service_unavailable` (timeout/refused: "Couldn't load today's arrivals - the reservation service didn't respond. Please try again."), `service_error` (5xx), `not_found`, `not_available`, `unexpected`. Never contains stack traces, URLs or tokens |
| Not supported (captured) | `not_supported` | `message`, `captured: true`, optional `link` (fastest manual path) |
| Planner table | `table` | `table: { columns: Column[], rows }`, `total` when rows are a subset. Intent `agent_table` |
| Planner chart | `chart` | `chart: { kind, xKey, series, data, unit? }`. `kind`: `bar` \| `line` \| `area` \| `pie`. Intent `agent_chart` |
| Planner stats / detail | `data` | `rows: Field[]`. Intent `agent_stats` (headline numbers) or `agent_detail` (one record) |
| Planner list | `list` | `items`, `total`. Intent `agent_list` |

Planner answers (`present_answer`) may return any combination of those last four, in order stats, chart, table, list, detail. Caps: 8 stats, 8 columns, 50 table rows, 30 list items, 16 detail rows, 4 chart series, 60 points. `text` is the one- or two-sentence summary and does not repeat the table. Create/update refusals for a verified entity (room, space, room type, rate type, amenity, tax, other charge, building/floor, role, reservation, guest, email template, folio notice) are a `navigate` card to that ASI screen.

```ts
Link      { label: string; route: string; params?: Record<string,string> }   // route = asi-frontend path, params = query string. Reports: prefer `reportId` (catalog id) + `report` (componentKey); optional `date` or `startDate`/`endDate`
Column    { key: string; label: string; align?: "left"|"right"|"center" }
TableData { columns: Column[]; rows: Record<string,string>[] }
ChartSeries { key: string; label: string }
ChartData { kind: "bar"|"line"|"area"|"pie"; xKey: string; series: ChartSeries[]; data: Record<string, unknown>[]; unit?: string }
Field     { label: string; value: string; hint?: string }                     // hint e.g. "+4.0% vs previous day"
ListItem  { id?: string; title: string; subtitle?: string; badges?: string[]; link?: Link }
Period    { label: string; value?: number; display: string }
CheckItem { label: string; status: "ok"|"warn"|"fail"|"unknown"; detail?: string; link?: Link }
Option    { label: string; value: string; message: string }   // send `message` as the next chat message to answer
Citation  { id: string; source: string; title: string; snippet?: string }
Chip      { label: string; message: string }
```

## Other endpoints

| Endpoint | Purpose |
|---|---|
| `GET /health`, `GET /v1/genie/healthcheck` | status, provider, store (`postgres`), read-tool count, intent count |
| `GET /conversations/{sessionId}` | `{ sessionId, messages: [{ role, content, toolCalls, createdAt, durationMs }], feedbackByTurnId }` — assistant `toolCalls` JSON holds `{ turnId, intent, cards, toolCalls, notice, citations, chips }` |
| `POST /feedback` | `{ turnId, sessionId, rating: "thumbs_up"|"thumbs_down", comment? }` → updates `genie_turns.feedback` |
| `GET /intents` | the Phase 1 capability registry (from Scenarios.csv) |
| `GET /analytics/turns?from=&to=&propertyId=&limit=&format=csv` | one row per chat turn (Postgres `genie_turns`): createdAt, sessionId, propertyId, user id/name/email (auth current-user, cached 10 min per token), PAN-masked message, reply, cards, intent, scenarioId, source (llm/keyword/rule), confidence, slots, tools (name/status/error code/durationMs), llmModel, llmFailed, durationMs, errorCode/errorMessage (masked), feedback (null for now). JSON by default; `format=csv` downloads a CSV. `from`/`to` are YYYY-MM-DD or RFC 3339 (`to` exclusive) |
| `GET /events?kind=unsupported\|denied\|intent\|pci_block&limit=100` | redacted analytics: captured unsupported asks, denials |

All endpoints except health require a user token (`401` without one).

Errors: `400 {"error":"message is required"}`, `400` for a missing propertyId or a group, `401` no/invalid token, `403` property not in the user's permissions, `502` when the engine fails. Inside a turn, failures never surface raw errors: they become an `error` card (codes above). When every planned read fails because ASI is down, the reply is one `error` card ("I couldn't reach ASI just now. Please try again in a moment.") instead of a model-written answer.
