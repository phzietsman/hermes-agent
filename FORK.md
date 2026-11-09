# FORK.md — manifest and changelog

Personal fork of [NousResearch/hermes-agent](https://github.com/NousResearch/hermes-agent).
Owner: Paul Zietsman. Purpose: an agent that works for me.

## Upstream baseline

| | |
|---|---|
| Upstream | `NousResearch/hermes-agent` |
| Forked from | `25a71a744cb9ef06950a91638e6229b4f808d461` (2026-10-08, "catalog: use reviewed disclosure wording for antigravity-subscription-directsdk") |
| First fork commit | `3ff6842f99` (2026-10-09) |
| Upstream tags | none at the fork point; `pyproject.toml` version is `0.0.0` |
| Diff vs baseline | `git diff 25a71a744c HEAD` (about 2,950 files, mostly deletions) |

Everything before `25a71a744c` is upstream. Everything after it is mine.
The history has a few duplicated commit pairs from rebases (for example
`3ff6842f99`/`540e0353ec`, `413dc30337`/`06a7f63a30`), so use the tree diff,
not the commit count.

## Stance

- **Never merging back.** The fork is permanently divergent and will not go upstream (see D27 in `DECISIONS.md`; the repo is to be made private). Upstream's contribution rubric (no third-party products in core, narrow core waist, etc.) does **not** apply here. Don't spend effort keeping changes upstream-mergeable.
- **Lock-in is accepted.** I pick specific tools and models and optimise for them rather than for portability.
- **Upstream is a source to cherry-pick from, not to track.** Take upstream fixes by hand. Expect conflicts in `agent/`, `gateway/`, `plugins/memory/` and `hermes_cli/`.
- `AGENTS.md` (Go) is the working guide; upstream's Python guide is `legacy/python/AGENTS.md`. Prompt-cache safety is kept (see D28) and listed first among the Go invariants.
- **The implementation is Go since 2026-11-08** (branch `go-port`, see the changelog). The sections below describe the Python fork it was ported from; paths such as `agent/` and `plugins/memory/` now live under `legacy/python/`.

## What is different from Hermes

| Area | Upstream Hermes | This fork |
|---|---|---|
| Memory | Builtin `MEMORY.md`/`USER.md` + `memory` tool, pluggable providers (honcho, mem0, supermemory, openviking, byterover, holographic, retaindb, ...) | **SurrealDB Agent Memory (SAM) is the only memory layer.** In-tree provider `plugins/memory/agent_memory/` with outbox, circuit breaker and core-profile cache. `MemoryManager` holds one provider. |
| Learning loop | Turn-counter nudges (`_memory_nudge_interval`, `_iters_since_skill`) and `agent/background_review.py` | `agent/learning/`: durable review queue, scheduler (iterations, session end/reset/compress, idle, shutdown) and runner. Typed, with idempotent jobs and backoff. |
| Platforms | ~20 messaging platforms | Telegram, Discord, WhatsApp, Slack, Email, Teams, webhook, api_server, relay. |
| OS support | Linux, macOS, Windows | Linux (incl. WSL) and macOS only. |
| Typing | `ty` only | Strict mypy and pyright on new and touched modules (`.github/workflows/typecheck.yml`). |
| Evaluation | Ad hoc probes in `evals/` | `evals/memory/`: 8 scenarios, with Jev (TypeSafe) as pinned judge and a live arm driving a real agent. |
| Models and services | Provider-agnostic | Agent runs on Claude (evals: `claude-haiku-5-5`); judge is TypeSafe `jev-1.14.0`; memory is hosted SAM. |

Fork-only docs:

- `surrealdb.plan.md`: spec for the SAM migration.
- `AUDIT.md`: Phase 0 audit of the memory code paths.
- `DECISIONS.md`: decision log (D1 to D47) with status.
- `RISKS.md`: known risks and gate outcomes (data egress, recall latency, etc.).
- `wishlist.md`: ideas not yet built.
- `evals/memory/README.md`: how to run the memory evals.

### Known costs of the divergence

- Recall latency on the hosted SAM sandbox is 2–25 s. Prefetch waits at most 2.5 s, so recall usually arrives one turn late (D31, D47).
- Conversation turns leave the machine for SAM. Redaction is incomplete for paths and commands (D14, `RISKS.md`).
- Several tests fail on upstream too (D23, D26). They are not caused by fork changes.
- Token budget gate was never judged.

## Changelog

Newest first. Dates are commit dates. `!` marks a breaking change.

### 2026-11-08 — Go port: the agent is rewritten in Go (branch `go-port`, not merged)
- Added!: a Go module (`github.com/phzietsman/hermes-agent`, Go 1.26, pure-Go SQLite) that replaces the Python agent: `cmd/hermes` (`chat`, `gateway run`, `cron`, `pairing`, `agent_memory`) and `cmd/hermes-eval`. The design, decisions (G1 to G35) and what is blocked on live credentials are in `PORT.md`; the test ledger and coverage floors are in `PORT_TESTS.md`.
- Ported with Python goldens: Anthropic message and request conversion `b7f4d17eb8`, `9436af7b88`; streaming and the HTTP client `ec2e92faea`; OAuth and Keychain `21fa784269`; error classification `47ddb5aacd`; prompt cache planner `d475d3e8d2`; prompt assembly `b7a8cf36e7`, `8e39f42ad7`, `e02b1e6d25`; turn loop `5764aed16b`; compaction `51b2ab49c6`; tool registry `39e5d646be`; approval detection (1,486-command corpus) `afc374da09`; file, patch and terminal tools `3d24e58a96`; web, session and skill tools `7454c55029`; cron schedules and DST-aware next runs `ee9168a652`.
- Added: config, sessions with FTS5 `5e26523496`, `868e882e56`; skills `586c5c819d`; delegation `5a4c1e112a`; the CLI `3f4ba2cfa8`; SAM client with outbox and breaker `09e9a06246`; memory in the loop and `hermes agent_memory` `770cd92d56`; the learning loop with a cache-sharing review fork `791a5698eb`; the Go eval harness `455869f3b4`; cron `ee9168a652`; gateway core `5db2e6a4b5`; Telegram `64dbf05e7d`; Discord `282c23c8d7`; acceptance `c2260a3c58`.
- Changed!: all Python (agent, tools, gateway, cron, hermes_cli, plugins, pm, tests, evals, build and packaging files) moved to `legacy/python/` (task 8.2); it is a frozen behavioural reference, not a runtime. The TypeScript frontends (`ui-tui/`, `apps/`, `web/`, `website/`) are untouched and have no backend: they no longer work.
- Removed!: not carried over to Go in Stage 1: other model providers and credential pools, the other platforms, browser/computer-use/code-execution/image/video/TTS tools, MCP, kanban, MoA, the TUI, setup wizards, `hermes update`, background processes, multiplexed profiles.

### 2026-11-06 — SAM becomes the sole memory layer
- Added: SAM provider merged to `main` (#3), phases 1–6 of `surrealdb.plan.md`. Gates: recall and supersession no worse than loop-fix only, write landing 100%, scenario 7 zero writes lost, scenarios 4 and 5 met; 2.5 s prefetch ceiling accepted (D47). `932f598d2f`, `749e044768`

### 2026-11-05 — Evals and fixes
- Added: eval scenarios 4 (preference following), 5 (procedural reuse), 7 (resilience), 8 (latency); SAM live arm; 6- and 9-seed result sets (D40–D46). `e63eb23867`, `927102e21c`, `58e0fdae6f`, `bba4a7ef42`, `8be5009b3b`
- Fixed: turn sync was skipped on every normal turn. `8be5009b3b`
- Fixed: delegated children were still denied the memory toolset. `ce4e7f5417`
- Removed: residual third-party memory-provider code (D37). `9c9e19cfd0`
- Changed: `.secrets/` git-ignored for local live-eval credentials. `6cf1f8f80f`

### 2026-11-04 — Memory migration, learning loop, eval harness
- Added: SAM provider (typed, outbox-backed), then aligned with the real service contract (D32, D33). `358fe3653f`, `ba599af112`
- Added: importer for legacy `MEMORY.md`/`USER.md` and session backfill (`hermes agent_memory import`). `b7e464fc1d`
- Added: `agent/learning/` review queue, scheduler, runner; wired into the agent loop; emits `PRE_COMPRESS` and defers reviews for the local runtime. `0305d06a92`, `73342230b9`, `cfa9926c07`, `ddc8cb83db`
- Added: strict typing infra and eval harness skeleton; Jev judge with calibration set; live memory arm; aside-style scenarios with write attribution. `13235ea180`, `f326795785`, `18ee7fc294`, `929d05ea62`
- Added: Phase 0 audit (`AUDIT.md`). `d92c137022`
- Removed!: builtin memory runtime (`MEMORY.md`/`USER.md` injection, `memory` tool, `/memory`, memory write-approval, `on_memory_write`, memory-related config keys). `45da57d2ff`
- Removed!: byterover, holographic and retaindb providers, plus catalog entries and docs for honcho, mem0, supermemory and openviking. `d092f13f1e`
- Changed: `MemoryManager` holds a single provider. `2380d494dc`
- Removed: dead `should_review_memory` plumbing (D18). `cfa9926c07`

### 2026-10-16 — Windows removed
- Removed!: Windows support everywhere, including the Windows Electron build, installers, MSIX/Store packaging, Windows CI and docs. Use WSL. `413dc30337`, `06a7f63a30`

### 2026-10-14 — Platforms trimmed
- Removed!: Signal, SMS, DingTalk, Feishu, WeCom, Weixin, BlueBubbles, QQBot, Yuanbao, Matrix, Mattermost, LINE, Google Chat, IRC, ntfy, Photon, Raft, Simplex, Buzz, A2A platform plugin, Home Assistant remnants, with their tools, tests, docs, skills and dependencies. Extras `sms`, `wecom`, `dingtalk`, `feishu`, `matrix`, `google-chat` dropped; `whatsapp` extra added. `540e0353ec` (first applied as `3ff6842f99` on 2026-10-09)

### 2026-10-08 — Fork created
- Forked from upstream `25a71a744c`.

## Maintaining this file

- Add an entry under a new dated heading for each user-visible add, remove or behaviour change, with the commit hash.
- Mark removals and breaking changes with `!`.
- If I take a fix from upstream, record it under `Upstream cherry-picks` (add the section when the first one lands) with the upstream hash.
- Keep `DECISIONS.md` for the why, and this file for the what.
