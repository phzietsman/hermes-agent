# -*- coding: utf-8 -*-
"""Regression tests for the fail-closed PID-ownership guard.

Refs #90471 / #89614. ``hermes_cli/_subprocess_compat.pid_is_hermes`` /
``kill_process_tree`` and ``hermes_cli/dashboard_procs`` stop targeting:

1. a recycled or foreign PID control process remains untouched
2. probe failure is never converted into permission to kill
"""
from pathlib import Path
from unittest import mock

from hermes_cli import _subprocess_compat
from hermes_cli import dashboard_procs


class TestPidIsHermes:
    """The shared identity probe must fail closed on every ambiguity."""

    def test_valid_pid_without_fingerprint_passes(self):
        assert _subprocess_compat.pid_is_hermes(1234) is True

    def test_rejects_recycled_identity(self):
        with mock.patch.object(_subprocess_compat, "_process_start_time", return_value=456):
            assert _subprocess_compat.pid_is_hermes(1234, expected_start_time=123) is False

    def test_matching_identity_passes(self):
        with mock.patch.object(_subprocess_compat, "_process_start_time", return_value=123):
            assert _subprocess_compat.pid_is_hermes(1234, expected_start_time=123) is True

    def test_probe_oserror_fails_closed(self):
        with mock.patch.object(
            _subprocess_compat, "_process_start_time", side_effect=OSError("broken pipe")
        ):
            assert _subprocess_compat.pid_is_hermes(1234, expected_start_time=123) is False

    def test_invalid_pid_inputs_do_not_crash(self):
        assert _subprocess_compat.pid_is_hermes(-1) is False
        assert _subprocess_compat.pid_is_hermes(0) is False
        assert _subprocess_compat.pid_is_hermes("not-a-pid") is False
        assert _subprocess_compat.pid_is_hermes(True) is False


def test_stop_only_targets_the_invoking_hermes_home(monkeypatch):
    """An argv match from another profile is never a ``--stop`` target."""
    own_home = "/tmp/hermes-own"
    foreign_home = "/tmp/hermes-foreign"
    monkeypatch.setenv("HERMES_HOME", own_home)

    with mock.patch.object(
        dashboard_procs, "_scan_dashboard_processes",
        return_value=[(12345, "hermes serve"), (12346, "hermes serve"), (12347, "hermes serve")],
    ), mock.patch.object(dashboard_procs, "_caller_ancestor_pids", return_value=set()), mock.patch.object(
        dashboard_procs, "_hermes_home_for_pid",
        side_effect=lambda pid: {
            12345: own_home,
            12346: foreign_home,
            12347: None,
        }[pid],
    ), mock.patch.object(
        dashboard_procs, "_kill_pids_posix"
    ) as kill:
        result = dashboard_procs._kill_stale_dashboard_processes(scope_home=own_home)

    kill.assert_called_once()
    assert kill.call_args.args[0] == [12345]
    assert result["matched"] == [12345]


class TestHermesHomeForPid:
    """Tri-state owner resolution: a readable environment always names a home."""

    def test_readable_env_without_var_resolves_to_that_process_default_home(self, monkeypatch, tmp_path):
        """The common install shape exports no HERMES_HOME: the backend lives in its user's
        platform default home, and a default-home ``--stop`` must still find it (#113978)."""
        home = str(tmp_path / "alice")
        monkeypatch.setattr(dashboard_procs, "_pid_environ", lambda pid: {"HOME": home})
        from hermes_cli import main_dashboard
        monkeypatch.setattr(main_dashboard, "_dashboard_cmdline_for_pid",
                            lambda pid: ["hermes", "--profile", "work", "serve"] if pid == 2 else ["hermes", "serve"])

        assert dashboard_procs._hermes_home_for_pid(1) == f"{home}/.hermes"
        # ``-p``/``--profile`` is applied to os.environ after exec, invisible in /proc environ.
        assert dashboard_procs._hermes_home_for_pid(2) == f"{home}/.hermes/profiles/work"
        assert dashboard_procs._pids_owned_by_hermes_home([1, 2], f"{home}/.hermes") == [1]

    # REGRESSION (#116906): a systemd/launchd unit with a scrubbed environment exports no HOME.
    # The target resolves its own default home from the password database, so attributing it to
    # the INSPECTING process's home named another user's directory — and `hermes update` /
    # `--stop` then acted on the wrong profile root.
    def _posix_scrubbed_unit(self, monkeypatch, tmp_path, passwd_home):
        """A unit whose environment carries neither HOME nor HERMES_HOME."""
        monkeypatch.setattr(dashboard_procs, "_pid_environ", lambda pid: {})
        monkeypatch.setattr(dashboard_procs, "_pid_passwd_home", lambda pid: passwd_home)
        monkeypatch.setattr(Path, "home", classmethod(lambda cls: tmp_path / "inspecting-user"))
        from hermes_cli import main_dashboard
        monkeypatch.setattr(main_dashboard, "_dashboard_cmdline_for_pid", lambda pid: ["hermes", "serve"])

    def test_scrubbed_unit_env_resolves_to_the_owners_passwd_home(self, monkeypatch, tmp_path):
        service_home = tmp_path / "hermes-service"
        self._posix_scrubbed_unit(monkeypatch, tmp_path, str(service_home))

        assert Path(dashboard_procs._hermes_home_for_pid(1)) == service_home / ".hermes"

    def test_unreadable_passwd_entry_keeps_the_existing_fallback(self, monkeypatch, tmp_path):
        """No owner, no entry: the previous behaviour stands rather than resolving to nothing."""
        self._posix_scrubbed_unit(monkeypatch, tmp_path, None)

        assert Path(dashboard_procs._hermes_home_for_pid(1)) == tmp_path / "inspecting-user" / ".hermes"

    def test_root_shaped_hermes_home_follows_the_flag_and_the_sticky_active_profile(self, monkeypatch, tmp_path):
        """Mirror ``_apply_profile_override``: an exported root ``HERMES_HOME`` is the root, not the
        home — ``-p work`` and ``hermes profile use work`` both land in ``<root>/profiles/work``."""
        root = tmp_path / ".hermes"
        root.mkdir()
        monkeypatch.setattr(dashboard_procs, "_pid_environ",
                            lambda pid: {"HOME": str(tmp_path), "HERMES_HOME": str(root)})
        from hermes_cli import main_dashboard
        monkeypatch.setattr(main_dashboard, "_dashboard_cmdline_for_pid",
                            lambda pid: ["hermes", "-p", "work", "serve"] if pid == 2 else ["hermes", "serve"])

        assert dashboard_procs._hermes_home_for_pid(2) == str(root / "profiles" / "work")
        assert dashboard_procs._hermes_home_for_pid(1) == str(root)  # no flag, no active_profile
        (root / "active_profile").write_text("work", encoding="utf-8")
        assert dashboard_procs._hermes_home_for_pid(1) == str(root / "profiles" / "work")
        # A profile-shaped HERMES_HOME without a flag is the home itself (root = its grandparent).
        monkeypatch.setattr(dashboard_procs, "_pid_environ",
                            lambda pid: {"HERMES_HOME": str(root / "profiles" / "ops")})
        assert dashboard_procs._hermes_home_for_pid(1) == str(root / "profiles" / "ops")

    def test_unreadable_env_is_none_and_spared(self, monkeypatch):
        monkeypatch.setattr(dashboard_procs, "_pid_environ", lambda pid: None)
        assert dashboard_procs._hermes_home_for_pid(7) is None
        assert dashboard_procs._pids_owned_by_hermes_home([7], "/home/alice/.hermes") == []
