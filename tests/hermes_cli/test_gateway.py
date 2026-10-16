"""Tests for hermes_cli.gateway."""

import json
import os
import subprocess
import sys
import textwrap
from types import SimpleNamespace

import pytest

import hermes_cli.gateway as gateway


@pytest.mark.platforms("posix")  # POSIX PTY coverage
@pytest.mark.parametrize(
    ("stdin_is_tty", "outcome", "expected_exit"),
    [
        (True, "systemexit:75", 75),
        (False, "systemexit:75", 75),
        (False, "systemexit:78", 78),
        (False, "failure", 1),
    ],
)
def test_gateway_run_subprocess_preserves_daemon_exit_codes(
    tmp_path, stdin_is_tty, outcome, expected_exit
):
    """TTY state must not rewrite the gateway's process-level exit contract.

    Exit 75 is the intentional systemd/launchd restart handoff, exit 78 is a
    fatal configuration error, and a false startup result is a generic failure.
    In particular, a non-TTY daemon launch must not blanket-catch SystemExit,
    because doing so would hide genuine startup/configuration failures.
    """
    script = textwrap.dedent(
        """
        import os
        import sys
        import types

        import hermes_cli.gateway as gateway_cli

        outcome = os.environ["HERMES_TEST_GATEWAY_OUTCOME"]

        async def start_gateway(**kwargs):
            if outcome == "failure":
                return False
            raise SystemExit(int(outcome.split(":", 1)[1]))

        fake_run = types.ModuleType("gateway.run")
        fake_run.start_gateway = start_gateway
        setattr(fake_run, "_exit_after_graceful_shutdown", sys.exit)
        sys.modules["gateway.run"] = fake_run

        gateway_cli._guard_official_docker_root_gateway = lambda: None
        gateway_cli._guard_named_profile_under_multiplexer = lambda force=False: None
        gateway_cli._attach_to_host_gateway_or_guard = lambda **kwargs: None
        gateway_cli._guard_supervised_gateway_conflict = lambda force=False: None
        gateway_cli._guard_existing_gateway_process_conflict = lambda replace=False: None
        gateway_cli.supports_systemd_services = lambda: False
        gateway_cli.run_gateway()
        """
    )
    env = {
        **os.environ,
        "HERMES_HOME": str(tmp_path),
        "HERMES_GATEWAY_EXIT_DIAG": "0",
        "HERMES_TEST_GATEWAY_OUTCOME": outcome,
        "INVOCATION_ID": "systemd-test",
    }

    master_fd = slave_fd = None
    try:
        if stdin_is_tty:
            import pty

            master_fd, slave_fd = pty.openpty()
            stdin = slave_fd
        else:
            stdin = subprocess.DEVNULL
        completed = subprocess.run(
            [sys.executable, "-c", script],
            stdin=stdin,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            env=env,
            timeout=30,
            check=False,
        )
    finally:
        if slave_fd is not None:
            os.close(slave_fd)
        if master_fd is not None:
            os.close(master_fd)

    assert completed.returncode == expected_exit, completed.stderr


def test_s6_runtime_snapshot_reports_supervised_service(monkeypatch, tmp_path):
    service_dir = tmp_path / "gateway-default"
    service_dir.mkdir()

    class FakeS6Manager:
        scandir = tmp_path

        def is_running(self, name):
            assert name == "gateway-default"
            return True

    monkeypatch.setattr(gateway, "is_linux", lambda: True)
    monkeypatch.setattr("hermes_constants.is_container", lambda: True)
    monkeypatch.setattr("hermes_cli.service_manager.detect_service_manager", lambda: "s6")
    monkeypatch.setattr("hermes_cli.service_manager.get_service_manager", lambda: FakeS6Manager())
    monkeypatch.setattr(gateway, "find_gateway_pids", lambda: [123])
    monkeypatch.setattr(gateway, "_profile_suffix", lambda: "")

    snapshot = gateway.get_gateway_runtime_snapshot()

    assert snapshot.manager == "s6 (container supervisor)"
    assert snapshot.service_installed is True
    assert snapshot.service_running is True
    assert snapshot.service_scope == "s6"
    assert snapshot.gateway_pids == (123,)


