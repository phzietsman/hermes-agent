import json
import sqlite3
import tempfile
from datetime import datetime, timedelta, timezone
from pathlib import Path

import pytest

from agent.verification_evidence import (
    classify_verification_command,
    mark_workspace_edited,
    record_terminal_result,
    record_verify_run,
    verification_status,
)

@pytest.fixture(autouse=True)
def _ledger_on(monkeypatch):
    """The ledger is inert unless verify-on-stop is enabled; these tests exercise the ledger."""
    monkeypatch.setenv("HERMES_VERIFY_ON_STOP", "1")



def _node_project(root: Path) -> None:
    (root / "package.json").write_text(
        json.dumps({"scripts": {"test": "vitest", "lint": "eslint .", "dev": "vite"}})
    )
    (root / "pnpm-lock.yaml").write_text("")
    scripts = root / "scripts"
    scripts.mkdir()
    (scripts / "run_tests.sh").write_text("#!/bin/sh\n")


def _python_project(root: Path) -> None:
    (root / "pyproject.toml").write_text("[tool.pytest.ini_options]\n")








def test_lint_and_typecheck_are_not_reported_as_full_tests(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / ".hermes"))
    _node_project(tmp_path)

    lint = classify_verification_command(
        "pnpm run lint",
        cwd=tmp_path,
        session_id="s1",
        exit_code=0,
    )
    test = classify_verification_command(
        "pnpm run test -- tests/button.test.tsx",
        cwd=tmp_path,
        session_id="s1",
        exit_code=0,
    )

    assert lint is not None
    assert lint.kind == "lint"
    assert lint.scope == "full"
    assert test is not None
    assert test.kind == "test"
    assert test.scope == "targeted"




def test_shell_wrappers_match_but_echo_does_not(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / ".hermes"))
    _node_project(tmp_path)

    wrapped = classify_verification_command(
        "env CI=1 bash scripts/run_tests.sh tests/test_widget.py",
        cwd=tmp_path,
        session_id="s1",
        exit_code=0,
    )
    echoed = classify_verification_command(
        "echo scripts/run_tests.sh tests/test_widget.py",
        cwd=tmp_path,
        session_id="s1",
        exit_code=0,
    )

    assert wrapped is not None
    assert wrapped.canonical_command == "scripts/run_tests.sh"
    assert wrapped.scope == "targeted"
    assert echoed is None


@pytest.mark.parametrize(
    "command",
    [
        "pytest || true",
        "pytest ; true",
        "pytest | tee test.log",
        "pytest &",
    ],
)
def test_masking_shell_control_is_not_verification_evidence(
    tmp_path, monkeypatch, command
):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / ".hermes"))
    _python_project(tmp_path)

    evidence = classify_verification_command(
        command,
        cwd=tmp_path,
        session_id="s1",
        exit_code=0,
    )

    assert evidence is None


@pytest.mark.parametrize("command", ["prepare && pytest", "pytest && report"])
def test_successful_and_chain_preserves_passing_evidence(
    tmp_path, monkeypatch, command
):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / ".hermes"))
    _python_project(tmp_path)

    evidence = classify_verification_command(
        command,
        cwd=tmp_path,
        session_id="s1",
        exit_code=0,
    )

    assert evidence is not None
    assert evidence.status == "passed"


@pytest.mark.parametrize("exit_code, expected", [(0, "passed"), (1, "failed")])
def test_final_verifier_after_sequence_owns_shell_exit_status(
    tmp_path, monkeypatch, exit_code, expected
):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / ".hermes"))
    _python_project(tmp_path)

    evidence = classify_verification_command(
        "prepare; pytest",
        cwd=tmp_path,
        session_id="s1",
        exit_code=exit_code,
    )

    assert evidence is not None
    assert evidence.status == expected


def test_quoted_shell_operator_remains_a_verifier_argument(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / ".hermes"))
    _python_project(tmp_path)

    evidence = classify_verification_command(
        "pytest -k 'passes || fails'",
        cwd=tmp_path,
        session_id="s1",
        exit_code=0,
    )

    assert evidence is not None
    assert evidence.status == "passed"


@pytest.mark.parametrize("redirect", ["2>&1", "&> test.log"])
def test_shell_redirection_does_not_hide_simple_verifier(tmp_path, monkeypatch, redirect):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / ".hermes"))
    _python_project(tmp_path)

    evidence = classify_verification_command(
        f"pytest {redirect}",
        cwd=tmp_path,
        session_id="s1",
        exit_code=0,
    )

    assert evidence is not None
    assert evidence.status == "passed"


