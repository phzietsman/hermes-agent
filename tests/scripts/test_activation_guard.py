"""The guard repo scripts call to require an activated shell.

``scripts/_activation.py`` is stdlib-only and importable before the environment
it checks for exists.
"""

from __future__ import annotations

import pytest

from scripts._activation import ACTIVATION_COMMAND, ACTIVATION_ENV_VAR, require_activation


def test_returns_when_activated_and_exits_naming_the_command_when_not(monkeypatch, capsys):
    monkeypatch.setenv(ACTIVATION_ENV_VAR, "<installed-state path>")
    require_activation()  # must return, not raise

    monkeypatch.delenv(ACTIVATION_ENV_VAR)
    with pytest.raises(SystemExit) as excinfo:
        require_activation()
    assert excinfo.value.code == 1
    assert "source ./activate" == ACTIVATION_COMMAND
    assert ACTIVATION_COMMAND in capsys.readouterr().err