def _run_status_with_snapshot(monkeypatch, snapshot) -> str:
    """``_cmd_status`` for a host with no installed systemd/launchd service."""
    import io
    from contextlib import redirect_stdout

    monkeypatch.setattr("hermes_cli.gateway_profile_lifecycle.print_parked_status", lambda: False)
    monkeypatch.setattr(gateway, "get_gateway_runtime_snapshot", lambda system=False: snapshot)
    monkeypatch.setattr(gateway, "_installed_service_kind", lambda: None)
    monkeypatch.setattr(gateway, "named_profile_served_by_running_multiplexer", lambda: False)
    for name in ("_print_runtime_health", "_print_multiplex_standalone_reason", "_print_served_ingress_urls",
                 "_print_duplicate_credential_warnings", "_print_other_profiles_gateway_status",
                 "_print_standalone_by_config"):
        monkeypatch.setattr(gateway, name, lambda *a, **k: None)
    buf = io.StringIO()
    with redirect_stdout(buf):
        gateway._cmd_status(SimpleNamespace(deep=False, full=False, system=False))
    return buf.getvalue()


def test_s6_supervised_gateway_without_scannable_pid_reports_running(monkeypatch):
    """#125390: s6 service up, process scan empty (`python -c` launcher argv is unmatched per
    #123881 and containers have no gateway.pid) — the default profile must not report an outage."""
    snapshot = gateway.GatewayRuntimeSnapshot(
        manager="s6 (container supervisor)", service_installed=True, service_running=True, gateway_pids=())
    out = _run_status_with_snapshot(monkeypatch, snapshot)
    assert out.startswith("✓ Gateway is running (supervised by s6 (container supervisor))")
    assert "not running" not in out


def test_manual_gateway_without_pids_still_reports_stopped(monkeypatch):
    out = _run_status_with_snapshot(monkeypatch, gateway.GatewayRuntimeSnapshot(manager="manual process"))
    assert out.startswith("✗ Gateway is not running")


class TestSystemdLingerStatus:
    def test_reports_enabled(self, monkeypatch):
        monkeypatch.setattr(gateway, "is_linux", lambda: True)
        monkeypatch.setenv("USER", "alice")
        monkeypatch.setattr(
            gateway.subprocess,
            "run",
            lambda *args, **kwargs: SimpleNamespace(returncode=0, stdout="yes\n", stderr=""),
        )
        monkeypatch.setattr("shutil.which", lambda name: "/usr/bin/loginctl")

        assert gateway.get_systemd_linger_status() == (True, "")


class TestContainerSystemdSupport:
    def test_supports_systemd_services_in_container_with_user_manager(self, monkeypatch):
        monkeypatch.setattr(gateway, "is_linux", lambda: True)
        monkeypatch.setattr(gateway, "is_wsl", lambda: False)
        monkeypatch.setattr(gateway, "is_container", lambda: True)
        monkeypatch.setattr("shutil.which", lambda name: "/usr/bin/systemctl")
        monkeypatch.setattr(gateway, "_systemd_operational", lambda system=False: not system)

        assert gateway.supports_systemd_services() is True


def test_spawn_detached_gateway_timestamps_stderr(monkeypatch, tmp_path):
    calls = []
    child_cmd = [
        "/usr/bin/python3",
        "-m",
        "hermes_cli.main",
        "gateway",
        "run",
        "--replace",
    ]

    def fake_popen(cmd, **kwargs):
        calls.append((cmd, kwargs))
        return SimpleNamespace()

    monkeypatch.setattr(gateway, "get_hermes_home", lambda: tmp_path)
    monkeypatch.setattr(gateway, "get_python_path", lambda: "/usr/bin/python3")
    monkeypatch.setattr(gateway, "_gateway_run_command", lambda: child_cmd)
    monkeypatch.setattr(gateway.subprocess, "Popen", fake_popen)

    assert gateway._spawn_detached_gateway() is True

    assert len(calls) == 1
    cmd, kwargs = calls[0]
    assert cmd[0] == "/usr/bin/python3"
    separator = cmd.index("--")
    assert cmd[separator - 2:separator] == ["--error-log", str(tmp_path / "logs" / "gateway.error.log")]
    assert cmd[separator + 1:] == child_cmd
    assert kwargs["stdin"] is gateway.subprocess.DEVNULL
    assert kwargs["stderr"] is gateway.subprocess.DEVNULL
    assert kwargs["stdout"].name == str(tmp_path / "logs" / "gateway.log")


