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
    assert kw["category"] == "instructions" and "kind=instruction" in kw["labels"]
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
