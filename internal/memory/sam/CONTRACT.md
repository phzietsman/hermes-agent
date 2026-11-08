# SAM wire contract

Derived by reading `surrealdb-memory==1.0.0b1` (the wheel behind `surrealdb.memory`) and confirmed with one
live read-only `GET .../me` (200, camelCase JSON). The Python reference client is
`plugins/memory/agent_memory/client.py`, a thin adapter over this SDK. A Go client talks plain HTTP/JSON.

## Base
- Endpoint: `AGENT_MEMORY_ENDPOINT` / `endpoint` in `agent-memory.json`, trailing `/` trimmed.
- Context id: `context` in config, URL-escaped with `quote(value, safe="")` (everything escaped, including `/`).
- Base path: `{endpoint}/api/v1/{context}`. Health (not context-scoped): `GET {endpoint}/api/v1/health`.
- Secret: `AGENT_MEMORY_API_KEY`. The SDK reads no env vars itself.

## Headers (every request)
- `Authorization: Bearer <api key>`
- `Accept: application/json`
- `User-Agent: surrealdb-py-spectron/1.0` (free to change in Go)
- `Content-Type: application/json` when a body is sent
- `Idempotency-Key` on `facts` and `facts/batch` only: hex sha256 over `METHOD \0 path \0 compact-json-body \0 bucket`,
  where `bucket = floor(unix_seconds / 30)`, JSON compact (`separators=(",", ":")`) and the path includes the base path.
- `X-Memory-On-Behalf-Of: <principal id>` optional delegation. Not used by Hermes.

## Retries and timeouts
- Default timeout 30s, default `max_retries` 3 (Hermes sets `timeout_s`=20, `max_retries`=2).
- Backoff schedule is fixed: 0.25s, 0.5s, 1.0s (truncated to `max_retries`).
- Retry only if the attempt count is below the limit AND (method is GET OR the call is marked idempotent) AND
  (the failure is a connection error/timeout OR the status is >= 500). Idempotent calls: `facts`, `facts/batch`.
  Recall, context, forget, consolidate, reflect (all POST) are NOT retried.
- A transport failure after retries becomes an error with status 0 and message `Connection failed: <exc>`.

## Errors
Any status >= 400 (after retries) is an error. Message = body `message`, else `title`, else `error`, else the body
string, else `Memory request failed`. `X-Trace-Id` response header is the trace id.
Classes: 401 auth, 403 scope, 404 not-found, anything else generic.

Hermes mapping (keep in Go):
| Condition | Class | Breaker | Outbox |
|---|---|---|---|
| 401 | AuthFailed | opens immediately | keep |
| 403, 404, other 4xx except 408/429 | Rejected | untouched | drop the entry |
| 408, 429, 5xx, status 0 | Unavailable | counts | keep |
| timeout (message contains `timed out`, or Go `context.DeadlineExceeded`) | Timeout (subclass of Unavailable) | reads do not count it | keep |

## Scopes
A scope argument is DNF: `[][]string`, an OR of AND-clauses of slash paths. A bare string is `[["x"]]`, a flat list
`["a","b"]` is `[["a"],["b"]]`, a nested list is one AND clause. Empty paths dropped, per-clause de-duplicated,
empty clauses dropped. Empty result means omit the field (server default write region).

## Endpoints used by Hermes
All paths are relative to the base path. JSON keys are as shown; `null` fields are omitted from requests.

| Method | Route | Request body | Response |
|---|---|---|---|
| `remember` | `POST /facts` (idempotent) | `{text, infer?, session_id?, scopes?, role?, memory_category?, labels?, triples?}` | `{mode, session_id, chunk_id?, extraction?, preview?, turn_id?}` |
| `remember_many` | `POST /facts/batch` (idempotent) | `{messages:[{role,content}...], session_id?, scopes?, extract?, infer?, labels?}` | `{session_id, turn_ids[], extractions[]}` |
| `recall` | `POST /query` | `{query, k?, mode?, sessionId?, include?, asOf?, atInstant?, labels?, lens?, scopeView?, validFrom?, validUntil?, source?, location?}` | `{hits:[{id,score,source,text}], queryMs, tier, classificationKind, seedEntities, trace}` |
| `query_context` | `POST /context` | `{query, k?, labels?, lens?, scopeView?}` | `{context, queryMs, tier}` |
| `forget` | `POST /forget` | `{query, purge?}` | `{deleted}` |
| `consolidate` | `POST /consolidate` | `{dryRun?, factLimit?, observationLimit?}` (or no body) | `{created, updated, superseded, dryRun, outcomes[], traceId}` |
| `reflect` | `POST /reflect` | `{query, persist?}` | `{reflection, evidence[], persistedAttributes[], traceId}` |
| `profile` | `GET /profile` | none | `{dynamic[], preferences[], static[], instructions[]}`; entries are `{key,value}`, instructions are free objects (text in `description`, `text`, `value` or `content`) |
| `scopes.register` | `POST /scopes` | `{path, description?, displayName?}` | scope node |
| `whoami` | `GET /me` | none | `{principalId, kind, displayName, grants, effectiveGrants, enforce?, ...}` |
| `health` | `GET {endpoint}/api/v1/health` | none | JSON object |

Notes:
- Mixed key casing is real: `remember`/`remember_many` bodies use snake_case (`session_id`, `memory_category`), `recall`
  and the others use camelCase (`sessionId`, `scopeView`). Responses are camelCase; the SDK also accepts snake_case.
- Service rules learned live (DECISIONS D32): `memory_category` only accepts `identity`/`knowledge`/`context`;
  `session_id` must be one the service issued (Hermes sends a `session=<id>` label instead); labels must be `key=value`;
  recall `mode` accepts `hybrid`, `vector`, `bm25`.
- Hermes does not use `infer` or `extract` (D11).
- Latency: recall is 2 to 25s on the hosted sandbox (D31), so the Go client must honour `prefetch_wait_s`.

## Not ported
`chat` (SSE), `audit`, `elaborate`, `inspect`, `state`, keys, principals, entities, sessions, traces, documents,
lifecycle namespaces. Not used by Hermes.

## Go client checklist
- Pure `net/http` with a context per call; no SDK.
- Reproduce the idempotency key exactly, or the server cannot dedupe retried writes.
- Unit tests use `httptest`, plus `-m live` equivalent behind a build tag `live` reading `.secrets/sam-live.env`.
