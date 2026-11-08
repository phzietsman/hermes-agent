# Hermes agent (Go) — development guide

For AI coding assistants and developers. The Python guide this replaces is `legacy/python/AGENTS.md`; its invariants
survive here.

## Invariants (the lens for reviewing any change)

- **Per-conversation prompt caching is sacred.** The system prompt is built once per session and stored write-once
  (`session.SetSystemPrompt`); it changes only at compaction (`RewriteSystemPrompt`). Past messages are never mutated
  except by compaction. No mid-conversation toolset swap, memory reload or skill reload. Mid-turn injection goes only into
  a user message or a tool result, and only request-locally (recalled memory joins the current user message in the
  request and is never stored). Four `cache_control` breakpoints. A review fork reuses its parent's stored prompt and tools
  array byte for byte.
- **The core is a narrow waist.** A new model tool must be fundamental; otherwise it is a CLI command plus a skill, a
  service-gated tool (registered only when configured, like `agent_memory_*`), or an edge adapter.
- **Fail closed.** Approval silence is denial; unreadable stores are errors, never silently emptied; the memory outbox
  never drops a write it could not send; secrets are scrubbed from errors and logs.
- **Never hardcode `~/.hermes`.** Use `config.Home(ctx)` and pass the home explicitly. Behaviour belongs in `config.yaml`,
  `.env` is secrets only.
- **One process serves one profile.** Code that runs outside a turn (cron, gateway loops, review forks) gets its home and
  session passed in; nothing reads the launch profile from globals.

## Working rules

- Tests: behaviour contracts against fake servers and temp homes (`t.TempDir()`), never `~/.hermes`. Verify against
  the Python reference with a generator under `legacy/python/scripts/port/` and a golden file when a behaviour was
  already tuned there. `go test -race -cover` must stay at or above the floors in `PORT_TESTS.md`.
- Gate: `scripts/check.sh` (build, vet, staticcheck, race tests). Run it before every commit.
- Registration is explicit (`registerPlatforms`, `Registry.Register`); no import-time discovery.
- Errors shown to the model keep Python's wording where it was tuned (`textError` in `internal/cron/schedule.go`).
- Commits: conventional messages; one task, one commit. Decisions go in the `PORT.md` decision log.
- The Python tree is a reference, not a dependency: do not import or run it from Go.
