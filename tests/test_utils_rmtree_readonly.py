"""``utils.rmtree_readonly`` removes trees that ``shutil.rmtree`` refuses.

Package installs and git object stores arrive as read-only trees, so every cleanup
path that deletes a clone needs the retry.  Regression coverage for #117170, #117176, #117179 and #117184.
"""

from __future__ import annotations

import os
import stat
from pathlib import Path

import pytest

import utils
from utils import rmtree_readonly


def _read_only_object_dir(root: Path) -> Path:
    """A clone-shaped tree whose loose object and its directory are read-only."""
    obj_dir = root / ".git" / "objects" / "4b"
    obj_dir.mkdir(parents=True)
    obj = obj_dir / "825dc642cb6eb9a060e54bf8d69288fbee4904"
    obj.write_text("blob", encoding="utf-8")
    obj.chmod(0o444)
    obj_dir.chmod(0o555)  # POSIX unlink needs a writable parent
    return obj_dir


def test_checkpoint_clear_all_removes_tree_with_read_only_object(tmp_path):
    """Driven through a production call site (#117170): ``checkpoint_maintenance.clear_all`` must
    reach ``rmtree_readonly`` — a bare ``shutil.rmtree`` there reports ``deleted=False``."""
    from tools.checkpoint_maintenance import clear_all

    root = tmp_path / "checkpoints"
    obj_dir = _read_only_object_dir(root)
    assert not (obj_dir / "825dc642cb6eb9a060e54bf8d69288fbee4904").stat().st_mode & stat.S_IWUSR

    out = clear_all(root)

    assert out["deleted"] is True
    assert not root.exists()


def test_non_permission_failures_propagate(tmp_path, monkeypatch):
    """Only ``PermissionError`` is retried — everything else keeps rmtree semantics."""
    attempts: list = []

    def _fake(path, **kwargs):
        attempts.append(path)
        raise OSError(39, "Directory not empty", str(path))

    monkeypatch.setattr(utils.shutil, "rmtree", _fake)

    with pytest.raises(OSError) as excinfo:
        rmtree_readonly(tmp_path)

    assert excinfo.value.errno == 39
    assert len(attempts) == 1  # no second attempt for a non-permission failure
