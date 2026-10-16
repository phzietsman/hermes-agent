"""PM-backed CUA setup and the host integration not supplied by its binary archive."""

from __future__ import annotations

import os
import subprocess
import sys
import time
from typing import Optional

from hermes_cli.cli_output import (
    print_info as _print_info, print_success as _print_success, print_warning as _print_warning)


def _run_text(cmd: list, *, timeout, capture_output: bool = True,
              **kwargs) -> subprocess.CompletedProcess:
    """Run a text subprocess with consistent decoding."""
    return subprocess.run(cmd, capture_output=capture_output, text=True, encoding="utf-8",
                          errors="replace", timeout=timeout, **kwargs)


def _fail(message: str, *hints: str) -> bool:
    _print_warning(message)
    for hint in hints:
        _print_info(hint)
    return False


def _print_output_tail(result: subprocess.CompletedProcess, printer=None) -> None:
    for line in (result.stderr or result.stdout or "").strip().splitlines()[-3:]:
        (printer or _print_info)(f"      {line[:200]}")


def _cua_driver_cmd() -> str:
    return os.environ.get("HERMES_CUA_DRIVER_CMD", "").strip() or "cua-driver"


def _cua_version_summary(raw: str, *, limit: int = 120) -> str:
    """Bound an external binary's potentially multiline version banner."""
    return next((line.strip()[:limit] for line in (raw or "").splitlines() if line.strip()), "")


def _resolved_cua_driver_cmd() -> Optional[str]:
    from tools.computer_use.cua_backend_driver import resolve_cua_driver_cmd
    return resolve_cua_driver_cmd()


def _cua_driver_env() -> dict:
    from tools.computer_use.cua_backend import sanitized_cua_driver_env
    return sanitized_cua_driver_env()


_CUA_DRIVER_CONTRACT_CACHE: dict = {}


def _cua_driver_contract_status(binary: Optional[str] = None) -> dict:
    """Cache the runtime manifest check by binary identity for UI polling."""
    from tools.computer_use.cua_backend_driver import cua_driver_runtime_contract_status
    resolved = binary or _resolved_cua_driver_cmd()
    if not resolved:
        return cua_driver_runtime_contract_status(None)
    try:
        stat = os.stat(resolved)
        fingerprint = (resolved, stat.st_mtime_ns, stat.st_size)
    except OSError:
        return cua_driver_runtime_contract_status(resolved)
    now = time.monotonic()
    cache = _CUA_DRIVER_CONTRACT_CACHE
    if cache.get("fingerprint") == fingerprint and now - cache.get("checked_at", 0.0) < 30.0:
        return dict(cache["state"])
    state = cua_driver_runtime_contract_status(resolved)
    cache.update(fingerprint=fingerprint, checked_at=now, state=dict(state))
    return state


def _cua_driver_install_ready() -> bool:
    state = _cua_driver_contract_status()
    if not state.get("ready"):
        return False
    if sys.platform == "darwin":
        from tools.computer_use.cua_backend_daemon import _resolve_cua_driver_app_path
        return bool(_resolve_cua_driver_app_path(state["binary"]))
    return True


def install_cua_driver(upgrade: bool = False, show_installer_progress: bool = True) -> bool:
    """Prepare the PM pin and host setup for an explicit install/upgrade command.

    Both CLI modes reconcile the same pin; neither discovers a vendor release.
    A configured override is validated, never replaced or acquired by PM.
    Unattended callers should use PM ensure directly, without interactive host setup.
    """
    from pm import ensure

    override = os.environ.get("HERMES_CUA_DRIVER_CMD", "").strip()
    binary = _resolved_cua_driver_cmd()
    fresh_install = binary is None
    if override:
        if not binary:
            return _fail(f"    HERMES_CUA_DRIVER_CMD does not resolve to an executable: {override}",
                         "    Fix or unset the override before running computer-use install.")
    else:
        if show_installer_progress:
            _print_info("    Preparing the pinned cua-driver with Hermes PM...")
        try:
            ensure("cua-driver", explicit=True)
        except Exception as exc:
            return _fail(f"    cua-driver preparation failed: {exc}")
        binary = _resolved_cua_driver_cmd()

    if not binary:
        return _fail("    PM did not select a usable cua-driver executable.")
    _CUA_DRIVER_CONTRACT_CACHE.clear()
    contract = _cua_driver_contract_status(binary)
    if not contract.get("ready"):
        hint = ("    Update the binary selected by HERMES_CUA_DRIVER_CMD, or unset the override."
                if override else "    Run: hermes computer-use doctor")
        return _fail("    cua-driver runtime contract is unusable: "
                     f"{contract.get('reason') or 'unknown error'}.", hint)
    if sys.platform == "darwin":
        from tools.computer_use.cua_backend_daemon import (
            _resolve_cua_driver_app_path, _validate_cua_driver_app_signature)

        app = _resolve_cua_driver_app_path(binary)
        if not app:
            return _fail("    macOS computer use requires the signed CuaDriver.app, not a bare binary.",
                         "    The PM cua-driver package must include its signed macOS app bundle.")
        try:
            _validate_cua_driver_app_signature(app)
            result = _run_text([
                "/System/Library/Frameworks/CoreServices.framework/Frameworks/"
                "LaunchServices.framework/Support/lsregister", "-f", app], timeout=15)
        except (RuntimeError, OSError, subprocess.SubprocessError) as exc:
            return _fail(f"    cua-driver macOS app registration failed: {exc}")
        if result.returncode:
            _print_output_tail(result)
            return _fail("    cua-driver macOS app registration failed.")
    if show_installer_progress:
        _print_success(f"    cua-driver ready: {contract.get('version') or 'unknown version'}.")
        _print_cua_platform_notes(sys.platform == "linux", fresh_install=fresh_install)
    return True


def _print_cua_platform_notes(is_linux: bool, *, fresh_install: bool) -> None:
    if is_linux:
        _print_warning("    Linux support is alpha.")
    else:
        _print_info("    IMPORTANT — grant macOS permissions now:" if fresh_install
                    else "    Grant macOS permissions if not done yet:")
        _print_info("      System Settings > Privacy & Security > Accessibility")
        _print_info("      System Settings > Privacy & Security > Screen Recording")
        _print_info("    Allow CuaDriver.app; run `hermes computer-use permissions grant` for guidance.")