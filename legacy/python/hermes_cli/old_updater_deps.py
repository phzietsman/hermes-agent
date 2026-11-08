"""Retired dependency hooks requested by already-running historical updaters."""
from typing import NoReturn

from hermes_cli._old_updater import stop_for_relaunch


def _capture_active_lazy_features() -> NoReturn:
    # PM owns the feature ledger; an old updater must not start a second install path.
    stop_for_relaunch()


def _refresh_active_lazy_features(*args, **kwargs) -> NoReturn:
    # Historical signatures varied; none may mutate the new environment.
    stop_for_relaunch()


def _refresh_active_memory_provider_dependencies() -> NoReturn:
    # Plugin dependencies participate in PM's union, not a last-writer reinstall.
    stop_for_relaunch()


def _npm_lockfile_changed(hermes_root) -> NoReturn:
    # Do not authorize an old npm install through a fabricated currency result.
    stop_for_relaunch()


def _update_node_dependencies() -> NoReturn:
    # Source builds use the shared Node dependency provider.
    stop_for_relaunch()


def _rebuild_desktop_after_update(desktop_dir, *, had_desktop_app_before_update) -> NoReturn:
    # Old callers must not report completion after skipping the build.
    stop_for_relaunch()


def _path_uid(path) -> NoReturn:
    # PM never mutates the old venv; do not re-enable its ownership preflight.
    stop_for_relaunch()


# Historical main's lazy exports can request these after swapping the checkout.
# No holder classification or tree kill is needed to prepare a new PM generation.
def _leftover_pausable_gateway_pids(matches: list[tuple[int, str, str]]) -> NoReturn:
    stop_for_relaunch()


def _ledger_manual_serve_holders(matches: list[tuple[int, str, str]]) -> NoReturn:
    stop_for_relaunch()


def _relaunch_stopped_serves(token: dict) -> None:
    from hermes_cli._old_updater import relaunch_stopped_serves
    relaunch_stopped_serves(token)


def _orphaned_desktop_backend_pids(matches: list[tuple[int, str, str]]) -> NoReturn:
    stop_for_relaunch()


def _ledger_reapable_backend_pids(matches: list[tuple[int, str, str]]) -> NoReturn:
    stop_for_relaunch()


def _handoff_reapable_backend_pids(matches: list[tuple[int, str, str]]) -> NoReturn:
    stop_for_relaunch()


def _stop_process_trees(pids: list[int] | list[tuple[int, int]]) -> NoReturn:
    stop_for_relaunch()


# Frozen old-updater surface (tests/compat/old_updater_surface.json and main's lazy exports): a
# running pre-PM updater looks these up in the new tree mid-update. Windows support is gone; each
# keeps the POSIX behaviour it always had. Current code must not call them.
def _pause_windows_gateways_for_update() -> None:
    return None


def _resume_windows_gateways_after_update(token: dict | None) -> None:
    from hermes_cli.update_cmd_posix_pause import resume_paused_gateways_after_update
    resume_paused_gateways_after_update(token)


def _cold_start_windows_gateway_after_update(token: dict | None = None) -> bool:
    return True


def _refresh_windows_gateway_launchers() -> None:
    return None


def _venv_launcher_ancestors(pids: list[int]) -> list[int]:
    return []


def _wait_for_windows_update_gateway_exit(pids: list[int], *, timeout: float) -> set[int]:
    from hermes_cli.update_cmd_posix_pause import _wait_gone
    return _wait_gone(pids, max(timeout, 0.0))


def _refuse_gateway_ancestor_tree_kill(pids: list[int], *, gateway_mode: bool) -> bool:
    return False


def _desktop_owns_gateway_lifecycle() -> bool:
    try:
        from hermes_cli.process_identity import ledger_entries, spawner_is_dead
        return any(e.get("purpose") in ("serve", "dashboard") and spawner_is_dead(e) is False
                   for e in ledger_entries())
    except Exception:  # health: allow BLE001 -- frozen old-updater stand-in: an unreadable ledger means "not Desktop-owned"
        return False


def _stop_windows_gateway_service(name: str, **kwargs) -> NoReturn:
    stop_for_relaunch()


def _start_windows_gateway_service(name: str, *, timeout: float = 30.0) -> NoReturn:
    stop_for_relaunch()


def _restore_windows_gateway_service(name: str, *, timeout: float = 60.0) -> NoReturn:
    stop_for_relaunch()


def _locate_real_git() -> None:
    # The Git-for-Windows trampoline swap is gone: there is never a replacement git binary.
    return None
