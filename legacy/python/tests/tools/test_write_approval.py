"""Tests for the skill write-approval gate (tools/write_approval.py)
and the shared slash-command handlers (hermes_cli/write_approval_commands.py).

Covers the boolean write_approval gate (off by default = write freely; on =
require approval) for the skills subsystem, pending store CRUD, and the list/approve/reject/diff/approval
subcommand dispatch.
"""

import json
import os
import tempfile
import shutil

import pytest

@pytest.fixture
def hermes_home(monkeypatch):
    d = tempfile.mkdtemp(prefix="hermes_wa_test_")
    home = os.path.join(d, ".hermes")
    os.makedirs(home)
    monkeypatch.setenv("HERMES_HOME", home)
    yield home
    shutil.rmtree(d, ignore_errors=True)

def _set_approval(subsystem, enabled):
    import hermes_cli.config as cfg
    c = cfg.load_config()
    c.setdefault(subsystem, {})["write_approval"] = enabled
    cfg.save_config(c)

# ---------------------------------------------------------------------------
# Config resolution
# ---------------------------------------------------------------------------

def test_list_pending_skips_non_dict_record(hermes_home):
    """A parseable-but-non-object pending file must be skipped, not crash the sort."""
    from tools import write_approval as wa
    wa.stage_write("skills", {"action": "create", "content": "ok"},
                   summary="ok", origin="foreground")
    pending_dir = wa._pending_path("skills", "").parent
    (pending_dir / "bad.json").write_text('"not a record"', encoding="utf-8")
    records = wa.list_pending("skills")
    assert len(records) == 1 and records[0]["payload"]["content"] == "ok"
    assert wa.get_pending("skills", "bad") is None

def test_normalize_enabled_coerces_values():
    from tools import write_approval as wa
    # Real bools pass through.
    assert wa._normalize_enabled(True) is True
    assert wa._normalize_enabled(False) is False
    # Truthy strings → True (incl. legacy 'approve').
    assert wa._normalize_enabled("on") is True
    assert wa._normalize_enabled("approve") is True
    assert wa._normalize_enabled("true") is True
    # Everything else → False (gate off is the safe default).
    assert wa._normalize_enabled("off") is False
    assert wa._normalize_enabled("garbage") is False
    assert wa._normalize_enabled(None) is False

# ---------------------------------------------------------------------------
# Gate
# ---------------------------------------------------------------------------

def test_gate_off_allows_write(hermes_home):
    from tools import write_approval as wa
    d = wa.evaluate_gate(wa.SKILLS)
    assert d.allow and not d.stage
    assert wa.pending_count(wa.SKILLS) == 0

def test_memory_is_not_a_write_approval_subsystem():
    from tools import write_approval as wa
    assert not hasattr(wa, "MEMORY")

def test_gate_on_stages(hermes_home):
    from tools import write_approval as wa
    _set_approval("skills", True)
    d = wa.evaluate_gate(wa.SKILLS)
    assert d.stage and not d.allow

# ---------------------------------------------------------------------------
# Shared command handler
# ---------------------------------------------------------------------------

def test_handle_reject_all(hermes_home):
    from hermes_cli.write_approval_commands import handle_pending_subcommand
    from tools import write_approval as wa
    for c in ("a", "b"):
        wa.stage_write("skills", {"action": "create", "content": c}, summary=c, origin="foreground")
    out = handle_pending_subcommand(wa.SKILLS, ["reject", "all"])
    assert "Rejected 2" in out
    assert wa.pending_count("skills") == 0

def test_handle_pending_lists_records(hermes_home):
    from hermes_cli.write_approval_commands import handle_pending_subcommand
    from tools import write_approval as wa
    rec = wa.stage_write("skills", {"action": "create", "content": "a"}, summary="gist-a", origin="background_review")
    out = handle_pending_subcommand(wa.SKILLS, ["pending"])
    assert "gist-a" in out and "[auto]" in out and "/skills approve" in out
    assert rec["id"] in out

def test_handle_approval_on(hermes_home):
    from hermes_cli.write_approval_commands import handle_pending_subcommand
    from tools import write_approval as wa
    captured = {}
    out = handle_pending_subcommand(
        wa.SKILLS, ["approval", "on"],
        set_mode_fn=lambda enabled: captured.update(enabled=enabled),
    )
    assert captured["enabled"] is True
    assert "on" in out

def test_handle_approval_off(hermes_home):
    from hermes_cli.write_approval_commands import handle_pending_subcommand
    from tools import write_approval as wa
    captured = {}
    out = handle_pending_subcommand(
        wa.SKILLS, ["approval", "off"],
        set_mode_fn=lambda enabled: captured.update(enabled=enabled),
    )
    assert captured["enabled"] is False
    assert "off" in out

# ---------------------------------------------------------------------------
# Staged-write review hint is surface-aware (#98330)
# ---------------------------------------------------------------------------

def _gate_message():
    from tools import write_approval as wa
    d = wa.evaluate_gate(wa.SKILLS)
    assert d.stage, d
    return d.message


def test_staged_hint_names_the_review_command_on_slash_surfaces(hermes_home):
    _set_approval("skills", True)
    assert "/skills pending" in _gate_message()


def test_staged_hint_names_the_pending_dir_on_headless_surfaces(hermes_home, monkeypatch):
    from tools import write_approval as wa
    _set_approval("skills", True)
    pending_dir = str(wa._pending_path(wa.SKILLS, "").parent)
    for var, val in (("HERMES_CRON_SESSION", "1"), ("HERMES_KANBAN_TASK", "task-7"),
                     ("HERMES_SESSION_PLATFORM", "api_server")):
        monkeypatch.setenv(var, val)
        msg = _gate_message()
        assert "pending records live in" in msg and pending_dir in msg, msg
        monkeypatch.delenv(var)


def test_staged_hint_keeps_command_for_chat_gateway_platform(hermes_home, monkeypatch):
    _set_approval("skills", True)
    monkeypatch.setenv("HERMES_SESSION_PLATFORM", "telegram")
    assert "/skills pending" in _gate_message()
