# AUDIT: SurrealDB Agent Memory (SAM) migration, Phase 0

Verified against this fork on branch `feat/surrealdb-agent-memory`. No code changes in this phase.
Classification: REMOVE / KEEP / MODIFY.

## Real paths (plan paths vs fork)

| Plan path | Real path | Status |
|---|---|---|
| `plugins/memory/<providers>` | `plugins/memory/{byterover,holographic,retaindb}` plus `__init__.py`, `config_schema.py`, `query_rewrite.py` | MODIFY: only 3 in-tree providers remain. honcho, mem0, hindsight, supermemory and openviking exist only as `plugin-catalog/*.yaml` entries |
| `agent/agent_init.py` | same | MODIFY |
| `agent/turn_context.py` | same (+ `turn_context_compaction.py`) | MODIFY |
| `agent/background_review.py` | same | MODIFY (replaced by `agent/learning/`) |
| `agent/memory_provider.py` (ABC) | same | KEEP |
| `agent/memory_manager.py` | same | MODIFY (single provider) |
| `tools/memory_tool.py` | same, plus `tools/memory_tool_store.py` | REMOVE |
| `hermes_cli/memory_provider_migration.py` | same | REMOVE (importer lives in the `agent_memory` plugin) |
| shutdown hook | `gateway/run_shutdown.py` | MODIFY |
| session reset/close | `gateway/session.py`, `gateway/run_turn.py`, `tui_gateway/` | MODIFY |
| `agent/learning/` | does not exist. Related: `agent/learning_mutations.py`, `agent/learning_graph.py` | New package. Check overlap before Phase 2 |
| `[tool.mypy]`, `[tool.pyright]` | absent. Only `[tool.ty.environment]` and `[tool.ty.rules]` (pyproject.toml:729) | Phase 1 adds them |
| CI | `.github/workflows/{ci.yaml,lint.yml,tests.yml,...}`, no `typecheck.yml` | Phase 1 adds it |

## Hit classification

### Built-in memory (MEMORY.md / USER.md / `memory` tool)
- REMOVE: `tools/memory_tool.py`, `tools/memory_tool_store.py`, tests `tests/tools/test_memory_tool.py`, `tests/agent/test_builtin_memory_disabled_surface.py`, `tests/agent/test_skip_memory_store.py`.
- MODIFY: `agent/system_prompt.py` (memory guidance block), `agent/context_compressor.py`, `agent/agent_init.py`, `agent/memory_manager.py`, `hermes_cli/memory_setup.py`, `hermes_cli/doctor_state.py`, `hermes_cli/config_defaults.py` (memory_enabled, user_profile_enabled, char limits).
- MODIFY: `tests/agent/test_memory_write_bridge.py`, `tests/agent/test_learning_mutations.py`, `tests/agent/test_run_agent.py`, `tests/tools/test_write_approval.py`.
- KEEP / review: `optional-skills/migration/openclaw-migration/` (reads MEMORY.md for import; not core).

### Session search (stays local)
- KEEP: `hermes_state*.py` (FTS5), `model_tools.py`, `tools/` session_search, `gateway/session_transcript.py`.

### Learning loop
- MODIFY/REPLACE: `agent/background_review.py` (69 hits of nudge/skip_memory/review), `agent/agent_init.py`, `run_agent.py`, `agent/turn_iteration_prep.py`.
- MODIFY: tests `test_background_review*.py`, `test_skip_background_review.py`, `test_review_idle_queue.py`, `tests/tui_gateway/test_*cancels_background_review.py`, `tests/hermes_state/test_background_review_session_isolation.py`. These encode current contracts, so port them to the new scheduler.
- Config keys `nudge_interval` / `creation_nudge_interval` stay as aliases for one release.

### Skills
- KEEP: `tools/skill_manager_tool.py`, `skill_manager_batch.py`, `skill_manager_guards.py`, `agent/curator.py`.
- MODIFY: add SkillEvent emission to `skill_manager_tool.py` and `agent/curator.py` (the bridge must not import `agent/`).