def test_masked_ad_hoc_script_is_not_verification_evidence(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / ".hermes"))
    (tmp_path / "package.json").write_text("{}", encoding="utf-8")
    script = Path(tempfile.gettempdir()) / f"hermes-ad-hoc-{tmp_path.name}.py"
    script.write_text("raise SystemExit(1)\n", encoding="utf-8")
    try:
        evidence = classify_verification_command(
            f"python {script} || true",
            cwd=tmp_path,
            session_id="s1",
            exit_code=0,
        )
    finally:
        script.unlink(missing_ok=True)

    assert evidence is None


def test_masked_verifier_does_not_clear_edited_ledger_state(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / ".hermes"))
    _python_project(tmp_path)
    record_terminal_result(
        command="pytest",
        cwd=tmp_path,
        session_id="s1",
        exit_code=0,
        output="passed",
    )
    mark_workspace_edited(
        session_id="s1",
        cwd=tmp_path,
        paths=[str(tmp_path / "changed.py")],
    )

    result = record_terminal_result(
        command="pytest || true",
        cwd=tmp_path,
        session_id="s1",
        exit_code=0,
        output="1 failed",
    )

    assert result is None
    assert verification_status(session_id="s1", cwd=tmp_path)["status"] == "stale"




def test_temp_script_records_ad_hoc_evidence_without_canonical_suite(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / ".hermes"))
    (tmp_path / "package.json").write_text("{}", encoding="utf-8")
    script = Path(tempfile.gettempdir()) / f"hermes-ad-hoc-{tmp_path.name}.py"
    script.write_text("print('ok')\n", encoding="utf-8")
    try:
        evidence = classify_verification_command(
            f"python {script}",
            cwd=tmp_path,
            session_id="s1",
            exit_code=0,
            output="ok",
        )
    finally:
        script.unlink(missing_ok=True)

    assert evidence is not None
    assert evidence.canonical_command == "ad-hoc verification script"
    assert evidence.kind == "ad_hoc"
    assert evidence.scope == "targeted"
    assert evidence.status == "passed"


@pytest.mark.parametrize(
    "invocation",
    [
        "python3.12 {script}",
        "/usr/bin/python3 {script}",
        "/usr/bin/python3.12 {script}",
        "python3 -u {script}",
        "/usr/bin/env python3 {script}",
    ],
)
def test_versioned_or_absolute_interpreter_records_ad_hoc_evidence(tmp_path, monkeypatch, invocation):
    """A versioned, absolute or `env`-prefixed interpreter names the same interpreter as a bare
    `python3`. Matching only the bare token left the ad-hoc branch blind to the invocation shapes
    the verify-on-stop nudge itself hands the agent, so a passing run recorded no evidence and
    every later turn re-nudged the same workspace as unverified.
    """
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / ".hermes"))
    (tmp_path / "package.json").write_text("{}", encoding="utf-8")
    script = Path(tempfile.gettempdir()) / f"hermes-verify-{tmp_path.name}.py"
    script.write_text("print('ok')\n", encoding="utf-8")
    try:
        evidence = classify_verification_command(
            invocation.format(script=script),
            cwd=tmp_path,
            session_id="s1",
            exit_code=0,
            output="ok",
        )
    finally:
        script.unlink(missing_ok=True)

    assert evidence is not None
    assert evidence.kind == "ad_hoc"
    assert evidence.status == "passed"


def test_non_interpreter_command_touching_the_temp_script_is_not_evidence(tmp_path, monkeypatch):
    """The nudge also tells the agent to clean the temp script up. A command that merely names
    the script — `rm`, `chmod`, `cat` — runs no verification and must not satisfy the gate.
    """
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / ".hermes"))
    (tmp_path / "package.json").write_text("{}", encoding="utf-8")
    script = Path(tempfile.gettempdir()) / f"hermes-verify-{tmp_path.name}.py"

    for command in (f"rm -f {script}", f"/usr/bin/chmod +x {script}", f"/usr/bin/cat {script}"):
        evidence = classify_verification_command(command, cwd=tmp_path, session_id="s1", exit_code=0)
        assert evidence is None, f"{command!r} must not be recorded as verification evidence"












