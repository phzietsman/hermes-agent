"""Update dependency diagnostics and historical markers."""

import logging
import os
import sys

from pathlib import Path
from typing import NoReturn

# Log-record parity with the origin module.
logger = logging.getLogger("hermes_cli.main")


def _pyproject_project(debug_fmt: str | None = None) -> dict | None:
    """``[project]`` table of pyproject.toml, or ``None`` when absent/unreadable."""
    from hermes_cli.main import PROJECT_ROOT
    pyproject = PROJECT_ROOT / "pyproject.toml"
    if not pyproject.is_file():
        return None
    try:
        import tomllib
        with pyproject.open("rb") as handle:
            project = tomllib.load(handle).get("project", {})
    except Exception as exc:
        if debug_fmt:
            logger.debug(debug_fmt, exc)
        return None
    return project if isinstance(project, dict) else None


# Install-scoped breadcrumbs live next to the venv (not under $HERMES_HOME)
# because the venv is shared across profiles.
#   ``.update-incomplete``       — generic core ``.[all]`` install was interrupted;
#     cleared only after a confirmed full dependency reinstall/recovery.
#   ``.lazy-refresh-incomplete`` — lazy-backend refresh may have corrupted packages;
#     cleared only after import-probe repair confirms healthy (never on indeterminate).
# Narrow lazy probes must NEVER clear the generic core marker.
# See #58004.
def _update_marker_path() -> Path:
    from hermes_cli.main import PROJECT_ROOT
    return PROJECT_ROOT / ".update-incomplete"


def _lazy_refresh_marker_path() -> Path:
    from hermes_cli.main import PROJECT_ROOT
    return PROJECT_ROOT / ".lazy-refresh-incomplete"


def _pytest_owns_live_checkout(root: Path) -> bool:
    """True under pytest when ``root`` is this checkout: unsandboxed update/recovery tests must
    neither litter the live repo root with breadcrumbs (false-arming the developer's next launch)
    nor run a real reinstall against the executing venv (cf. ``managed_scope._under_pytest``)."""
    return "PYTEST_CURRENT_TEST" in os.environ and root == Path(__file__).resolve().parent.parent


def _clear_marker_file(path: Path, *, label: str) -> None:
    """Remove an update-recovery breadcrumb. Never raises."""
    try:
        path.unlink()
    except FileNotFoundError:
        pass
    except OSError as exc:
        logger.debug("Could not clear %s marker: %s", label, exc)


def _clear_update_incomplete_marker() -> None:
    """Remove the interrupted core-install breadcrumb. Never raises."""
    _clear_marker_file(_update_marker_path(), label="update-incomplete")


def _clear_lazy_refresh_incomplete_marker() -> None:
    """Remove the interrupted lazy-refresh breadcrumb. Never raises."""
    _clear_marker_file(_lazy_refresh_marker_path(), label="lazy-refresh-incomplete")


# Frozen old-updater import; current updates never detach dependency work.
_UPDATE_REEXEC_ENV = "HERMES_UPDATE_REEXEC"


def _reexec_dependency_sync_off_windows_shim() -> NoReturn:
    """Stop a mixed old-code/new-files updater at the retired sync boundary."""
    from hermes_cli._old_updater import stop_for_relaunch
    stop_for_relaunch()


# Frozen old-updater surface (tests/compat/old_updater_surface.json): a running pre-PM updater
# lazy-imports these from the new tree mid-update. Windows support is gone; each keeps the
# POSIX behaviour it always had. Current code must not call them.
def _is_windows() -> bool:
    return False


def _hermes_exe_shims(scripts_dir: Path) -> list[Path]:
    return []


def _venv_scripts_dir() -> Path | None:
    """Return the venv Scripts directory if we're running inside the project venv."""
    from hermes_cli.main import PROJECT_ROOT
    from hermes_constants import project_venv_dir
    from pm.environments import venv_bin_dir
    venv_dir = project_venv_dir(PROJECT_ROOT)
    if venv_dir is None:
        return None
    scripts = venv_bin_dir(venv_dir)
    return scripts if scripts.is_dir() else None


def _recover_update_debts_on_startup() -> None:
    """The startup half of a previous update, run on every launch before dispatch."""
    # Dependency recovery already ran before imports. Report any fleet restart
    # still owed by a previous update without restarting services here.
    if "update" not in sys.argv[1:]:
        try:
            from hermes_cli.update_cmd_fleet import _warn_pending_fleet_restart_on_startup

            _warn_pending_fleet_restart_on_startup()
        except Exception:  # a startup warning never blocks the launch it warns about
            logger.debug("pending fleet restart check failed", exc_info=True)
    from hermes_cli.update_pause_record import recover as _recover_paused_gateways
    # Unconditional: recover() itself skips the PARSED `update`/`gateway run` commands; a raw argv
    # value equal to "update" (`--resume update`) is no reason to strand paused gateways.
    _recover_paused_gateways()  # a killed `hermes update` left gateways paused


