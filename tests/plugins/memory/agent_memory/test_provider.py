from __future__ import annotations

import json
from pathlib import Path

import pytest

from plugins.memory.agent_memory.client import Hit, Profile
from plugins.memory.agent_memory.provider import AgentMemoryProvider

from .fakes import FakeClient, make, write_config


def test_is_available_needs_key_endpoint_and_context_but_no_network(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("AGENT_MEMORY_API_KEY", raising=False)
    p = AgentMemoryProvider(client_factory=lambda _s: FakeClient())
    p.initialize("s", hermes_home=str(tmp_path))
    assert not p.is_available() and "AGENT_MEMORY_API_KEY" in p.unavailable_reason()
    monkeypatch.setenv("AGENT_MEMORY_API_KEY", "k")
    assert not p.is_available()  # still no endpoint/context
    write_config(tmp_path)
    assert p.is_available()


def test_sync_turn_writes_only_user_and_assistant_prose(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    c = FakeClient()
    p = make(tmp_path, c, monkeypatch)
    p.sync_turn("hi", "hello", messages=[{"role": "tool", "content": "SECRET-TOOL-OUTPUT"}])
    p.shutdown()
    (name, kw), = [x for x in c.calls if x[0] == "remember_many"]
    assert kw["items"] == [{"role": "user", "content": "hi"}, {"role": "assistant", "content": "hello"}]
    assert kw["scopes"] == "user/default" and "SECRET-TOOL-OUTPUT" not in json.dumps(kw)


def test_tool_outputs_only_sent_when_opted_in(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    c = FakeClient()
    p = make(tmp_path, c, monkeypatch, send_tool_outputs=True)
    p.sync_turn("hi", "hello", messages=[{"role": "tool", "content": "out"}])
    p.shutdown()
    items = next(kw["items"] for n, kw in c.calls if n == "remember_many")
    assert {"role": "tool", "content": "out"} in items


def test_prefetch_formats_hits_caps_chars_and_points_at_skills(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    c = FakeClient()
    c.hits = (Hit("1", "Likes terse answers", 0.91), Hit("2", "Skill 'deploy-api': ships the api. Use when: deploying", 0.8))
    p = make(tmp_path, c, monkeypatch, max_prefetch_chars=500)
    out = p.prefetch("how do I deploy?")
    assert out.startswith("## Recalled memory") and "- Likes terse answers (0.91)" in out
    assert "Relevant skills: deploy-api (load with skill_view)" in out
    c.hits = tuple(Hit(str(i), "x" * 200, 0.5) for i in range(20))
    assert len(p.prefetch("another query")) <= 500
    p.shutdown()


def test_tools_mode_injects_nothing(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    c = FakeClient()
    c.hits = (Hit("1", "fact", 0.9),)
    p = make(tmp_path, c, monkeypatch, recall_mode="tools")
    assert p.prefetch("q") == "" and "recall" not in c.names()
    p.shutdown()


def test_prefetch_failure_returns_empty_and_auth_failure_opens_breaker_immediately(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    c = FakeClient()
    p = make(tmp_path, c, monkeypatch)
    c.mode = "auth"
    assert p.prefetch("q") == ""
    c.mode = "ok"
    c.hits = (Hit("1", "fact", 0.9),)
    assert p.prefetch("q2") == ""  # breaker is open for the session even though the backend recovered
    p.shutdown()


def test_breaker_degrades_to_cached_core_profile_and_outbox(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    c = FakeClient()
    c.profile_value = Profile(instructions=("Always answer tersely",), preferences=("Likes vim",))
    first = make(tmp_path, c, monkeypatch)
    assert "Always answer tersely" in first.system_prompt_block()
    first.shutdown()
    # new session while Agent Memory is down: last good core profile is served, writes queue
    down = FakeClient()
    down.mode = "down"
    second = make(tmp_path, down, monkeypatch, session="s2")
    assert "Always answer tersely" in second.system_prompt_block()
    second.sync_turn("a", "b")
    second.shutdown()
    assert (tmp_path / "agent-memory-outbox.jsonl").exists() and down.calls == []


def test_outbox_replays_after_recovery_and_is_idempotent(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    down = FakeClient()
    down.mode = "down"
    p = make(tmp_path, down, monkeypatch)
    p.sync_turn("a", "b")
    p.sync_turn("a", "b")  # identical write queued twice -> one outbox entry
    p.shutdown()
    lines = (tmp_path / "agent-memory-outbox.jsonl").read_text().splitlines()
    assert len(lines) == 1
    up = FakeClient()
    q = make(tmp_path, up, monkeypatch, session="s2")  # initialize() replays
    q.shutdown()
    assert up.names().count("remember_many") == 1
    assert not (tmp_path / "agent-memory-outbox.jsonl").exists()
    r = make(tmp_path, up, monkeypatch, session="s3")
    r.shutdown()
    assert up.names().count("remember_many") == 1  # nothing left to replay


def test_secrets_never_appear_in_payloads_outbox_or_cache(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    down = FakeClient()
    down.mode = "down"
    p = make(tmp_path, down, monkeypatch)
    p.sync_turn("hello", "world")
    p.on_session_end([{"role": "user", "content": "hello"}])
    p.shutdown()
    blob = "".join(f.read_text() for f in tmp_path.iterdir() if f.is_file())
    assert "sk-test-secret" not in blob


@pytest.mark.parametrize("context", ["cron", "subagent", "flush"])
def test_non_primary_contexts_never_write(tmp_path: Path, monkeypatch: pytest.MonkeyPatch, context: str) -> None:
    c = FakeClient()
    p = make(tmp_path, c, monkeypatch, agent_context=context)
    p.sync_turn("a", "b")
    p.on_session_end([{"role": "user", "content": "a"}])
    p.handle_tool_call("agent_memory_remember", {"text": "x"})
    p.shutdown()
    assert not {"remember", "remember_many", "forget", "consolidate"} & set(c.names())


def test_session_end_remembers_transcript_once_then_consolidates(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    c = FakeClient()
    p = make(tmp_path, c, monkeypatch)
    msgs = [{"role": "user", "content": "q"}, {"role": "assistant", "content": "a"}, {"role": "tool", "content": "t"}]
    p.on_pre_compress(msgs)
    p.on_session_end(msgs)  # same transcript: digest-identical write is not duplicated
    p.shutdown()
    names = c.names()
    assert names.count("remember_many") == 1 and names[-1] == "consolidate"
    kw = next(kw for n, kw in c.calls if n == "remember_many")
    assert [i["role"] for i in kw["items"]] == ["user", "assistant"]
    assert any(label.startswith("digest=") for label in kw["labels"])


def test_remember_tool_labels_kind_and_origin_and_routes_instructions(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    c = FakeClient()
    p = make(tmp_path, c, monkeypatch)
    out = json.loads(p.handle_tool_call("agent_memory_remember", {"text": "never use tabs", "kind": "instruction"}))
    p.shutdown()
    assert out["success"]
    kw = next(kw for n, kw in c.calls if n == "remember")
    assert kw["category"] == "identity" and "kind=instruction" in kw["labels"]  # SAM: identity|knowledge|context
    assert any(label.startswith("origin=") for label in kw["labels"]) and kw["scopes"] == "user/default"


def test_recall_forget_reflect_tools_and_unknown_tool(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    c = FakeClient()
    c.hits = (Hit("1", "fact", 0.9),)
    p = make(tmp_path, c, monkeypatch)
    assert json.loads(p.handle_tool_call("agent_memory_recall", {"query": "q"}))["result"]["hits"][0]["text"] == "fact"
    assert json.loads(p.handle_tool_call("agent_memory_reflect", {"query": "q", "persist": True}))["success"]
    p.handle_tool_call("agent_memory_forget", {"query": "old"})
    assert "error" in json.loads(p.handle_tool_call("nope", {}))
    p.shutdown()
    assert ("forget", {"query": "old", "purge": False}) in c.calls
    assert [s["name"] for s in p.get_tool_schemas()] == [
        "agent_memory_recall", "agent_memory_remember", "agent_memory_forget", "agent_memory_reflect"]


def test_user_id_becomes_the_default_scope(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    c = FakeClient()
    p = make(tmp_path, c, monkeypatch, user_id="alice")
    p.sync_turn("a", "b")
    p.shutdown()
    assert next(kw["scopes"] for n, kw in c.calls if n == "remember_many") == "user/alice"


def test_prefetch_never_blocks_past_its_wait_and_serves_the_last_completed_recall(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    import time

    c = FakeClient()
    c.hits = (Hit("1", "first answer", 0.9),)
    p = make(tmp_path, c, monkeypatch, prefetch_wait_s=0.3)
    assert "first answer" in p.prefetch("turn one")  # fast recall finishes inside the wait
    c.hits = (Hit("2", "second answer", 0.9),)
    c.recall_delay = 1.0  # service gets slow
    started = time.monotonic()
    out = p.prefetch("turn two")
    assert time.monotonic() - started < 0.8  # did not wait for the slow recall
    assert "first answer" in out  # last completed recall, not nothing
    time.sleep(1.2)  # the background recall lands and serves the next prefetch of that query
    assert "second answer" in p.prefetch("turn two")
    p.shutdown()


def test_queue_prefetch_warms_the_next_turn(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    import time

    c = FakeClient()
    c.hits = (Hit("1", "warm fact", 0.9),)
    p = make(tmp_path, c, monkeypatch, prefetch_wait_s=0.0)
    p.queue_prefetch("what next")
    time.sleep(0.3)
    assert "warm fact" in p.prefetch("something else entirely")  # zero wait: only the warmed result is available
    p.shutdown()


def test_slow_reads_do_not_open_the_breaker_but_failures_do(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    c = FakeClient()
    p = make(tmp_path, c, monkeypatch, breaker_threshold=3, prefetch_wait_s=1.0)
    c.mode = "timeout"
    for i in range(6):
        p.prefetch(f"query {i}")
    assert "error" in json.loads(p.handle_tool_call("agent_memory_recall", {"query": "q"}))
    c.mode = "ok"
    c.hits = (Hit("1", "still works", 0.9),)
    assert "still works" in p.prefetch("after the slowness")  # slow is not down
    c.mode = "down"
    for i in range(3):
        p.prefetch(f"down {i}")
    c.mode = "ok"
    assert p.prefetch("after the outage") != "still works"  # real failures opened it for the session
    p.shutdown()


def test_writes_never_pass_hermes_session_id_to_the_service_and_label_the_session_instead(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    c = FakeClient()
    p = make(tmp_path, c, monkeypatch, session="20261104_abc def")
    p.sync_turn("a", "b")
    p.handle_tool_call("agent_memory_remember", {"text": "x"})
    p.shutdown()
    writes = [kw for n, kw in c.calls if n in ("remember", "remember_many")]
    assert writes and all(kw["session_id"] is None for kw in writes)  # the service 403s ids it did not issue
    assert all("session=20261104_abc_def" in kw["labels"] for kw in writes)


def test_a_rejected_write_is_dropped_without_opening_the_breaker_or_blocking_the_queue(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    c = FakeClient()
    c.reject = {"poison"}
    p = make(tmp_path, c, monkeypatch)
    p.handle_tool_call("agent_memory_remember", {"text": "poison"})
    p.handle_tool_call("agent_memory_remember", {"text": "good one"})
    p.handle_tool_call("agent_memory_remember", {"text": "good two"})
    p.shutdown()
    assert [kw["text"] for n, kw in c.calls if n == "remember"] == ["good one", "good two"]
    assert not (tmp_path / "agent-memory-outbox.jsonl").exists()  # nothing stuck behind the poison entry
    c2 = FakeClient()
    c2.hits = (Hit("1", "fine", 0.9),)
    q = make(tmp_path, c2, monkeypatch, session="s2")
    assert "fine" in q.prefetch("q")  # session two is not degraded
    q.shutdown()


@pytest.mark.parametrize("origin,writes", [("assistant_tool", True), ("background_review", False)])
def test_turn_sync_runs_for_a_normal_turn_origin_and_not_for_a_review_fork(
        tmp_path: Path, monkeypatch: pytest.MonkeyPatch, origin: str, writes: bool) -> None:
    """A real turn binds origin "assistant_tool" (agent/turn_context.py), never "foreground"."""
    from tools.skill_provenance import reset_current_write_origin, set_current_write_origin

    c = FakeClient()
    p = make(tmp_path, c, monkeypatch)
    token = set_current_write_origin(origin)
    try:
        p.sync_turn("hello there", "hi")
    finally:
        reset_current_write_origin(token)
    p.shutdown()
    assert ("remember_many" in c.names()) is writes
