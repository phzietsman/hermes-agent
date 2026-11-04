# SPEC: All-in SurrealDB Agent Memory + reliable learning loop for the Hermes fork

## Goal
Make SurrealDB Agent Memory (SAM) the only memory layer, and make the agent's learning loop
reliable (reviews always run, writes always land, failures are visible and replayable).

## Global rules (every task)
- STRICT TYPING IS MANDATORY in all new and touched modules: full annotations,
  `from __future__ import annotations`, no implicit Any, no untyped defs. Use Pydantic v2 for
  config and IO models, TypedDict for JSON/tool payloads, Protocol for seams (SAM client, queue),
  Literal/Enum for modes. `# type: ignore[code]` only with a justification comment.
- Done = `uv run mypy --strict <scope>`, `uv run pyright <scope>`, `uv run ruff check`, and the
  relevant pytest all pass.
- Paths below come from upstream NousResearch/hermes-agent and are UNVERIFIED for this fork.
  Verify each with `rg`/`ls`; if missing, locate by symbol and record the real path in AUDIT.md.
- No network in unit tests. SAM sits behind a Protocol and is faked.
- One phase per PR, conventional commits. Tag `pre-sam` before Phase 3.

## Target architecture
  user turn -> agent loop
     |-- prompt build: [system] + CoreProfile (SAM identity+instructions, cached) +
     |                 Recalled (SAM hybrid recall, per turn)
     |-- tools: agent_memory_{recall,remember,forget,reflect}, session_search (local FTS5),
     |          skill_view/skill_manage (files)
     |-- turn end -> MemoryManager.sync_turn -> SAM episodic (async, outbox-backed)
     |-- ReviewScheduler (durable queue in state.db) triggers:
     |       iterations>=N | session end/reset/compress | idle>=T | shutdown
     |   -> ReviewRunner (fork WITH provider write access) -> SAM remember/reflect + skill_manage
     |   -> SkillsBridge mirrors skill create/patch/archive into SAM (kind=skill)
     |-- Outbox (jsonl or state.db table): failed SAM writes, replayed on next start/turn
  Degraded mode: breaker open -> CoreProfile from local cache, writes -> outbox, WARN once/session.

## Phase 0 - Audit (no code changes; produce AUDIT.md)
Run and record:
- ls plugins/memory/ ; rg -l "MemoryProvider|register_memory_provider" --type py
- rg -n "memory_tool|MEMORY\.md|USER\.md|memories/|frozen snapshot" --type py
- rg -n "session_search|state\.db|FTS5" --type py
- rg -n "background_review|nudge_interval|creation_nudge_interval|skip_memory|_review" --type py
- rg -n "skill_manage|skill_manager|curator|skill_provenance" --type py
- rg -n "byterover|retaindb|honcho|mem0|hindsight|supermemory|openviking|holographic" -g '!uv.lock'
- rg -n "memory_provider_migration|hermes_agent\.(memory_providers|plugins)" .
- rg -n "\[tool\.(ty|mypy|pyright)" pyproject.toml ; ls .github/workflows
- Inspect the official plugin (surrealdb/agent-memory-hermes) and the installed SDK:
  `uv pip install --prerelease=allow 'surrealdb[memory]'` then
  `python - <<'EOF'\nimport inspect, surrealdb.memory as m; print([n for n in dir(m) if not n.startswith('_')])\nEOF`
  and record the real signatures of remember, remember_many, recall, query_context, consolidate,
  reflect, forget, and any feedback/usefulness/trace API.
AUDIT.md classifies each hit REMOVE / KEEP / MODIFY and lists SDK gaps. No other changes.
Acceptance: AUDIT.md committed.

## Phase 1 - Typing infra, eval harness, baseline
1. pyproject: dev deps mypy, pyright, pydantic>=2, pytest-asyncio. Add
   [tool.mypy] strict=true, warn_unreachable=true, files=[new/touched modules]
   [tool.pyright] typeCheckingMode="strict", include=[same]. Leave existing [tool.ty] alone.
2. .github/workflows/typecheck.yml: mypy --strict, pyright, ruff, pytest (memory + agent) on PR.
3. Build evals/memory/ (see Phase 6) and run against current main. Commit baseline.json.
Acceptance: CI green; baseline committed. (Do this BEFORE changing behaviour.)

