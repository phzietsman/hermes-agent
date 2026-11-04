---
sidebar_position: 4
title: "Memory Provider"
description: "SurrealDB Agent Memory: the persistent memory provider"
---

# Memory Provider

Hermes keeps long-term memory in [SurrealDB Agent Memory](https://surrealdb.com/agent-memory) (the `agent_memory` provider). It is the only memory layer: what the agent learns about you, your standing instructions, and lessons from past work all live there and are recalled on later turns and in later sessions.

Skills stay as files on disk. Agent Memory holds a pointer to each skill so it can be found by recall.

## Setup

```bash
hermes memory setup          # choose agent_memory; stores the key in .env, the rest in agent-memory.json
hermes agent_memory scopes-init   # register the user/agent scopes (Agent Memory requires this once)
hermes agent_memory doctor        # probe whoami, profile and recall
```

Install the SDK with the `agent-memory` extra. It is a prerelease, so outside the managed environment pass `--prerelease=allow`.

| Setting | Where | Default |
|---|---|---|
| `AGENT_MEMORY_API_KEY` | `.env` (secret) | required |
| `endpoint`, `context` | `agent-memory.json` | required |
| `default_scope` | `agent-memory.json` | `user/default` (`user/<id>` when the platform supplies a user id) |
| `recall_mode` | `agent-memory.json` | `hybrid` (`context`: synthesised answer; `tools`: nothing injected) |
| `top_k` | `agent-memory.json` | `5` |
| `core_profile_max_chars`, `max_prefetch_chars` | `agent-memory.json` | `1500`, `4000` |
| `send_tool_outputs` | `agent-memory.json` | `false` |

## What the agent sees

- **Standing instructions** (`## About the user / standing instructions`) are loaded once at session start into the system prompt. A change made mid-session reaches the model through recall and is folded into the profile at the next session, so the cached prompt prefix is never rebuilt.
- **Recalled memory** (`## Recalled memory`) is added to each turn from hybrid recall. Skill pointers show up as `Relevant skills: ... (load with skill_view)`.
- **Tools:** `agent_memory_recall`, `agent_memory_remember` (`kind`: fact, preference, instruction, lesson), `agent_memory_forget` (soft by default), `agent_memory_reflect`.

## Learning loop

Completed turns are written back to Agent Memory. At session end, `/new`, compression and shutdown, and after enough tool iterations or idle time, a background review looks for durable facts and lessons the agent did not save itself. See `learning:` in the configuration reference.

## When Agent Memory is unavailable

Writes never block a turn and are not lost. A failed write goes to `agent-memory-outbox.jsonl` and replays at the next session start, every few turns, or with `hermes agent_memory replay`. After repeated failures (or immediately on an auth error) the provider stops calling the service for the rest of the session, serves the last good core profile from `core_profile_cache.json`, and logs one warning.

## Data leaving your machine

User and assistant text from each turn is sent to your Agent Memory endpoint. Tool outputs are not sent unless `send_tool_outputs` is on. Redaction removes known secret formats, but file paths and command text in conversation can still be sent.

## Commands

`hermes agent_memory status | doctor | scopes-init | replay`
