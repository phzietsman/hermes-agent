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
Measured through the real provider on the hosted Sandbox (`evals/memory/regression.json`, D46): raw recall p50
14.3s / p95 16.2s (an earlier run measured a 6-7s median; it varies, D31). The provider caps the blocking wait at
`prefetch_wait_s` (2.5s): cold prefetch p50/p95 2.51s, warm prefetch p50 ~0 / p95 2.51s. The plan's "prefetch p95 <
1.5s" gate is not met and cannot be on this endpoint. In scenario 5 this shows as ~45% more wall time per task
than loop-fix only (D45). Recall usually arrives one turn late.
**Open:** a faster tier/endpoint, or accept a lower `prefetch_wait_s`.

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
latency and prompt tokens per turn (capped by `top_k` and a character budget). Client-side tokens per task were
measured (scenario 5: ~50k per reuse run with SAM vs ~54k without, D45/D46). SAM's own server-side tokens are not
visible to the client and the plan states no numeric monthly budget, so that gate cannot be judged; get usage from
the vendor dashboard. Vendor pricing can change.

## 8. Recall noise and cache safety
Recalled text is injected into context each turn and can be wrong, stale or irrelevant (supersession showed no
difference vs `main`, D25, n=12). The core profile is frozen per session so the prompt cache stays valid (D28);
facts remembered this session reach the model only through recall.

## 9. Test coverage gaps
Scenarios 1-8 all have results (D25, D41-D46). The full suite was run after Phase 3b/3c and D37 (D46): the only
failures left are ones that also fail on `main`. Sample sizes are small (tens of questions per arm), so gaps of a
few points are noise. Not measured: SAM's server-side token spend, and behaviour at a large store size.

## 10. Credentials
Sandbox and judge keys were pasted into a chat session during development. Rotate them before the repository
or those logs are shared, and keep them out of `~/.hermes` and the repo.