def _configured_features_missing_deps() -> list[tuple[str, str, str]]:
    """Check configured platforms/MCP in the fresh, selected update-build child.

    PM preserves recorded extras atomically, but configuration can reference an
    unselected SDK. Never call this in the updater's stale import graph (#10651).
    Rows are ``(label, hint, candidate extra)``: a platform's SDK is the extra named after it.
    """
    missing: list[tuple[str, str, str]] = []
    try:
        from gateway.config import load_gateway_config
        from gateway.platform_registry import platform_registry

        for platform in load_gateway_config().get_connected_platforms():
            entry = platform_registry.get(platform.value)
            if entry is not None and not entry.check_fn():
                missing.append((entry.label, entry.install_hint or "Run `hermes setup` to install support.",
                                entry.name))
    except Exception as exc:
        logger.debug("configured-platform dependency check skipped: %s", exc)
    try:
        import importlib.util
        from hermes_cli.config import load_config_readonly

        if (load_config_readonly().get("mcp_servers") or {}) and importlib.util.find_spec("mcp") is None:
            missing.append(("MCP servers", "Run `hermes pm install` to install MCP support.", "mcp"))
    except Exception as exc:
        logger.debug("configured-MCP dependency check skipped: %s", exc)
    return missing


def _install_configured_features_missing_deps(project_root: Path) -> None:
    """Add the extras configured features need to the dependency environment (#124228).

    Recorded extras never follow config, so an enabled platform whose SDK is not
    selected would come up without its adapter after the gateway restart.
    """
    import pm
    from pm.extras import extra_supported
    from pm.features import declared_extras

    missing = _configured_features_missing_deps()
    if not missing:
        return
    declared = set(declared_extras(project_root))
    extras = sorted({extra for *_, name in missing
                     if (extra := name.replace("_", "-")) in declared and extra_supported(extra)})
    if extras:
        try:
            pm.sync_venv(extras, explicit=True, project_root=project_root, evict_incompatible_plugins=True)
        except Exception as exc:  # noqa: BLE001 — a feature install never fails the update; warn below
            print(f"  ⚠ Could not install {', '.join(extras)} for configured features: {exc}")
        else:
            missing = [row for row in missing if row[2].replace("_", "-") not in extras]
    if missing:
        print("  ⚠ Configured features whose dependencies are still missing — the gateway will fail to load them on restart:")
        for feature, hint, _extra in missing:
            print(f"    - {feature}: {hint}")


def _load_console_script_names() -> list[str]:
    """Return ``[project.scripts]`` entry-point names from pyproject.toml."""
    project = _pyproject_project("console script verification: failed to read pyproject.toml: %s")
    scripts = (project or {}).get("scripts", {}) or {}
    return [str(name) for name in scripts if name]


def _is_termux_env(env: dict[str, str] | None = None) -> bool:
    from hermes_cli.main import _is_termux_startup_environment
    return _is_termux_startup_environment(env)


def _is_windows_npm_path(npm_path: str) -> bool:
    """True if ``npm_path`` points at a Windows npm shim (WSL drive interop, ``.cmd``/``.exe``, UNC).

    A WSL host can see the Windows side's npm through drive interop; it must not run it.
    """
    low = npm_path.lower()
    mount = low.split("/", 3)[2] if low.startswith("/mnt/") else ""
    return (
        low.endswith((".exe", ".cmd", ".bat"))
        or (len(mount) == 1 and mount.isalpha())
        or "\\" in npm_path
    )


def _resolve_node_runtime_npm() -> str | None:
    """PM's npm, refused on a POSIX host when it is a Windows shim (EISDIR over WSL UNC paths, #30271).

    Never re-scans the user's PATH for another npm: Hermes runs only its PM-managed toolchain.
    """
    from hermes_constants import find_node_executable
    npm = find_node_executable("npm")
    if not npm:
        return npm
    return None if _is_windows_npm_path(npm) else npm


def _resolve_update_branch(args) -> str:
    """Normalize ``args.branch`` to a non-empty name (default ``main``; blank/whitespace = default)."""
    return (getattr(args, "branch", None) or "main").strip() or "main"
