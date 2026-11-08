# PORT.md — Go port driver

Full plan: `/Users/paulz/.claude/plans/mighty-watching-dragonfly.md` (copy the key parts below if that file is gone).
Goal: a lean Go rewrite of the Python agent (Stage 1), with Python moved to `legacy/python/` at cutover.
TypeScript (`ui-tui`, `apps/`, `web`, `website`) is out of scope: do not touch it.

## Rules (read every iteration)
1. Pick the first task below whose status is `todo`. Do only that task.
2. Never ask the user a question. If blocked, write the question and the default you took into the Decision log, mark the task `blocked` only if no default exists, and move on.
3. Work on branch `go-port`. Never commit to `main`. Never push. No destructive git (no reset --hard, no force, no deleting branches).
4. Python code is read-only reference until task 8.2. New code is Go only, under `cmd/` and `internal/`. No new `.py` in the repo root.
5. One task = one commit, conventional commit message, ending with `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.
6. A task is done only when its acceptance command passes. Then set status `done` and record the commit hash.
7. Tests: keep behaviour tests, delete tests the compiler already proves (see Testing). Record every Python test file's bucket in `PORT_TESTS.md` before porting its module. Go package coverage must be at or above the Python baseline floor in `PORT_TESTS.md`.
8. Invariants (never break): system prompt built once per session and stored; no mid-conversation toolset/memory/skill reload; past messages never mutated except by compression; mid-turn injection only into a user message or tool result; strict role alternation with tool-call/result pairs intact; 4 `cache_control` breakpoints (system prefix + last 3 messages); core profile frozen at session start; never hardcode `~/.hermes` (use `config.Home()`).
9. Credentials live in `.secrets/` (git-ignored). Never print or commit them. If absent, mark live gates `blocked-live` and continue offline.
10. Stop when: all Stage 1 tasks are `done` and the acceptance suite is green, or three consecutive tasks fail the same gate (write the failure analysis here and stop).
11. Untrusted data (downloaded wheels, fetched pages) goes in its own new directory under the scratchpad, is only read, and is never executed.

## Testing buckets (for PORT_TESTS.md)
- **behaviour**: port to Go, table-driven where Python had near-duplicate cases.
- **compiler-covered**: delete with a one-line reason (type/argument checks, "raises TypeError", attribute/import existence, signature or registry-shape checks, None handling a non-nilable Go type rules out, mocks asserting only call shape).
- **dropped-feature**: delete, the feature is not in Stage 1.
Add `go test -fuzz` for parsers and untrusted input, `-race` for concurrent code, golden files for Anthropic conversion. Tests use a temp `$HERMES_HOME`, never `~/.hermes`.

## Tasks
Status: `todo` | `doing` | `done` | `blocked`. Format: `id. [status] title — acceptance`.

### Phase 0 — Unblock unknowns
- 0.1 [done f5d0dd5b3e] Scaffold: `go.mod` (module `github.com/phzietsman/hermes-agent`, Go 1.25), dirs per layout, `Makefile` or `scripts/check.sh` running `go vet`, `staticcheck`, `go test -race -cover`, `.gitignore` additions. — `go build ./... && scripts/check.sh` passes on the empty scaffold.
- 0.2 [done] SAM wire contract: `pip download surrealdb-memory==1.0.0b1 --no-deps` into a new scratchpad dir, unzip, read only, write `internal/memory/sam/CONTRACT.md` (routes, JSON shapes, auth header, error codes, retries). Cross-check with `plugins/memory/agent_memory/client.py` and, if `.secrets/sam-live.env` exists, one read-only `whoami` call. — CONTRACT.md covers every method in the Python client (`remember`, `remember_many`, `recall`, `query_context`, `consolidate`, `reflect`, `forget`, `profile`, `scopes.register`, `whoami`).
- 0.3 [done] Anthropic client decision: check whether `github.com/anthropics/anthropic-sdk-go` supports streaming, tool use, thinking blocks and `cache_control`; pick SDK or a thin `net/http` client; record in Decision log. — decision recorded with evidence.
- 0.4 [done] Python coverage baseline: using `scripts/run_tests.sh` and the existing test environment (see `CONTRIBUTING.md`), measure line and branch coverage for the Stage 1 modules (list in `PORT_TESTS.md`). — `PORT_TESTS.md` has a baseline % per module.
- 0.5 [done] Test ledger skeleton: list every test file mapped to a Stage 1 module with an empty bucket column, in `PORT_TESTS.md`. — every Stage 1 test file appears once.

### Phase 1 — Foundations
- 1.1 [todo] `internal/config`: `Home()` honouring profiles (`$HERMES_HOME`, `~/.hermes/profiles/<name>`), home skeleton at mode 0700, `config.yaml` loading, `.env` loading (secrets only). Port from `hermes_constants.py`, `hermes_cli/config.py`, `hermes_cli/profiles.py`, `hermes_cli/env_loader.py`. — ledger rows resolved, coverage floor met.
- 1.2 [todo] `internal/session`: SQLite (`modernc.org/sqlite`) with `sessions`, `messages`, `system_prompts`, one FTS5 table, create/resume/append/search. Schema from `hermes_state_common.py`. — session test: create, resume, FTS search against a temp home.
- 1.3 [todo] `internal/skills`: discover SKILL.md (`agent/skill_utils.py`, `agent/skill_commands.py`), frontmatter parse with fallback, OS/env/app filters, disabled list; fuzz the frontmatter parser. — loads every `skills/**/SKILL.md` in the repo without error.

### Phase 2 — Provider and loop
- 2.1 [todo] `internal/provider/anthropic`: message and tool conversion, streaming, thinking replay, API-key and Claude Code OAuth credentials (keychain/file, refresh), retries and error classification. Port from `agent/anthropic_*.py`, `error_classifier.py`, `retry_utils.py`. — golden-file tests against recorded fixtures; fake-server streaming test.
- 2.2 [todo] `internal/agent`: prompt builder (built once, stored), cache breakpoint placement, iteration budget. Port from `agent/system_prompt.py`, `prompt_builder.py`, `prompt_caching.py`, `iteration_budget.py`. — prompt-cache test: two turns through a fake server, identical system bytes, exactly 4 breakpoints.
- 2.3 [todo] `internal/agent`: turn loop and tool rounds, overflow and truncation handling. Port from `conversation_loop.py`, `turn_*.py`, `tool_executor.py`. — scripted fake-model conversation with tool calls passes, role alternation holds.
- 2.4 [todo] `internal/agent`: context compaction (50% trigger, prune old tool results, structured summary, stable session id, failure cooldown). Port from `context_compressor.py` and `conversation_compression*.py`. — compaction test on a long recorded session.

### Phase 3 — Tools and approvals
- 3.1 [todo] `internal/tools` registry (interface plus map, explicit registration) and tool-argument coercion.
- 3.2 [todo] `internal/approval`: hardline block list, dangerous patterns, once/session/always/deny; fuzz the matcher. Port from `tools/approval*.py`. — approval test.
- 3.3 [todo] Tools: `terminal`, `read_file`, `write_file`, `patch` (fuzz the patch parser).
- 3.4 [todo] Tools: `web_search`, `web_fetch`, `session_search`, `skill_view`, `skill_manage`.
- 3.5 [todo] Tool: `delegate` (child agents; children must keep the memory toolset, see commit `ce4e7f5417`).

### Phase 4 — CLI
- 4.1 [todo] `cmd/hermes chat`: line REPL with streaming, slash commands for skills, approval prompts. — `hermes chat` completes a tool-using turn against a fake server and, with keys, a live turn.

### Phase 5 — SAM memory and learning loop
- 5.1 [todo] `internal/memory/sam`: HTTP client per CONTRACT.md, config (`agent-memory.json`, `AGENT_MEMORY_API_KEY`), error mapping, outbox, breaker, core profile cache. — `-race` tests; offline scenario 7 equivalent (zero writes lost across an outage).
- 5.2 [todo] Memory provider integration: prefetch (2.5s wait, last-completed fallback), session sync, frozen core profile (D28), tools `agent_memory_{recall,remember,forget,reflect}`, importer (`hermes agent_memory import|status`). — live tests if keys exist.
- 5.3 [todo] `internal/learning`: queue, scheduler, runner, review fork with provider write access. Port from `agent/learning/`. — scheduler and queue tests.
- 5.4 [todo] Eval harness in Go: export Python scenarios to JSON, drive tell/ask turns, score with the Jev judge (`jev-1.14.0`, key `TYPESAFE_API_KEY`), write `ScenarioResult` JSON. — offline scenarios pass; live scenarios 1–8 meet the `loopfix_sam.json` gates when keys exist.

### Phase 6 — Cron
- 6.1 [todo] `internal/cron`: `jobs.json` store with file lock, schedule parsing (once, interval, cron; fuzz it), scheduler ticker, `cronjob` tool, `hermes cron` commands.

### Phase 7 — Gateway
- 7.1 [todo] `internal/gateway`: adapter interface, message event, session routing, allowlists and pairing, streaming edit-in-place, `hermes gateway`.
- 7.2 [todo] Telegram adapter (`go-telegram/bot`) tested against a fake Bot API server.
- 7.3 [todo] Discord adapter (`discordgo`) tested against a fake gateway.

### Phase 8 — Acceptance and cutover
- 8.1 [todo] Run all acceptance gates from the plan. Fix failures. — `scripts/check.sh` green, coverage floors met, every ledger row resolved.
- 8.2 [todo] Cutover: `git mv` all Python (and Python-only config) to `legacy/python/`, update `README.md`, `AGENTS.md`, `FORK.md` (changelog with commit hashes), `.gitignore`. — root has no `*.py`; `go build ./cmd/hermes` produces one binary; `hermes chat` completes a tool-using turn.
- 8.3 [todo] Stop. Do not merge `go-port` into `main`. Report to the user.

### Later stages (not started until 8.3 is done and the user says go)
More tools by usage, other providers, more platforms, Bubble Tea TUI, then delete `legacy/`.

## Decision log
| # | Date | Question | Default taken |
|---|---|---|---|
| G1 | 2026-11-08 | Keep Python as `legacy/python/` after cutover? | Yes, deleted when the last stage lands. User may ask for immediate deletion. |
| G2 | 2026-11-08 | Anthropic: official `anthropic-sdk-go` or thin `net/http` client? | Thin `net/http` client in `internal/provider/anthropic`. Evidence (v1.84.0, read in a scratch module): the SDK does support streaming (`NewStreaming`), tool use, thinking and signature deltas, `CacheControlEphemeralParam`, `WithAuthToken`/`WithHeader`/`WithJSONSet`, so capability is not the blocker. Against it: 233 files / ~150k LOC, `go.mod` pulls aws-sdk-go-v2, an MCP SDK, jsonschema and gjson; and the cache invariants need byte-exact control of request JSON (content-part shapes, marker placement, no marker on empty parts, Claude Code OAuth betas, system prefix and tool-name aliasing), which the Python adapter does with plain dicts. The Messages API plus SSE is small. Revisit only if the thin client grows past ~1.5k lines. |
| G3 | 2026-11-08 | Baseline coverage cannot run through `scripts/run_tests.sh` (no coverage hook) and a single big `pytest` run per group was too slow and aborted on collection errors. | Per-file isolated runs under `coverage run --branch` with the sysmon core, extras installed (`--all-extras`), coverage installed outside the env. Floors are per group on owned tests only. Details in `PORT_TESTS.md`. |
