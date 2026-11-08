"""CUA runtime validation, explicit PM setup, and native host integration."""

import json
from types import SimpleNamespace
from unittest.mock import patch

import pytest


def _runtime_manifest(version="0.20.0", *, omit=()):
    required = {
        "mcp": {"--socket", "--grant"},
        "serve": {"--socket", "--permission-mode", "--capability-manifest",
                  "--approve-capability-manifest", "--embedded"},
        "stop": {"--socket"},
    }
    return {
        "binary_version": version,
        "mcp_invocation": {"command": "cua-driver", "args": ["mcp"]},
        "subcommands": [
            {"name": command, "args": [{"name": arg} for arg in sorted(args - set(omit))]}
            for command, args in required.items()
        ],
    }


@pytest.mark.parametrize("version,omit,ready", [
    ("0.20.0", (), True),
    ("0.19.4", (), False),
    ("bad-version", (), False),
    ("0.20.0", ("--approve-capability-manifest",), False),
])
def test_runtime_contract(version, omit, ready, tmp_path):
    from hermes_cli import tools_config_cua as cua

    result = SimpleNamespace(returncode=0, stderr="",
                             stdout=json.dumps(_runtime_manifest(version, omit=omit)))
    binary = str(tmp_path / "cua-driver")
    with patch("subprocess.run", return_value=result):
        state = cua._cua_driver_contract_status(binary)
    assert state["ready"] is ready
    assert state["binary"] == binary
    assert bool(state["reason"]) is not ready
    if omit:
        assert "serve --approve-capability-manifest" in state["reason"]


@pytest.mark.parametrize("upgrade", [False, True])
def test_pm_failure_is_reported_without_vendor_fallback(monkeypatch, capsys, upgrade):
    import pm
    from hermes_cli import tools_config_cua as cua

    monkeypatch.delenv("HERMES_CUA_DRIVER_CMD", raising=False)
    monkeypatch.setattr(cua, "_resolved_cua_driver_cmd", lambda: None)
    with patch.object(pm, "ensure", side_effect=pm.InstallError("cua-driver", "offline")) as ensure, \
         patch.object(cua.subprocess, "Popen", side_effect=AssertionError("vendor installer")):
        assert not cua.install_cua_driver(upgrade=upgrade)
    ensure.assert_called_once_with("cua-driver", explicit=True)
    assert "offline" in capsys.readouterr().out


@pytest.mark.parametrize("upgrade", [False, True])
def test_broken_override_never_acquires_standard_driver(tmp_path, monkeypatch, upgrade):
    import pm
    from hermes_cli import tools_config_cua as cua

    monkeypatch.setenv("HERMES_CUA_DRIVER_CMD", str(tmp_path / "missing-driver"))
    with patch.object(pm, "ensure", side_effect=AssertionError("override was replaced")):
        assert not cua.install_cua_driver(upgrade=upgrade)


@pytest.mark.platforms("macos")
def test_setup_refuses_bare_binary_without_required_signed_app(monkeypatch, tmp_path):
    from hermes_cli import tools_config_cua as cua

    driver = tmp_path / "cua-driver"
    driver.write_text("#!/bin/sh\nexit 0\n")
    driver.chmod(0o755)
    monkeypatch.setenv("HERMES_CUA_DRIVER_CMD", str(driver))
    monkeypatch.setattr(cua, "_cua_driver_contract_status", lambda *a: {"ready": True})
    assert not cua.install_cua_driver(show_installer_progress=False)


@pytest.mark.platforms("macos")
def test_setup_registers_only_the_validated_selected_app(monkeypatch, tmp_path):
    from hermes_cli import tools_config_cua as cua
    from tools.computer_use import cua_backend_daemon as daemon

    app = tmp_path / "CuaDriver.app"
    driver = app / "Contents" / "MacOS" / "cua-driver"
    driver.parent.mkdir(parents=True)
    driver.write_text("#!/bin/sh\nexit 0\n")
    driver.chmod(0o755)
    monkeypatch.setenv("HERMES_CUA_DRIVER_CMD", str(driver))
    monkeypatch.setattr(cua, "_cua_driver_contract_status", lambda *a: {"ready": True})
    with patch.object(daemon, "_validate_cua_driver_app_signature") as validate, \
         patch.object(cua, "_run_text", return_value=SimpleNamespace(returncode=0)) as register:
        assert cua.install_cua_driver(show_installer_progress=False)
    validate.assert_called_once_with(str(app))
    assert register.call_args.args[0][-2:] == ["-f", str(app)]


@pytest.mark.platforms("macos")
def test_setup_keeps_permission_guidance(capsys):
    from hermes_cli.tools_config_cua import _print_cua_platform_notes

    _print_cua_platform_notes(False, fresh_install=True)
    output = capsys.readouterr().out
    assert "Accessibility" in output
    assert "Screen Recording" in output


@pytest.mark.parametrize("raw,expected", [
    ("cua-driver 0.20.0", "cua-driver 0.20.0"),
    ("banner\nsecond line", "banner"),
    ("\n\n  cua-driver 0.20.0  ", "cua-driver 0.20.0"),
    ("x" * 500, "x" * 120),
    ("", ""),
    ("   \n \n", ""),
])
def test_version_summary(raw, expected):
    from hermes_cli.tools_config_cua import _cua_version_summary

    assert _cua_version_summary(raw) == expected