## Phase 2 - Reliable learning loop (provider-agnostic; works on current builtin memory)
Add package agent/learning/ (typed):
  models.py
    class ReviewTrigger(str, Enum): ITERATIONS, SESSION_END, SESSION_RESET, PRE_COMPRESS, IDLE, SHUTDOWN
    class ReviewJob(BaseModel, frozen): job_id: str; session_id: str; trigger: ReviewTrigger;
        start_msg_idx: int; end_msg_idx: int; transcript_digest: str; attempts: int = 0;
        created_at: datetime; status: Literal["pending","running","done","failed"]
  queue.py
    class ReviewQueue(Protocol): enqueue(job) -> bool; claim() -> ReviewJob | None;
        complete(job_id) -> None; fail(job_id, error: str) -> None
    SqliteReviewQueue in state.db table `review_jobs`; UNIQUE(session_id, transcript_digest) for
    idempotency (re-enqueue of same range is a no-op).
  scheduler.py
    class ReviewSettings(BaseModel): iteration_interval: int = 10; idle_seconds: int = 900;
        max_attempts: int = 5; backoff_base_s: float = 30.0; review_on_boundary: bool = True
    ReviewScheduler counts TOOL ITERATIONS (not user turns), fires on boundaries (/new, /reset,
    compression, close, shutdown) and on idle timeout (timer reset each turn).
  runner.py
    ReviewRunner executes a job in a fork that is bound to the parent MemoryManager (NOT skip_memory)
    so writes reach the provider with metadata origin="background_review". Cancellation by the next
    user message must NOT consume the job: on interrupt, mark pending with attempts unchanged.
    Failures retry with exponential backoff up to max_attempts, then status=failed + WARNING.
Wire-in (locate real sites via Phase 0): agent/agent_init.py, agent/turn_context.py,
agent/background_review.py, the session reset/close paths, process shutdown hook.
Replace the existing nudge-counter logic; keep config keys as aliases for one release.
Tests (tests/agent/learning/): fake queue + fake provider; cases: iteration trigger fires in a
tool-heavy single user turn; interrupted review is retried; same range is not reviewed twice;
boundary and idle triggers enqueue; review writes call on_memory_write with origin; max_attempts.
Acceptance: all tests pass; re-run evals -> evals/memory/loopfix.json shows write-landing rate up
even with builtin memory.

## Phase 3 - Remove everything except the new provider (tag `pre-sam` first)
- Delete plugins/memory/<all except agent_memory>, their tests, extras in pyproject, uv.lock entries.
- Delete tools/memory_tool.py, MEMORY.md/USER.md load/inject/consolidation code, the `memory` tool
  registration, and config keys memory_enabled / user_profile_enabled / char limits.
- Delete catalog entries and hermes_cli/memory_provider_migration.py (keep a one-shot importer, Phase 5).
- Collapse MemoryManager to exactly one provider (agent_memory) but keep the ABC + hook dispatch;
  remove multi-provider selection and the builtin-provider-first logic.
- Remove docs for deleted providers; fix Docusaurus sidebar ids.
- System prompt: remove the built-in memory guidance block; replace with the agent_memory block (Phase 4).
Acceptance: `rg -i "byterover|retaindb|honcho|mem0|hindsight|supermemory|openviking|holographic|MEMORY\.md|USER\.md"`
returns only CHANGELOG/history; full suite green; docs build green.
(Phase 3 and 4 may land in one PR so main is never without memory.)

## Phase 4 - SAM provider (plugins/memory/agent_memory/)
Files: __init__.py, plugin.yaml, config.py, client.py, provider.py, core_profile.py, outbox.py,
       tools.py, skills_bridge.py, cli.py, py.typed
Dependency: extra `agent-memory = ["surrealdb[memory]==<pinned prerelease>"]`; document `--prerelease=allow`.

config.py
  class RecallMode(str, Enum): HYBRID="hybrid"; CONTEXT="context"; TOOLS="tools"
  class AgentMemorySettings(BaseModel):  # frozen, extra="forbid"
      endpoint: HttpUrl; context: str; api_key: SecretStr
      default_scope: str = "user/default"; agent_scope: str = "agent/hermes"
      recall_mode: RecallMode = RecallMode.HYBRID
      top_k: int = Field(5, ge=1, le=20); max_prefetch_chars: int = Field(4000, ge=500, le=20000)
      core_profile_max_chars: int = Field(1500, ge=200, le=6000)
      timeout_s: float = Field(6.0, gt=0, lt=8.0)   # < MemoryManager prefetch timeout (8s)
      max_retries: int = 2; breaker_threshold: int = 3
      send_tool_outputs: bool = False; consolidate_on_end: bool = True
  Secret via agent.secret_scope ("AGENT_MEMORY_API_KEY"); non-secrets in $HERMES_HOME/agent-memory.json.
  The SDK reads no env vars: pass everything explicitly.

client.py: `MemoryClient` Protocol (remember, remember_many, recall, query_context, consolidate,
  reflect, forget, close) with TypedDict/dataclass results; `SurrealMemoryAdapter` wraps
  surrealdb.memory.Memory. Fit the Protocol to the REAL SDK signatures from AUDIT.md; never change
  call sites to match the SDK. MemoryAuthError opens the breaker immediately.

core_profile.py
  CoreProfile = recall(lens=[default_scope], categories identity+instructions, bounded to
  core_profile_max_chars), refreshed at session start and after any remember/forget of those
  categories. Persist last good copy to $HERMES_HOME/core_profile_cache.json (atomic write).
  Injected every turn as "## About the user / standing instructions". On breaker-open, serve cache.
  (If the SDK cannot filter by category, filter client-side by label/category on hits; note in AUDIT.md.)

