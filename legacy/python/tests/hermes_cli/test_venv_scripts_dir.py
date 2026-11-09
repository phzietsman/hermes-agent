"""``_venv_scripts_dir`` resolves both venv layouts (#79542)."""

from __future__ import annotations

import pytest

from hermes_cli import main as cli_main
from hermes_cli import main_install_repair
from hermes_constants import venv_bin_dir


@pytest.mark.parametrize("venv_name", ["venv", ".venv"])
def test_venv_scripts_dir_finds_both_layouts(tmp_path, monkeypatch, venv_name):
    """uv writes .venv; our installers write venv. Both must resolve."""
    scripts = venv_bin_dir(tmp_path / venv_name)
    scripts.mkdir(parents=True)
    monkeypatch.setattr(cli_main, "PROJECT_ROOT", tmp_path)
    assert main_install_repair._venv_scripts_dir() == scripts
