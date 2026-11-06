from __future__ import annotations

import argparse
from pathlib import Path
from typing import Any

import pytest
from pydantic import SecretStr

from plugins.memory.agent_memory import cli
from plugins.memory.agent_memory.config import AgentMemorySettings
from plugins.memory.agent_memory.importer import (
    ENTRY_DELIMITER, import_memory_files, import_sessions, read_entries,
)

from .fakes import FakeClient, write_config

SETTINGS = AgentMemorySettings(endpoint="https://x.example", context="c", api_key=SecretStr("k"))  # type: ignore[arg-type]


@pytest.fixture
def home(tmp_path: Path) -> Path:
    (tmp_path / "memories").mkdir()
    (tmp_path / "memories" / "MEMORY.md").write_text(
        ENTRY_DELIMITER.join(["Staging cluster is zephyr-1.", "Deploys go out on Fridays.  \n"]), encoding="utf-8")
    (tmp_path / "memories" / "USER.md").write_text("Prefers terse answers.\n§ not a delimiter\n", encoding="utf-8")
    return tmp_path


def test_entries_split_on_the_full_delimiter_and_a_bare_section_sign_survives(home: Path) -> None:
    entries = read_entries(home)
    assert [(e.source, e.text) for e in entries] == [
        ("memory", "Staging cluster is zephyr-1."), ("memory", "Deploys go out on Fridays."),
        ("user", "Prefers terse answers.\n§ not a delimiter"),
    ]


def test_user_entries_become_identity_and_memory_entries_knowledge(home: Path) -> None:
    c = FakeClient()
    report = import_memory_files(c, home, SETTINGS, sleep=lambda _s: None)
    assert (report.written, report.skipped, report.failed) == (3, 0, 0)
    by_text = {kw["text"]: kw for n, kw in c.calls if n == "remember"}
    assert by_text["Prefers terse answers.\n§ not a delimiter"]["category"] == "identity"
    assert by_text["Staging cluster is zephyr-1."]["category"] == "knowledge"
    for kw in by_text.values():
        assert "origin=import" in kw["labels"] and any(label.startswith("digest=") for label in kw["labels"])
        assert kw["scopes"] == "user/default" and kw["session_id"] is None


def test_dry_run_prints_the_plan_and_writes_nothing(home: Path) -> None:
    c, lines = FakeClient(), []
    report = import_memory_files(c, home, SETTINGS, dry_run=True, out=lines.append)
    assert c.calls == [] and report.planned == 3 and report.written == 0
    assert len(lines) == 3 and "[user -> identity]" in lines[2]
    assert not (home / "agent-memory-import.json").exists()  # a dry run leaves no ledger behind


def test_importing_twice_writes_no_duplicates(home: Path) -> None:
    c = FakeClient()
    import_memory_files(c, home, SETTINGS, sleep=lambda _s: None)
    second = import_memory_files(c, home, SETTINGS, sleep=lambda _s: None)
    assert (second.written, second.skipped) == (0, 3)
    assert c.names().count("remember") == 3


def test_a_new_entry_added_later_is_the_only_one_imported(home: Path) -> None:
    c = FakeClient()
    import_memory_files(c, home, SETTINGS, sleep=lambda _s: None)
    path = home / "memories" / "MEMORY.md"
    path.write_text(path.read_text(encoding="utf-8") + ENTRY_DELIMITER + "A brand new fact.", encoding="utf-8")
    again = import_memory_files(c, home, SETTINGS, sleep=lambda _s: None)
    assert (again.written, again.skipped) == (1, 3)


def test_a_failed_entry_is_reported_retried_next_time_and_does_not_stop_the_rest(home: Path) -> None:
    c = FakeClient()
    c.reject = {"Deploys go out on Fridays."}
    lines: list[str] = []
    first = import_memory_files(c, home, SETTINGS, sleep=lambda _s: None, out=lines.append)
    assert (first.written, first.failed) == (2, 1) and any("✗" in line for line in lines)
    c.reject = set()
    second = import_memory_files(c, home, SETTINGS, sleep=lambda _s: None)
    assert (second.written, second.skipped, second.failed) == (1, 2, 0)  # resumes: only the failed one


def test_rate_limit_pauses_between_writes(home: Path) -> None:
    pauses: list[float] = []
    import_memory_files(FakeClient(), home, SETTINGS, rate_s=1.5, sleep=pauses.append)
    assert pauses == [1.5, 1.5, 1.5]


def test_missing_memory_files_import_nothing(tmp_path: Path) -> None:
    assert import_memory_files(FakeClient(), tmp_path, SETTINGS).planned == 0


class _FakeDb:
    def __init__(self, sessions: dict[str, list[dict[str, Any]]]) -> None:
        self._sessions = sessions

    def list_sessions_rich(self, *, limit: int, order_by_last_active: bool) -> list[dict[str, Any]]:
        return [{"id": sid} for sid in list(self._sessions)[:limit]]

    def get_messages_as_conversation(self, session_id: str) -> list[dict[str, Any]]:
        return self._sessions[session_id]


def _db() -> _FakeDb:
    return _FakeDb({
        "s1": [{"role": "user", "content": "hello"}, {"role": "tool", "content": "SECRET"},
               {"role": "assistant", "content": "hi"}],
        "s2": [{"role": "user", "content": "other"}],
        "empty": [{"role": "system", "content": "prompt only"}],
    })


def test_session_backfill_sends_prose_only_limits_n_and_is_idempotent(home: Path) -> None:
    c = FakeClient()
    first = import_sessions(c, home, SETTINGS, limit=2, db=_db(), sleep=lambda _s: None)
    assert (first.written, first.skipped) == (2, 0)
    sent = [kw for n, kw in c.calls if n == "remember_many"]
    assert [i["content"] for i in sent[0]["items"]] == ["hello", "hi"] and "SECRET" not in str(sent)
    assert "origin=import-session" in sent[0]["labels"] and "session=s1" in sent[0]["labels"]
    second = import_sessions(c, home, SETTINGS, limit=2, db=_db(), sleep=lambda _s: None)
    assert (second.written, second.skipped) == (0, 2) and c.names().count("remember_many") == 2


def test_import_command_end_to_end(home: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]) -> None:
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("AGENT_MEMORY_API_KEY", "k")
    write_config(home)
    c = FakeClient()
    args = argparse.Namespace(dry_run=True, sessions=0, rate=0.0)
    assert cli.cmd_import(lambda _s: c, args) == 0
    assert c.calls == [] and "would write 3" in capsys.readouterr().out
    args.dry_run = False
    assert cli.cmd_import(lambda _s: c, args) == 0 and "wrote 3" in capsys.readouterr().out
    assert cli.cmd_import(lambda _s: c, args) == 0 and "already imported 3" in capsys.readouterr().out


def test_import_subcommand_parses_its_flags() -> None:
    parser = argparse.ArgumentParser()
    cli.register_cli(parser)
    ns = parser.parse_args(["import", "--dry-run", "--sessions", "5", "--rate", "0.5"])
    assert (ns.agent_memory_cmd, ns.dry_run, ns.sessions, ns.rate) == ("import", True, 5, 0.5)