provider.py (implements MemoryProvider):
  - is_available(): key + endpoint + context present; no network.
  - initialize(): bind session/profile; for agent_context in {"cron","subagent"} disable auto-writes.
  - system_prompt_block(): when to call agent_memory_remember (durable facts, corrections,
    preferences, lessons), recalled memory appears under "## Recalled memory", procedures go in skills.
  - prefetch(query): hybrid recall k=top_k, format "- <text> (score)" capped at max_prefetch_chars;
    TOOLS mode returns "". Errors -> "", count toward breaker.
  - queue_prefetch: spawn_context_thread for next-turn warmup.
  - sync_turn: non-blocking remember_many([user, assistant], extract="whole_conversation");
    drop raw tool outputs unless send_tool_outputs.
  - on_pre_compress / on_session_end: idempotent remember_many (label transcript digest);
    on_session_end then consolidate() if consolidate_on_end.
  - on_memory_write: map add/replace -> remember(labels origin=...), remove -> forget(purge=False).
  - Every SAM write goes through outbox.py: on failure append typed OutboxEntry (jsonl, atomic),
    WARNING log; replay on initialize() and every N turns; `hermes agent_memory replay` for manual.
  - Circuit breaker: threshold consecutive failures or auth error -> degraded mode for the session.

tools.py (TypedDict schemas; names must not collide): agent_memory_recall(query,k),
  agent_memory_remember(text, kind: Literal["fact","preference","instruction","lesson"]),
  agent_memory_forget(query, purge=False), agent_memory_reflect(query, persist=False).

skills_bridge.py: typed SkillEvent (create/patch/archive) emitted by skill_manage and the curator
  (no import cycle into agent/). On create/patch -> remember("Skill '<name>': <description>. Use when:
  <triggers>", labels [kind=skill, skill=<name>], memory_category="instructions"); archive ->
  forget(..., purge=False). In prefetch, hits labelled kind=skill append "Relevant skills: <names>
  (load with skill_view)". Skills remain files on disk; SAM holds the discoverability pointer only.
  If AUDIT.md shows the `documents` namespace fits skill bodies better, switch to it behind the same bridge.

learning hook: ReviewRunner (Phase 2) ends each job with
  1) remember lessons extracted by the review (kind=lesson) and 2) reflect(query=<session summary>,
  persist=True) to crystallise patterns. If the SDK exposes retrieval feedback/usefulness, call it when
  a recalled memory was cited in a successful turn; otherwise skip and note the gap.

cli.py: `hermes agent_memory status | doctor | scopes-init | replay | import`.
  scopes-init registers user/<name> and agent/hermes (SAM requires scopes registered first).
Tests (tests/plugins/memory/agent_memory/): FakeMemoryClient; hook->call mapping; prefetch under
  timeout; breaker + degraded mode serves cached core profile; outbox replay idempotency; secrets
  never in payloads; cron/subagent write suppression; skills bridge; `@pytest.mark.live` Sandbox
  tests skipped without env vars.
Acceptance: mypy --strict + pyright strict clean on the package; tests green; `hermes agent_memory doctor` passes
  against the Sandbox; a manual two-session smoke test recalls a fact from session 1 in session 2.

## Phase 5 - Migration
`hermes agent_memory import`: read old MEMORY.md/USER.md (split on §) and remember each entry with
labels origin=import (user entries -> identity/instructions); optional rate-limited backfill of the last N
sessions from state.db via remember_many. Dry-run flag prints what would be written. Idempotent via digests.
Acceptance: import twice -> no duplicates; core profile shows imported preferences.

## Phase 6 - Evaluation (typed, seeded, judge model pinned)
evals/memory/ scenarios, each run in fresh sessions:
 1 Recall across 10 sessions (30 facts)    2 Update/supersession (+ "as of" query)
 3 Abstention on unknown facts             4 Preference following 5 sessions later
 5 Procedural: repeat a 6+ tool-call task; measure tool calls/time/errors, skill created & reused
 6 Write-landing: claimed saves vs actual SAM writes (from outbox/trace logs)
 7 Resilience: kill network mid-session -> degraded mode serves core profile, outbox replays, nothing lost
 8 Regression: full pytest, prefetch p95, tokens per session incl. SAM usage
Matrix: {baseline, loop-fix only, loop-fix + SAM} x {daily model, ~30B local model}.
Gates to ship: >=15pt gain on 1+2 vs loop-fix-only; no regression on 5; write-landing >=95%;
prefetch p95 < 1.5s; scenario 7 loses zero writes; monthly tokens within plan.

## Rollback / ops
- Tag `pre-sam`; Phase 3 is revertable by git. Because provider code is isolated, a revert restores builtin memory.
- Document data egress (turns leave the machine; secrets are redacted, paths/commands may not be).
- RISKS.md: hosted-only dependency, prerelease SDK (pin + contract tests), preview access status,
  background consolidation token cost, recall noise (cap top_k/chars), vendor pricing changes.