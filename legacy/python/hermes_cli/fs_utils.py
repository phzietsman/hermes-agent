"""Frozen old-updater surface (tests/compat/old_updater_surface.json): a running pre-PM updater
imports ``rmtree_force`` from the new tree mid-update. Current code uses ``shutil.rmtree``."""

import shutil
from pathlib import Path


def rmtree_force(path: Path) -> None:
    shutil.rmtree(path)
