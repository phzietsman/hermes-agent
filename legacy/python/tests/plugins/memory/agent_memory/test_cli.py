from __future__ import annotations

import argparse
from pathlib import Path

import pytest

from plugins.memory.agent_memory import cli
from plugins.memory.agent_memory.outbox import Outbox

from .fakes import FakeClient, write_config


@pytest.fixture
def home(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    monkeypatch.setenv("AGENT_MEMORY_API_KEY", "k")
    write_config(tmp_path)
    return tmp_path


def test_scopes_init_registers_user_and_agent_scopes_once_each(home: Path) -> None:
    c = FakeClient()
    assert cli.cmd_scopes_init(lambda _s: c) == 0
    assert [kw["path"] for n, kw in c.calls if n == "register_scope"] == ["user/default", "agent/hermes"]


def test_doctor_passes_when_every_probe_succeeds_and_fails_when_down(home: Path, capsys: pytest.CaptureFixture[str]) -> None:
    c = FakeClient()
    assert cli.cmd_doctor(lambda _s: c) == 0
    c.mode = "down"
    assert cli.cmd_doctor(lambda _s: c) == 1
    assert "✗ whoami" in capsys.readouterr().out


def test_replay_drains_the_outbox(home: Path) -> None:
    Outbox(home / "agent-memory-outbox.jsonl").append("remember", {"text": "t", "labels": [], "category": None})
    c = FakeClient()
    assert cli.cmd_replay(lambda _s: c) == 0
    assert c.names() == ["remember"] and Outbox(home / "agent-memory-outbox.jsonl").pending() == []


def test_unconfigured_commands_fail_with_the_reason(tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
                                                     capsys: pytest.CaptureFixture[str]) -> None:
    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    monkeypatch.delenv("AGENT_MEMORY_API_KEY", raising=False)
    assert cli.cmd_doctor(lambda _s: FakeClient()) == 1
    assert "AGENT_MEMORY_API_KEY" in capsys.readouterr().out


def test_register_cli_exposes_the_four_subcommands() -> None:
    parser = argparse.ArgumentParser()
    cli.register_cli(parser)
    for name in ("status", "doctor", "scopes-init", "replay"):
        assert parser.parse_args([name]).agent_memory_cmd == name
