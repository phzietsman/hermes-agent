# Hermes agent (Go)

A personal AI agent that works for me: one binary, a terminal chat, Telegram and Discord, scheduled jobs,
long-term memory and a learning loop that writes skills. It is a Go rewrite of the Python
[Hermes agent](https://github.com/NousResearch/hermes-agent) this repository forked from (see `FORK.md`).
The Python tree is kept, frozen, in `legacy/python/` as the behavioural reference.

Choices are deliberate lock-in: Anthropic models, SurrealDB Agent Memory ("SAM"), the Jev judge for evals.

## Build and run

```
go build -o bin/hermes ./cmd/hermes
bin/hermes chat                  # interactive; -q "question" for one answer; --resume <id>
bin/hermes gateway run           # Telegram and Discord, plus the cron ticker
bin/hermes cron list|add|edit|pause|resume|remove|run|tick|daemon|status
bin/hermes pairing list|approve|revoke
bin/hermes agent_memory status|doctor|scopes-init|replay|import
go build -o bin/hermes-eval ./cmd/hermes-eval    # memory evals: memory, latency
```

State lives in `~/.hermes` (or `$HERMES_HOME`, or `~/.hermes/profiles/<name>` with `--profile`):
`config.yaml` (behaviour), `.env` (secrets only), `state.db` (sessions), `cron/`, `pairing/`, `skills/`.

Minimum setup: put `ANTHROPIC_API_KEY=...` in `~/.hermes/.env`. For memory add `AGENT_MEMORY_API_KEY` and an
`agent-memory.json` with `endpoint` and `context`. For chat platforms add `TELEGRAM_BOT_TOKEN` or
`DISCORD_BOT_TOKEN` and set `gateway.platforms.<name>.enabled: true` plus `allowed_users` (or pair people with
`hermes pairing`).

## Layout

```
cmd/hermes, cmd/hermes-eval   the binaries
internal/app                  runtime wiring shared by the CLI, gateway and cron
internal/agent                turn loop, prompt assembly, compaction, prompt-cache planning
internal/provider/anthropic   Messages API client: streaming, OAuth, retries, error classification
internal/tools/...            file, shell, web, session search, skills, delegate, cron, memory tools
internal/approval             dangerous-command detection and the approval gate
internal/session              SQLite sessions with FTS5 search
internal/memory(/sam)         SAM client, outbox, breaker, recall and sync
internal/learning             review queue, scheduler and the background review fork
internal/cron                 jobs store, schedule parser, scheduler
internal/gateway(/telegram,/discord)   adapters, pairing, per-chat sessions, streaming
internal/evals                memory eval harness
skills/, optional-skills/     SKILL.md packages (unchanged data)
legacy/python/                the frozen Python implementation and its test suite
```

## Develop

```
scripts/check.sh      build, vet, staticcheck, go test -race -cover over cmd/ and internal/
```

`PORT.md` is the log of the port: tasks, the decision log (G1 to G35) and what is blocked on live credentials.
`PORT_TESTS.md` maps every Python test file to a Go counterpart or a reason it was dropped.
The TypeScript frontends (`ui-tui/`, `apps/`, `web/`, `website/`) are untouched and have no backend any more.
