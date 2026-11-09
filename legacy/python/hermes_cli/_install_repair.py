"""Frozen old-updater surface (tests/compat/old_updater_surface.json).

A running pre-PM updater imports these from the new tree mid-update. Windows
launcher repair is gone; each keeps the POSIX behaviour it always had.
"""

from __future__ import annotations

from pathlib import Path


def _sync_windows_cli_launchers(root: Path) -> list[Path]:
    return []


def migrate_windows_bin_path(root, **_kwargs) -> bool:
    return False
