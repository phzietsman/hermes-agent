# Risks: SurrealDB Agent Memory (SAM) as the only memory layer

Status as of the `feat/surrealdb-agent-memory` branch. Each item: what can go wrong, what limits it today,
what is still open. Decision numbers refer to `DECISIONS.md`.

## 1. Data egress
Conversation turns leave the machine for the SAM endpoint. Only user/assistant prose is sent; tool rows and
system prompts are not (`transcript.direct_messages`), and tool outputs go only with `send_tool_outputs`.
Secrets are redacted before queueing and never reach the outbox or the core-profile cache (tested).
**Open:** redaction is pattern-based. Paths, commands and anything a user types in prose that does not look
like a key will leave the machine. Do not point this at an endpoint you do not trust with that content.

## 2. Hosted-only dependency
There is no local fallback and the builtin memory was deleted (D35). If SAM is down, the agent keeps running
with the last cached core profile and a durable outbox; recall returns nothing.
**Evidence:** scenario 7 (`evals/memory/resilience.py`): network cut mid-session, restart while still down,
recovery. Every told fact lands exactly once, the outbox drains, the cached core profile is served while down.
**Open:** a long outage grows the outbox without bound; there is no size cap or expiry.
**Rollback:** `pre-sam` marks the pre-change tree. Because Phase 3c deleted builtin memory, reverting to it
means reverting those commits, not flipping a flag.

## 3. Latency
Recall on the hosted Sandbox takes 2-25s (median ~6-7s) in every mode (D31), so the "prefetch p95 < 1.5s" gate
cannot be met against it. The provider waits at most `prefetch_wait_s` (2.5s) and otherwise serves the last
completed recall, so recall usually arrives one turn late.
**Open:** needs a faster endpoint or tier, or a different threshold.

## 4. Write reliability
The agent claims saves that do not always reach SAM. Live result (Haiku, Jev judge, 3 seeds x 8 facts, D25):
write-landing 83% on this branch vs 33% on `main`. The gate is 95%; **not met.**
Rejected writes (4xx) are dropped, not retried (D32); the first live run showed that treating them as outages
silently turned memory off (D33).

## 5. Prerelease SDK and moving contract
`surrealdb[memory]==3.0.0b10` and `surrealdb-memory==1.0.0b1` are pinned betas. The real service contract
differed from the plan (categories, labels, session ids; D32) and unit tests with a fake client could not see it.
**Mitigation:** `test_sdk_contract.py` and the `-m live` tests. Run the live tests on every provider or SDK bump.
**Open:** the live tests need credentials, so CI does not run them.

## 6. Preview access
The Sandbox is a preview instance. Availability, quotas, data retention and the API itself can change or end
without notice. Treat anything stored there as non-durable until a production tier is agreed.

## 7. Cost
Background consolidation and extraction run server-side and spend tokens on the vendor's side; recall adds
latency and prompt tokens per turn (capped by `top_k` and a character budget). The plan's monthly token gate
(scenario 8) has not been measured: it needs a live run. Vendor pricing can change.

## 8. Recall noise and cache safety
Recalled text is injected into context each turn and can be wrong, stale or irrelevant (supersession showed no
difference vs `main`, D25, n=12). The core profile is frozen per session so the prompt cache stays valid (D28);
facts remembered this session reach the model only through recall.

## 9. Test coverage gaps
Scenarios 4 (preference following), 5 (procedural reuse) and 8 (regression/token cost) have no live results
yet. A full-suite run after Phase 3b/3c and D37 was not done; targeted runs only, with the `main`-baseline
failures listed in D23/D26.

## 10. Credentials
Sandbox and judge keys were pasted into a chat session during development. Rotate them before the repository
or those logs are shared, and keep them out of `~/.hermes` and the repo.