### Other providers
- REMOVE: `plugins/memory/{byterover,holographic,retaindb}`, their tests under `tests/plugins/` and `tests/plugins/memory/`, `plugin-catalog/{honcho,mem0,supermemory,openviking,hermes-memory-ui,hermes-dreaming}.yaml` (confirm the last two are memory providers), `optional-skills/autonomous-ai-agents/honcho/`.
- MODIFY: `plugins/memory/__init__.py` (discovery), `plugins/memory/config_schema.py`, `hermes_cli/web_routers/memory_providers.py`, `hermes_cli/web_server_memory.py`, `hermes_cli/memory_oauth.py`, `hermes_cli/memory_provider_migration.py` (REMOVE), `hermes_cli/main.py`, `hermes_cli/_parser.py`, `hermes_cli/banner.py`, `hermes_cli/profiles.py`, `hermes_cli/backup.py`, `agent/tool_executor.py`, `agent/redact.py`, `pm/install.py`, `plugins/AGENTS.md`, `hermes_cli/AGENTS.md`, READMEs.
- Many `tests/gateway/*` hits are incidental string matches (probably "honcho"/"mem0" in fixtures); triage individually in Phase 3 by running `rg` per file.
- `agent/memory_provider.py`, `agent/memory_manager.py`, `gateway/run_shutdown.py`: provider ABC and dispatch, KEEP the ABC.

## SDK facts (`surrealdb[memory]`, resolved `surrealdb==3.0.0b10`, `surrealdb-memory==1.0.0b1`)

Install: `uv pip install --prerelease=allow 'surrealdb[memory]'`. Pin both exact versions in the extra.

`surrealdb.memory.Memory(context: str, *, endpoint=None, api_key=None, timeout=30.0, max_retries=3, transport=None)`. `AsyncMemory` also exists. Methods (all accept `on_behalf_of`):

- `remember(text=None, *, infer, session_id, scopes, role, memory_category, labels, triples) -> RememberResponse`
- `remember_many(items: Sequence[Mapping], *, session_id, scopes, extract, infer, labels) -> RememberBatchResponse`
- `recall(query, *, k, mode, session_id, include, as_of, at_instant, labels, lens, scope_view, valid_from, valid_until, source, location) -> RecallResponse` (`hits: list[RecallHit(id, score, source, text)]`, `tier`, `trace`, `query_ms`)
- `query_context(query, *, k, labels, lens, scope_view) -> ContextQueryResponse`
- `consolidate(*, dry_run, fact_limit, observation_limit) -> ConsolidateResponse`
- `reflect(query, *, persist=False) -> ReflectResponse`
- `forget(query, *, purge=False) -> ForgetResponse`
- `profile() -> ProfileResponse(static, dynamic, preferences: list[ProfileEntry(key,value)], instructions: list[dict])`
- Also: `elaborate`, `inspect`, `chat`, `audit`, `health`, `state`, `whoami`, `close`.
- Sub-APIs: `scopes` (`register`, `list`, `delete`, `forget`), `traces` (`get`, `list`, `stats`), plus sessions, documents, entities, keys, principals, lifecycle.
- Errors: `MemoryAPIError`, `MemoryAuthError`, `MemoryNotFoundError`, `MemoryScopeError`, `MemoryServiceError`.

### Gaps and plan adjustments
1. **No category filter on `recall`.** Categories are reachable through `profile()` (`instructions`, `preferences`, `static`). Build CoreProfile from `profile()` instead of a filtered recall; this is better than the plan's client-side filter. Bounded by `core_profile_max_chars`.
2. **No retrieval-feedback or usefulness API.** `traces` is read-only (`get/list/stats`). Skip feedback in the review hook and note this gap.
3. **`recall` has no `categories` arg.** Use `labels` and `include`.
4. **Sync SDK.** `Memory` is blocking, so calls run in worker threads (`spawn_context_thread`). The SDK `timeout` must be < 8s (MemoryManager prefetch).
5. **`remember_many(extract="whole_conversation")` and `remember(infer=...)` value sets are undocumented in the signatures.** Confirm against the Sandbox in Phase 4 (`@pytest.mark.live`) before relying on them.
6. **`scopes` is `ScopeArg`.** `scopes.register` handles `scopes-init`.
7. **Scope for skills:** the `documents` API exists, so the plan's `documents`-namespace option is viable. Default to labelled memories; revisit if skill pointers recall poorly.
8. Not verified: official plugin `surrealdb/agent-memory-hermes` (no network clone done). Review it before Phase 4 for naming and config parity.

## Provider ABC hook surface (agent/memory_provider.py)
`is_available`, `initialize(session_id, **kwargs)`, `system_prompt_block`, `prefetch`, `queue_prefetch`, `sync_turn`, `on_turn_start`, `on_session_end`, `on_session_switch`, `on_pre_compress`, `on_delegation`, `on_memory_write(action, target, content, metadata)`. The plan's hook mapping fits. `on_memory_write` is the origin-metadata seam for background review.

## Recommended PR sequence
0 AUDIT (this) → 1 typing infra + evals baseline → 2 `agent/learning/` → 3+4 provider swap in one PR (so main always has memory) → 5 import → 6 evals and RISKS.md.