def test_file_tool_stales_evidence_by_session_id_for_absolute_edit(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / ".hermes"))
    _node_project(tmp_path)
    target = tmp_path / "src" / "app.ts"
    target.parent.mkdir()

    record_terminal_result(
        command="pnpm test",
        cwd=tmp_path,
        session_id="conversation",
        exit_code=0,
        output="green",
    )

    from tools.file_tools import write_file_tool

    result = json.loads(
        write_file_tool(
            str(target),
            "export const ok = true\n",
            task_id="turn",
            session_id="conversation",
        )
    )

    assert result["files_modified"] == [str(target.resolve())]
    assert verification_status(session_id="conversation", cwd=tmp_path)["status"] == "stale"
    assert verification_status(session_id="turn", cwd=tmp_path)["status"] == "unverified"






def test_workspace_verify_clears_stale_on_other_session(tmp_path, monkeypatch):
    """A hermes verify pass is a property of the workspace, not the session
    that ran the CLI (#103271)."""
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / ".hermes"))
    _python_project(tmp_path)
    changed = str(tmp_path / "changed.py")
    mark_workspace_edited(session_id="s-edit", cwd=tmp_path, paths=[changed])
    assert verification_status(session_id="s-edit", cwd=tmp_path)["status"] == "unverified"

    event = record_verify_run(root=tmp_path, session_id="default", ok=True, output="all green")
    assert event is not None
    status = verification_status(session_id="s-edit", cwd=tmp_path)
    assert status["status"] == "passed"
    assert status["evidence"]["canonical_command"] == "hermes verify"


def test_targeted_partial_verify_does_not_clear_other_session(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / ".hermes"))
    _python_project(tmp_path)
    record_verify_run(root=tmp_path, session_id="s-edit", ok=True)
    mark_workspace_edited(session_id="s-edit", cwd=tmp_path, paths=[str(tmp_path / "a.py")])
    assert verification_status(session_id="s-edit", cwd=tmp_path)["status"] == "stale"

    record_verify_run(root=tmp_path, session_id=None, ok=True, scope="targeted")

    status = verification_status(session_id="s-edit", cwd=tmp_path)
    assert status["status"] == "stale"


def test_failed_verify_does_not_erase_other_session_edit_record(tmp_path, monkeypatch):
    home = tmp_path / ".hermes"
    monkeypatch.setenv("HERMES_HOME", str(home))
    _python_project(tmp_path)
    mark_workspace_edited(session_id="s-edit", cwd=tmp_path, paths=[str(tmp_path / "a.py")])

    record_verify_run(root=tmp_path, session_id=None, ok=False)

    row = sqlite3.connect(home / "verification_evidence.db").execute(
        "SELECT last_edit_at, changed_paths_json FROM verification_state WHERE session_id='s-edit'"
    ).fetchone()
    assert row is not None
    assert row[0] is not None
    assert verification_status(session_id="s-edit", cwd=tmp_path)["status"] != "passed"


def test_older_workspace_verify_does_not_clear_later_edit(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / ".hermes"))
    _python_project(tmp_path)
    record_verify_run(root=tmp_path, session_id="default", ok=True, output="old pass")
    mark_workspace_edited(
        session_id="s-edit",
        cwd=tmp_path,
        paths=[str(tmp_path / "changed.py")],
    )
    assert verification_status(session_id="s-edit", cwd=tmp_path)["status"] == "unverified"


def test_recording_expires_old_edit_only_state(tmp_path, monkeypatch):
    home = tmp_path / ".hermes"
    monkeypatch.setenv("HERMES_HOME", str(home))
    _node_project(tmp_path)

    mark_workspace_edited(
        session_id="old-session",
        cwd=tmp_path,
        paths=[str(tmp_path / "src" / "app.ts")],
    )
    cutoff = (datetime.now(timezone.utc) - timedelta(days=31)).isoformat()
    with sqlite3.connect(home / "verification_evidence.db") as conn:
        conn.execute("UPDATE verification_state SET last_edit_at = ?", (cutoff,))
        conn.commit()

    record_terminal_result(
        command="pnpm test",
        cwd=tmp_path,
        session_id="new-session",
        exit_code=0,
        output="new green",
    )

    status = verification_status(session_id="old-session", cwd=tmp_path)
    assert status["status"] == "unverified"
    assert status["changed_paths"] == []