@pytest.mark.platforms("posix")  # systemd user-linger is Linux-only (drives os.getuid())
def test_systemd_install_checks_linger_status(monkeypatch, tmp_path):
    unit_path = tmp_path / "systemd" / "user" / "hermes-gateway.service"

    monkeypatch.setattr(gateway, "get_systemd_unit_path", lambda system=False: unit_path)
    # Synthetic unit with a non-temp home: the real generator bakes the
    # hermetic test HERMES_HOME (a tmp dir), which the temp-home write
    # guard correctly refuses.
    monkeypatch.setattr(
        gateway,
        "generate_systemd_unit",
        lambda system=False, run_as_user=None: (
            '[Service]\nEnvironment="HERMES_HOME=/home/alice/.hermes"\n'
        ),
    )

    calls = []
    helper_calls = []

    def fake_run(cmd, check=False, **kwargs):
        calls.append((cmd, check))
        return SimpleNamespace(returncode=0, stdout="", stderr="")

    monkeypatch.setattr(gateway.subprocess, "run", fake_run)
    monkeypatch.setattr(gateway, "_ensure_linger_enabled", lambda: helper_calls.append(True))

    gateway.systemd_install(force=False)

    assert unit_path.exists()
    assert [cmd for cmd, _ in calls] == [
        ["systemctl", "--user", "daemon-reload"],
        ["systemctl", "--user", "enable", gateway.get_service_name()],
    ]
    assert helper_calls == [True]


def test_gateway_install_noninteractive_skips_legacy_unit_prompt(monkeypatch, tmp_path):
    """In non-TTY, the legacy-unit removal prompt in systemd_install is skipped.

    Covers the second hidden prompt that --start-now/--start-on-login do not
    guard. Originally contributed via PR #42124 (kyssta-exe).
    """
    monkeypatch.setattr(gateway, "has_legacy_hermes_units", lambda: True)

    calls = []
    monkeypatch.setattr(
        gateway,
        "prompt_yes_no",
        lambda question, default=True: calls.append(("prompt", question)) or True,
    )
    monkeypatch.setattr(gateway, "remove_legacy_hermes_units", lambda interactive=False: calls.append(("remove_legacy",)))
    monkeypatch.setattr(gateway, "print_legacy_unit_warning", lambda: None)

    fake_path = tmp_path / "hermes-gateway.service"
    monkeypatch.setattr(gateway, "get_systemd_unit_path", lambda system=False: fake_path)
    monkeypatch.setattr(gateway, "generate_systemd_unit", lambda system=False, run_as_user=None: "[Service]")
    monkeypatch.setattr(gateway, "_run_systemctl", lambda *a, **kw: None)
    monkeypatch.setattr(gateway, "_ensure_linger_enabled", lambda: None)
    monkeypatch.setattr(gateway, "print_systemd_scope_conflict_warning", lambda: None)
    monkeypatch.setattr(gateway, "_service_scope_label", lambda system=False: "user")

    gateway.systemd_install(non_interactive=True)

    # Legacy units removed without prompting.
    assert ("remove_legacy",) in calls
    assert all(c[0] != "prompt" for c in calls)


# ---------------------------------------------------------------------------
# _wait_for_gateway_exit
# ---------------------------------------------------------------------------


class TestWaitForGatewayExit:
    """PID-based wait with force-kill on timeout."""


    def test_force_kills_after_grace_period(self, monkeypatch):
        """When the process doesn't exit, force-kill the saved PID."""

        # Simulate monotonic time advancing past force_after
        call_num = 0
        def fake_monotonic():
            nonlocal call_num
            call_num += 1
            # First two calls: initial deadline + force_deadline setup (time 0)
            # Then each loop iteration advances time
            return call_num * 2.0  # 2, 4, 6, 8, ...

        kills = []
        def mock_terminate(pid, force=False, **kwargs):
            kills.append((pid, force))

        # get_running_pid returns the PID until kill is sent, then None
        def mock_get_running_pid():
            return None if kills else 42

        monkeypatch.setattr("time.monotonic", fake_monotonic)
        monkeypatch.setattr("time.sleep", lambda _: None)
        monkeypatch.setattr("gateway.status.get_running_pid", mock_get_running_pid)
        monkeypatch.setattr(gateway, "terminate_pid", mock_terminate)

        gateway._wait_for_gateway_exit(timeout=10.0, force_after=5.0)
        assert (42, True) in kills


    def test_kill_gateway_processes_force_uses_helper(self, monkeypatch):
        calls = []

        monkeypatch.setattr(gateway, "find_gateway_pids", lambda exclude_pids=None, all_profiles=False: [11, 22])
        # Kill-time re-verification: force-kills only proceed when the LIVE
        # cmdline still looks like a gateway.
        monkeypatch.setattr(
            gateway, "_capture_gateway_argv", lambda pid: ["python", "-m", "hermes_cli.main", "gateway", "run"]
        )
        monkeypatch.setattr(
            gateway,
            "terminate_pid",
            lambda pid, force=False, **kwargs: calls.append((pid, force)),
        )

        killed = gateway.kill_gateway_processes(force=True)

        assert killed == 2
        assert calls == [(11, True), (22, True)]

    def test_kill_gateway_processes_force_refuses_recycled_pid(self, monkeypatch):
        """A scanned PID whose live argv no longer looks like a gateway is skipped."""
        calls = []

        monkeypatch.setattr(gateway, "find_gateway_pids", lambda exclude_pids=None, all_profiles=False: [11, 22])
        monkeypatch.setattr(
            gateway,
            "_capture_gateway_argv",
            lambda pid: None if pid == 11 else ["python", "-m", "hermes_cli.main", "gateway", "run"],
        )
        monkeypatch.setattr(
            gateway,
            "terminate_pid",
            lambda pid, force=False, **kwargs: calls.append((pid, force)),
        )

        killed = gateway.kill_gateway_processes(force=True)

        assert killed == 1
        assert calls == [(22, True)]


