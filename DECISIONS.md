# Open decisions: SurrealDB Agent Memory migration

Status key: OPEN (needs owner input) / DEFAULTED (proceeding on the stated default; reversible).

| # | Decision | Status | Default taken / options | Needed by |
|---|---|---|---|---|
| D1 | Live eval arm: how to drive Hermes headlessly, and which judge model to pin. `evals/memory/baseline.json` is `harness_only` until this exists. | OPEN | Build after Phase 2 scheduler lands. Needs credentials and a judge model choice | Phase 2 acceptance (`loopfix.json`) |
| D2 | Review queue storage: new `review_jobs` table in `state.db` created by the queue itself (`CREATE TABLE IF NOT EXISTS`) vs a schema migration in `hermes_state_schema.py`. | DEFAULTED | Self-created table, no schema version bump. Switch to a migration if maintainers want it versioned | Phase 2 wire-in |
| D3 | Replace vs wrap the existing nudge counters (`_memory_nudge_interval`, `_iters_since_skill`) and `agent/background_review.py`. | DEFAULTED | New `agent/learning/` package is built and tested standalone first; wire-in replaces counters, config keys kept as aliases for one release | Phase 2 wire-in |
| D4 | Overlap with `agent/learning_mutations.py` / `learning_graph.py`. | RESOLVED | No overlap: they are the journey UI over MEMORY.md/USER.md and skills. They go away or change in Phase 3, not Phase 2 | Phase 3 |
| D5 | Single-provider collapse: remove `MemoryManager` multi-provider selection entirely, or leave the seam for a future second provider. | OPEN | Plan says collapse; confirm | Phase 3 |
| D6 | Land Phases 3 and 4 in one PR (plan) vs behind a flag. | DEFAULTED | One PR, so main always has memory | Phase 3 |
| D7 | `pre-sam` tag: create it on this branch's base commit. | OPEN | Tag `main` HEAD before Phase 3 merges. I will not push tags without approval | Phase 3 |
| D8 | CoreProfile source: SDK `profile()` (static/preferences/instructions) instead of category-filtered `recall` (SDK has no category filter). | DEFAULTED | Use `profile()` | Phase 4 |
| D9 | Retrieval feedback/usefulness: SDK exposes none (`traces` is read-only). | DEFAULTED | Skip, record gap in RISKS.md | Phase 4 |
| D10 | Skills in SAM: labelled memories (plan) vs the `documents` API. | DEFAULTED | Labelled memories; revisit if recall of skill pointers is poor | Phase 4 |
| D11 | SDK value sets for `remember_many(extract=...)` and `remember(infer=...)` are undocumented. | OPEN | Verify against a Sandbox context | Phase 4 |
| D12 | SAM Sandbox access: endpoint, context name, API key, preview-access status. | OPEN | Needed for `doctor`, live tests, smoke test | Phase 4 acceptance |
| D13 | Review of the official `surrealdb/agent-memory-hermes` plugin (not fetched). | OPEN | Read before Phase 4 for naming/config parity | Phase 4 |
| D14 | Data egress policy: turns leave the machine; redaction is not complete for paths/commands. Is that acceptable, and is `send_tool_outputs=False` the right default? | OPEN | Document in RISKS.md | Phase 4 |
| D15 | Ship-gate thresholds (>=15pt gain, >=95% write landing, p95 < 1.5s) and the "monthly token plan" figure. | OPEN | Token budget number not provided | Phase 6 |
| D16 | Local ~30B model for the eval matrix: which one, where it runs. | OPEN | | Phase 6 |
| D17 | Typecheck scope: strict mypy/pyright only on new/touched modules (current), vs whole repo. | DEFAULTED | New modules only; repo has only `ty` today | ongoing |
| D18 | Dead `should_review_memory` plumbing through turn_context / conversation_loop / finalize_turn / codex_runtime. | RESOLVED | Removed, with the kwarg dropped from 11 test files | done |
| D19 | Idle-deferral to the managed local llama-server (`_review_should_defer` + `review_idle_queue`) is bypassed for loop-driven reviews; the runner calls `_spawn_background_review_now` directly. Local-model users may see a review start mid-session. | OPEN | Either route the loop through the deferral gate or accept (reviews already start only after delivery) | Phase 2 acceptance |
| D20 | `ReviewTrigger.PRE_COMPRESS` is defined but unused: `conversation_compression.py` is at its file-size cap, so compression is covered by `commit_memory_session` (SESSION_RESET). | DEFAULTED | Split a function out of `conversation_compression.py`, then emit PRE_COMPRESS | Phase 2 PR |
| D21 | Config: `learning:` block registered in `config_defaults.py`. `learning.iteration_interval` defaults to None so the legacy keys still work as aliases (`skills.creation_nudge_interval`, then `memory.nudge_interval`); `memory.nudge_interval` now counts tool iterations, not turns (behaviour change, documented in memory.md). | RESOLVED | done | done |
| D22 | The review fork still gets its memory writes through the legacy `_memory_store` / `_memory_manager` it inherits; the planned "writes carry origin=background_review through the provider" test is not written (no provider write path to test until Phase 4). | OPEN | Write when SAM provider exists | Phase 4 |
| D23 | Pre-existing failures on `main` (not caused by this branch): `tests/agent/lsp/test_workspace.py`, `tests/agent/test_compaction_prompt_rebuild.py`, `tests/hermes_state/test_cross_vm_fs_wal_refusal.py`, `tests/hermes_state/test_guest_durability_barriers.py`. | OPEN | Verified failing with this branch's tracked changes stashed | n/a |
