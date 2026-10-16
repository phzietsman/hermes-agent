"""A desktop app swap that died between its two renames is finished on the next swap, never
erased: the previous app must survive a crash at the commit point and a failing retry."""

from __future__ import annotations

import os
import sys
from pathlib import Path

from hermes_cli import main_desktop


def _packaged_exe_rel() -> Path:
    if sys.platform == "darwin":
        return Path("mac-arm64") / "Hermes.app" / "Contents" / "MacOS" / "Hermes"
    return Path("linux-unpacked") / "hermes"


def _staged_over_live(tmp_path: Path, monkeypatch):
    desktop_dir = tmp_path / "apps" / "desktop"
    live_exe = desktop_dir / "release" / _packaged_exe_rel()
    live_exe.parent.mkdir(parents=True)
    live_exe.write_text("old", encoding="utf-8")
    staging = main_desktop._desktop_staging_dir(desktop_dir)
    staged_exe = staging / _packaged_exe_rel()
    staged_exe.parent.mkdir(parents=True)
    staged_exe.write_text("new", encoding="utf-8")
    monkeypatch.setattr(main_desktop, "_stop_desktop_processes_locking_build", lambda d, **kw: [])
    slept: list[float] = []
    monkeypatch.setattr(main_desktop._time_mod, "sleep", slept.append)
    return desktop_dir, staging, live_exe, slept


def test_swap_after_a_crash_between_renames_keeps_the_previous_app(tmp_path, monkeypatch):
    desktop_dir, staging, live_exe, _slept = _staged_over_live(tmp_path, monkeypatch)
    live_root = live_exe.parent
    while live_root.parent.name != "release":
        live_root = live_root.parent
    # Crash state: the live app was moved aside and nothing moved in.
    previous = live_root.with_name(live_root.name + main_desktop._DESKTOP_PREVIOUS_SUFFIX)
    live_root.rename(previous)
    real_rename = os.rename

    def promotion_fails(src, dst):
        if Path(src).parent == staging:
            raise OSError(5, "disk error")
        return real_rename(src, dst)

    monkeypatch.setattr(main_desktop.os, "rename", promotion_fails)

    assert main_desktop._swap_staged_desktop_app(desktop_dir, staging) is None
    assert live_exe.read_text(encoding="utf-8-sig") == "old"
