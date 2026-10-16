"""Baked bytecode: staging contract and launcher marker behavior."""

import os
import sys
from pathlib import Path

import pytest

from scripts.bundles.bytecode import MARKER, bake_bytecode


def _make_payload(root: Path) -> Path:
    (root / "hermes-agent" / "pkg").mkdir(parents=True)
    (root / "hermes-agent" / "pkg" / "__init__.py").write_text("")
    (root / "hermes-agent" / "pkg" / "mod.py").write_text("X = 1\n")
    site = root / "venv" / f"lib/python{sys.version_info.major}.{sys.version_info.minor}" / "site-packages"
    site.mkdir(parents=True)
    (site / "dep.py").write_text("Y = 2\n")
    pm = root / "pm-runtime" / f"lib/python{sys.version_info.major}.{sys.version_info.minor}" / "site-packages"
    pm.mkdir(parents=True)
    (pm / "pmdep.py").write_text("Z = 3\n")
    return root


def test_bake_produces_readonly_unchecked_hash_pycs(tmp_path):
    root = _make_payload(tmp_path)
    result = bake_bytecode(root, Path(sys.executable))
    assert (root / MARKER).read_text().strip() == "unchecked-hash"
    pyc = next((root / "hermes-agent" / "pkg" / "__pycache__").glob("mod*.pyc"))
    # PEP 552 header, little-endian flags at bytes 4..8: value 1 =
    # hash-based (bit0) and unchecked (bit1 clear). Timestamp pycs would be 0;
    # checked-hash would be 3.
    assert pyc.read_bytes()[4:8] == b"\x01\x00\x00\x00"
    # read-only before packaging: a cache-miss write cannot land
    assert not os.access(pyc, os.W_OK) or pyc.stat().st_mode & 0o222 == 0
    assert result["modules"] >= 3


def test_bake_fails_closed_on_uncompilable_module(tmp_path):
    """Strict compileall: an unparseable module fails the whole bake instead of
    silently shipping cold-compile-every-launch bytecode."""
    import subprocess as _sp
    root = _make_payload(tmp_path)
    (root / "hermes-agent" / "pkg" / "broken.py").write_text("this is (( not python\n")
    with pytest.raises(_sp.CalledProcessError):
        bake_bytecode(root, Path(sys.executable))


def test_uncovered_reports_parseable_module_without_pyc(tmp_path):
    """The coverage gate: a parseable module lacking bytecode is reported
    before it can ship as a cold-compile stall."""
    from importlib.util import cache_from_source
    root = _make_payload(tmp_path)
    bake_bytecode(root, Path(sys.executable))
    from scripts.bundles.bytecode import _uncovered
    missing, unparseable = _uncovered(root / "hermes-agent")
    assert missing == [] and unparseable == 0
    pyc = Path(cache_from_source(root / "hermes-agent" / "pkg" / "mod.py"))
    pyc.chmod(0o644)  # pycs are sealed read-only; dirs stay writable
    pyc.unlink()
    missing, _ = _uncovered(root / "hermes-agent")
    assert [m.name for m in missing] == ["mod.py"]


def test_bake_rejects_missing_import_root(tmp_path):
    (tmp_path / "venv").mkdir()
    with pytest.raises(FileNotFoundError):
        bake_bytecode(tmp_path, Path(sys.executable))


