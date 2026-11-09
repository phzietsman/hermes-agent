"""A per-file child that dies silently is still reported with its exit code."""

from __future__ import annotations

import importlib.util
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
_RUNNER_PATH = REPO_ROOT / "scripts" / "run_tests_parallel.py"


def _load_runner():
    spec = importlib.util.spec_from_file_location("run_tests_parallel", _RUNNER_PATH)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def test_silent_child_failure_reports_exit_code(tmp_path, monkeypatch):
    from types import SimpleNamespace
    mod = _load_runner()
    child = SimpleNamespace(pid=123, returncode=23, communicate=lambda **kwargs: ("", None))
    monkeypatch.setattr(mod.subprocess, "Popen", lambda *args, **kwargs: child)
    monkeypatch.setattr(mod, "_kill_tree", lambda *args, **kwargs: None)
    _, code, output, counts, _ = mod._run_one_file_once(tmp_path / "test.py", [], tmp_path, 30)
    assert code == 23
    assert "23" in output and "no output" in output.lower()
    assert counts == {}