class TestRestartWaitsForApiServerPort:
    """Regression for #91547: ``hermes gateway restart`` waited only for the old PID; on macOS the
    replacement then hit EADDRINUSE and ran with no API server."""

    def test_port_is_reported_free_once_the_old_listener_closes(self):
        import socket
        import threading

        listener = socket.socket()
        listener.bind(("127.0.0.1", 0))
        listener.listen(8)
        port = listener.getsockname()[1]
        threading.Timer(0.3, listener.close).start()

        assert gateway._wait_for_tcp_port_free("127.0.0.1", port, timeout=5.0) is True

    def test_wait_targets_the_configured_api_server_port_only_when_enabled(self, monkeypatch):
        import socket

        from gateway.config import GatewayConfig, Platform, PlatformConfig

        listener = socket.socket()
        listener.bind(("127.0.0.1", 0))
        listener.listen(8)
        port = listener.getsockname()[1]
        cfg = GatewayConfig()
        cfg.platforms[Platform.API_SERVER] = PlatformConfig(enabled=True, extra={"port": port})
        monkeypatch.setattr(gateway, "load_gateway_config", lambda: cfg)
        monkeypatch.delenv("API_SERVER_PORT", raising=False)
        try:
            # config.yaml port wins over the env default: the busy configured port is what we wait on
            assert gateway._wait_for_api_server_port_free(timeout=0.3) is False
            cfg.platforms[Platform.API_SERVER].enabled = False
            assert gateway._wait_for_api_server_port_free(timeout=0.3) is True
        finally:
            listener.close()


class TestStopProfileGateway:
    def test_stop_profile_gateway_keeps_pid_file_when_process_still_running(self, monkeypatch):
        calls = {"kill": 0, "alive_probes": 0, "remove": 0, "reap_calls": 0}

        monkeypatch.setattr("gateway.status.get_running_pid", lambda: 12345)
        # Post-#21561: the stop loop sends one SIGTERM via ``os.kill`` then
        # polls liveness via ``gateway.status._pid_exists``. Instrument
        # both seams separately.
        monkeypatch.setattr(
            gateway.os,
            "kill",
            lambda pid, sig: calls.__setitem__("kill", calls["kill"] + 1),
        )
        monkeypatch.setattr(
            "gateway.status._pid_exists",
            lambda pid: calls.__setitem__("alive_probes", calls["alive_probes"] + 1) or True,
        )
        monkeypatch.setattr("time.sleep", lambda _: None)
        monkeypatch.setattr(
            "gateway.status.remove_pid_file",
            lambda: calls.__setitem__("remove", calls["remove"] + 1),
        )
        # Mock the orphan reap so it doesn't scan for real gateway processes
        # (#75936 — stop_profile_gateway now calls _reap_unsupervised_gateway_orphans
        # after killing the pid-file PID).
        monkeypatch.setattr(
            gateway,
            "_reap_unsupervised_gateway_orphans",
            lambda extra_exclude=None: calls.__setitem__("reap_calls", calls["reap_calls"] + 1) or False,
        )

        assert gateway.stop_profile_gateway() is True
        assert calls["kill"] == 1          # one SIGTERM
        assert calls["remove"] == 0
        assert calls["reap_calls"] == 1    # orphan sweep ran after kill

    def test_stop_profile_gateway_excludes_killed_pid_from_orphan_reap(self, monkeypatch):
        """The PID we killed must be excluded from the orphan sweep (#75936)."""
        killed_pid = 99999
        reap_extra_excludes = []

        monkeypatch.setattr("gateway.status.get_running_pid", lambda: killed_pid)
        monkeypatch.setattr(gateway.os, "kill", lambda pid, sig: None)
        monkeypatch.setattr("gateway.status._pid_exists", lambda pid: False)
        monkeypatch.setattr("time.sleep", lambda _: None)
        monkeypatch.setattr("gateway.status.remove_pid_file", lambda: None)

        def fake_reap(extra_exclude=None):
            if extra_exclude:
                reap_extra_excludes.append(extra_exclude)
            return False

        monkeypatch.setattr(gateway, "_reap_unsupervised_gateway_orphans", fake_reap)

        assert gateway.stop_profile_gateway() is True
        assert len(reap_extra_excludes) == 1
        assert killed_pid in reap_extra_excludes[0]


