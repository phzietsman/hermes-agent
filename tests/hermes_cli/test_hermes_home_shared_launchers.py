"""A launch under another HERMES_HOME must not rebind shared launchers (#123238).

The checkout's ``.hermes/bin`` launchers are shared by every data root and by
the ``~/.local/bin`` shims. Rewriting them to the launching root's store
Python bricks ``hermes`` once that root is deleted. The launching process must
still relaunch into its own root's Python.
"""
import json
from pathlib import Path

import pm
from hermes_cli import _launchers
from hermes_cli import venv_sync


def _make_home(base: Path, name: str) -> tuple[Path, Path]:
    """A fake data root whose store records one live interpreter."""
    home = base / name
    store = home / "tools"
    entry = store / f"python-{name}"
    python = entry / "bin" / "python3"
    python.parent.mkdir(parents=True)
    python.touch()
    (store / "facts.json").write_text(json.dumps({
        "schema": 1,
        "packages": {"python": {"version": "fixture", "entry": entry.name}},
    }), encoding="utf-8")
    return home, python


def _repoint_store(home: Path, name: str) -> Path:
    """Move the fake store's recorded interpreter to a new live entry."""
    store = home / "tools"
    entry = store / f"python-{name}"
    python = entry / "bin" / "python3"
    python.parent.mkdir(parents=True)
    python.touch()
    (store / "facts.json").write_text(json.dumps({
        "schema": 1,
        "packages": {"python": {"version": "fixture", "entry": entry.name}},
    }), encoding="utf-8")
    return python


def _make_repo(base: Path) -> Path:
    repo = base / "checkout"
    (repo / ".git").mkdir(parents=True)
    (repo / "pyproject.toml").write_text('[project]\nname = "x"\nversion = "0"\n', encoding="utf-8")
    (repo / "install-stamp.json").write_text(json.dumps({"updateMechanism": "self"}), encoding="utf-8")
    return repo


def _isolate(tmp_path, monkeypatch, home: Path) -> None:
    monkeypatch.setenv("HOME", str(tmp_path))
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.delenv("HERMES_RUNTIME_DIR", raising=False)
    monkeypatch.delenv("HERMES_DISABLE_LAZY_INSTALLS", raising=False)


def _publish(repo: Path) -> dict[str, bytes]:
    local = repo / ".hermes" / "bin"
    written = _launchers.ensure_install_launchers(repo, local)
    assert len(written) == len(_launchers.ENTRY_POINTS), written
    return {Path(p).name: Path(p).read_bytes() for p in written}


def test_temp_home_prepare_launch_relaunches_without_rebinding(tmp_path, monkeypatch):
    # An apostrophe in the owner's path: the guard must read back what the writer quoted.
    default_home, _ = _make_home(tmp_path, "owner's home")
    temp_home, temp_python = _make_home(tmp_path, "temp")
    repo = _make_repo(tmp_path)

    _isolate(tmp_path, monkeypatch, default_home)
    before = _publish(repo)

    _isolate(tmp_path, monkeypatch, temp_home)
    monkeypatch.setattr(pm, "venv_is_current", lambda **kwargs: True)
    target = venv_sync.prepare_launch(repo, [])
    assert target is not None
    assert target.resolve() == temp_python.resolve()
    for name, content in before.items():
        assert (repo / ".hermes" / "bin" / name).read_bytes() == content


def test_missing_dead_and_same_store_launchers_still_publish(tmp_path, monkeypatch):
    default_home, _ = _make_home(tmp_path, "default")
    temp_home, temp_python = _make_home(tmp_path, "temp")
    repo = _make_repo(tmp_path)
    _isolate(tmp_path, monkeypatch, default_home)

    # Missing launchers are published.
    before = _publish(repo)

    # A same-store repin is published.
    repinned = _repoint_store(default_home, "repin")
    after = _publish(repo)
    assert after != before
    assert all(_launchers._launcher_python(repo / ".hermes" / "bin" / name) == repinned for name in after)

    # A launcher whose interpreter died with its root is repaired, not kept: the bricked
    # state of #123238 heals on the next launch from any live root.
    repinned.unlink()
    _isolate(tmp_path, monkeypatch, temp_home)
    healed = _publish(repo)
    assert all(_launchers._launcher_python(repo / ".hermes" / "bin" / name) == temp_python for name in healed)