@pytest.mark.platforms("macos")
class TestReapUnsupervisedGatewayOrphansMacOS:
    """Tests that the orphan reaper excludes launchd-managed PIDs on macOS.

    Regression guard: without the ``is_macos()`` exclusion of
    ``_get_service_pids()``, the reaper would SIGTERM the launchd-supervised
    gateway every time Hermes Desktop opens (``hermes serve`` calls
    ``_reap_unsupervised_gateway_orphans`` during startup).
    """

    def test_macos_excludes_launchd_pid_from_kill(self, monkeypatch):
        """A launchd-managed PID must not appear in the orphan kill list."""
        launchd_pid = 52615

        monkeypatch.setattr(gateway, "supports_systemd_services", lambda: False)

        # _get_service_pids returns the launchd-managed gateway PID.
        # (accepts all_profiles: the reaper asks for the whole fleet, #74075)
        monkeypatch.setattr(
            gateway, "_get_service_pids", lambda all_profiles=False: {launchd_pid}
        )
        # No pidfile-recorded gateway in this scenario.
        monkeypatch.setattr("gateway.status.get_running_pid", lambda: None)

        # find_gateway_pids returns the launchd PID plus a real orphan.
        # The reaper should only kill the orphan, not the launchd PID.
        orphan_pid = 99998
        monkeypatch.setattr(
            gateway,
            "find_gateway_pids",
            lambda exclude_pids=None: [p for p in [launchd_pid, orphan_pid] if p not in (exclude_pids or set())],
        )

        killed_pids = []
        monkeypatch.setattr(gateway.os, "kill", lambda pid, sig: killed_pids.append((pid, sig)))
        monkeypatch.setattr("gateway.status._pid_exists", lambda pid: False)
        monkeypatch.setattr("gateway.status.write_planned_stop_marker", lambda pid: None)
        monkeypatch.setattr("time.sleep", lambda _: None)
        monkeypatch.setattr("time.monotonic", lambda: 1.0)

        result = gateway._reap_unsupervised_gateway_orphans()

        assert result is True  # at least one orphan was reaped
        killed = [pid for pid, _ in killed_pids]
        assert orphan_pid in killed       # the real orphan was killed
        assert launchd_pid not in killed  # the launchd PID was NOT killed


class TestReaperStartupGrace:
    """``min_age_s`` spares a gateway still claiming gateway.pid/lock (#122533).

    A booting gateway is argv-visible before it is record-visible; reaping it writes
    a planned-stop marker it consumes on startup and exits 0 with no supervisor. An
    undeterminable age must read as too young, never widen the reap.
    """

    def test_grace_spares_booting_and_unknown_age_but_reaps_stale_orphan(self, monkeypatch):
        booting, unknown, stale = 55501, 55502, 99998
        ages = {booting: 2.0, stale: 900.0}

        def _age(pid):
            if pid not in ages:
                raise RuntimeError("probe failed")
            return ages[pid]

        monkeypatch.setattr(gateway, "supports_systemd_services", lambda: False)
        monkeypatch.setattr("gateway.status.get_running_pid", lambda cleanup_stale=True: None)
        monkeypatch.setattr(
            gateway, "find_gateway_pids",
            lambda exclude_pids=None: [p for p in (booting, unknown, stale) if p not in (exclude_pids or set())],
        )
        monkeypatch.setattr("hermes_cli.dashboard_procs._process_age_seconds", _age)
        monkeypatch.setattr(gateway.os, "kill", lambda pid, sig: None)
        monkeypatch.setattr("gateway.status._pid_exists", lambda pid: False)
        monkeypatch.setattr("time.sleep", lambda _: None)
        marked = []
        monkeypatch.setattr("gateway.status.write_planned_stop_marker", marked.append)

        assert gateway._reap_unsupervised_gateway_orphans(min_age_s=180.0) is True
        assert marked == [stale]


def test_find_profile_gateway_processes_strict_propagates_profile_listing_failure(
    monkeypatch,
):
    import hermes_cli.profiles as profiles_mod

    monkeypatch.setattr(
        profiles_mod,
        "list_profiles",
        lambda: (_ for _ in ()).throw(RuntimeError("profile listing failed")),
    )

    with pytest.raises(RuntimeError, match="profile listing failed"):
        gateway.find_profile_gateway_processes(strict=True)

from pathlib import Path


# ---------------------------------------------------------------------------
# Steward-keyed gateway posture: apt-termux sealed installs
#
# A Termux APT package ships a sealed tree with no service manager: the
# service install/uninstall/start lanes refuse (keyed on the steward
# stamp, not a platform probe), while foreground process management
# (stop via the PID registry) keeps working on every install.
# ---------------------------------------------------------------------------


def _apt_termux_tree(tmp_path) -> Path:
    """A sealed (no .git) tree stamped as an apt-termux install."""
    root = tmp_path / "apt-termux"
    root.mkdir(parents=True, exist_ok=True)
    (root / "install-stamp.json").write_text(
        json.dumps({"distribution": "apt-termux"})
    )
    return root


def test_apt_termux_probe_keys_on_steward_stamp(monkeypatch, tmp_path):
    """The probe fires only for a sealed apt-termux tree, by provenance."""
    root = _apt_termux_tree(tmp_path)
    monkeypatch.setattr(gateway, "PROJECT_ROOT", root)
    assert gateway._is_apt_termux_install() is True

    # A git checkout (the repo itself) is not steward-owned: not apt-termux.
    monkeypatch.setattr(gateway, "PROJECT_ROOT", Path(__file__).parent.parent)
    assert gateway._is_apt_termux_install() is False


@pytest.mark.parametrize(
    "cmd_name",
    ["_cmd_install", "_cmd_uninstall", "_cmd_start"],
)
def test_service_commands_refuse_on_sealed_apt_termux(
    monkeypatch, tmp_path, capsys, cmd_name
):
    """Every gated service lane exits 1 with the no-backend message on a
    sealed apt-termux tree -- even without TERMUX env set (the refusal is
    provenance-keyed, not platform-keyed)."""
    import types as _types

    monkeypatch.delenv("TERMUX_VERSION", raising=False)
    monkeypatch.setenv("PREFIX", "/usr/local")  # no termux prefix
    monkeypatch.setattr(gateway, "is_termux", lambda: False)
    monkeypatch.setattr(gateway, "is_managed", lambda: False)
    monkeypatch.setattr(gateway, "_refuse_from_inside_gateway", lambda *a: None)
    monkeypatch.setattr(gateway, "PROJECT_ROOT", _apt_termux_tree(tmp_path))

    with pytest.raises(SystemExit) as exc:
        getattr(gateway, cmd_name)(_types.SimpleNamespace(
            force=False, system=False, run_as_user=None, all=False,
            start_now=None, start_on_login=None, elevated_handoff=False,
        ))
    assert exc.value.code == 1
    out = capsys.readouterr().out
    assert "Termux" in out


@pytest.mark.parametrize("installed", [True, False])
def test_install_if_missing_only_installs_when_no_service_exists(monkeypatch, installed):
    """The installer's gateway stage runs after setup, which may have installed
    the service already. --if-missing must not ask the install questions again."""
    installs = []
    monkeypatch.setattr(gateway, "is_managed", lambda: False)
    monkeypatch.setattr(gateway, "_is_service_installed", lambda: installed)
    monkeypatch.setattr(gateway, "_guard_named_profile_under_multiplexer", lambda force: None)
    monkeypatch.setattr(gateway, "_service_mgmt_blocked", lambda: False)
    monkeypatch.setattr(gateway, "_service_backend", lambda: "launchd")
    monkeypatch.setattr(gateway, "launchd_install", lambda force, start_now: installs.append(force))

    gateway._cmd_install(SimpleNamespace(if_missing=True, force=False, system=False, run_as_user=None))

    assert installs == ([] if installed else [False])
