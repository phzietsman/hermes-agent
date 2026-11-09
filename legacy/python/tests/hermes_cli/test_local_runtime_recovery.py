"""Recovery only targets the recorded managed router, never a process-name sweep."""
from __future__ import annotations

import json
import os
from pathlib import Path
import subprocess
import sys
import time

import psutil
import pytest


def _wait_for(predicate, timeout=10):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.05)
    assert predicate(), "process transition did not complete"


@pytest.mark.parametrize("failure", ["arrival", "truncated"])
def test_startup_reuses_without_activity_probe(tmp_path, monkeypatch, failure):
    import http.client
    from hermes_cli.local_runtime import bootstrap, endpoint, recovery

    monkeypatch.setattr(bootstrap, "_SUPERVISOR", None)
    monkeypatch.setattr(bootstrap, "staged_models", lambda: [tmp_path / "model.gguf"])
    monkeypatch.setattr(bootstrap, "_presets_stale", lambda: False)
    state = {"base_url": "http://127.0.0.1:59999/v1", "api_key": "test-only"}
    monkeypatch.setattr(endpoint, "_state_endpoint", lambda: state)
    calls = []
    def probe(*args, **kwargs):
        calls.append(True)
        if failure == "truncated":
            raise http.client.IncompleteRead(b"partial", 100)
        return {"data": []}  # work arrives immediately after an idle snapshot
    monkeypatch.setattr(endpoint, "managed_get_json", probe)
    monkeypatch.setattr(recovery, "read_state", lambda: state)
    from types import SimpleNamespace
    monkeypatch.setattr(recovery, "recorded_process",
                        lambda state: SimpleNamespace(is_running=lambda: False))
    monkeypatch.setattr(recovery, "_owner_is_dead", lambda state: True)
    for _ in range(2):
        assert bootstrap.ensure_local_runtime({"local_runtime": {"enabled": True}}) is None
    assert not calls


def test_shutdown_during_backoff_cannot_restart_or_remove_another_server(tmp_path, monkeypatch):
    from types import SimpleNamespace
    from hermes_cli.local_runtime import supervisor

    monkeypatch.setattr(supervisor, "runtimes_root", lambda: tmp_path)
    sup = supervisor.LlamaServerSupervisor(tmp_path, tmp_path, port=59998)
    sup.proc = SimpleNamespace(pid=101, poll=lambda: 1)
    other = {"pid": 202, "base_url": "http://127.0.0.1:59997/v1", "api_key": "test-only"}
    supervisor.state_path().write_text(json.dumps(other))
    spawned = []
    monkeypatch.setattr(sup, "_spawn", lambda: spawned.append(True))
    monkeypatch.setattr(sup, "_wait_health", lambda *a: None)
    monkeypatch.setattr(sup, "_reap_orphaned_children", lambda: None)
    monkeypatch.setattr(supervisor.time, "sleep", lambda *a: sup.stop())
    # A real Event.wait is independently exercised by the native lifetime tests.
    if hasattr(sup, "_stop_event"):
        monkeypatch.setattr(sup._stop_event, "wait", lambda *a: (sup.stop() or True))
    sup._watch()
    assert not spawned, "stop during restart backoff resurrected the runtime"
    assert json.loads(supervisor.state_path().read_text()) == other


def test_spawn_state_records_process_incarnations(tmp_path, monkeypatch):
    import os
    from hermes_cli.local_runtime import supervisor

    monkeypatch.setattr(supervisor, "runtimes_root", lambda: tmp_path)
    sup = supervisor.LlamaServerSupervisor(tmp_path, tmp_path, port=59998)
    proc = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"])
    try:
        sup.proc = proc
        sup._write_state()
        state = json.loads(supervisor.state_path().read_text())
        assert state.get("create_time") == psutil.Process(proc.pid).create_time()
        assert state.get("owner_pid") == os.getpid()
        assert state.get("owner_create_time") == psutil.Process().create_time()
        assert state.get("executable") == psutil.Process(proc.pid).exe()
    finally:
        proc.kill()
        proc.wait(timeout=5)


def test_stopped_state_is_retained_without_unlink_race(tmp_path, monkeypatch):
    from types import SimpleNamespace
    from hermes_cli.local_runtime import supervisor

    monkeypatch.setattr(supervisor, "runtimes_root", lambda: tmp_path)
    sup = supervisor.LlamaServerSupervisor(tmp_path, tmp_path, port=59998)
    sup.proc = SimpleNamespace(pid=101, poll=lambda: 1)
    sup._state = {"pid": 101}
    path = supervisor.state_path()
    path.write_text(json.dumps(sup._state))
    replacement = {"pid": os.getpid(), "base_url": "http://127.0.0.1:59997/v1", "api_key": "test-only"}
    unlink = Path.unlink
    def publish_before_unlink(self, *args, **kwargs):
        if self == path:
            self.write_text(json.dumps(replacement))
        return unlink(self, *args, **kwargs)
    with monkeypatch.context() as m:
        m.setattr(Path, "unlink", publish_before_unlink)
        sup.stop()
    assert json.loads(path.read_text()) == sup._state
    path.write_text(json.dumps(replacement))
    sup.stop()
    assert json.loads(path.read_text()) == replacement


@pytest.mark.parametrize("kind", ["psutil", "subprocess", "missing-psutil", "wait-error"])
def test_terminate_tree_escalates_and_always_cleans_children(monkeypatch, kind):
    from types import SimpleNamespace
    from unittest.mock import Mock
    from hermes_cli.local_runtime.supervisor import LlamaServerSupervisor

    child = Mock()
    child.is_running.return_value = True
    proc = Mock(pid=123)
    error = (psutil.TimeoutExpired(15) if kind == "psutil" else
             RuntimeError("wait failed") if kind == "wait-error" else
             subprocess.TimeoutExpired("router", 15))
    proc.wait.side_effect = error
    if kind == "missing-psutil":
        monkeypatch.setitem(sys.modules, "psutil", None)
    else:
        monkeypatch.setattr(psutil, "Process", lambda pid: SimpleNamespace(children=lambda **kw: [child]))
    if kind == "wait-error":
        with pytest.raises(RuntimeError, match="wait failed"):
            LlamaServerSupervisor._terminate_tree(proc)
    else:
        LlamaServerSupervisor._terminate_tree(proc)
        proc.kill.assert_called_once()
    if kind != "missing-psutil":
        child.terminate.assert_called_once()
        child.kill.assert_called_once()


@pytest.mark.parametrize("reuse_at", ["before-walk", "during-walk", "never"])
def test_explicit_stop_preserves_verified_root_incarnation(tmp_path, monkeypatch, reuse_at):
    from unittest.mock import Mock
    from hermes_cli.local_runtime import recovery, supervisor

    state = {"pid": 123, "create_time": 1.0}
    path = tmp_path / "server.json"
    path.write_text(json.dumps(state))
    monkeypatch.setattr(supervisor, "state_path", lambda: path)
    root = Mock(spec=psutil.Process, pid=123)
    owned_child = Mock(spec=psutil.Process)
    replacement_child = Mock(spec=psutil.Process)
    replacement = Mock(spec=psutil.Process, pid=123)
    replacement.children.return_value = [replacement_child]
    replacement_child.is_running.return_value = True
    owned_child.is_running.return_value = True
    stale = False

    def final_identity_check():
        nonlocal stale
        if stale:
            return False
        # The verified root exits and its PID is reused just after this check.
        stale = reuse_at == "before-walk"
        return True

    def verified_children(*, recursive):
        nonlocal stale
        assert recursive is True
        if stale:
            raise psutil.NoSuchProcess(root.pid)
        if reuse_at == "during-walk":
            stale = True
            return [replacement_child]
        return [owned_child]

    def verified_terminate():
        if stale:
            raise psutil.NoSuchProcess(root.pid)

    root.is_running.side_effect = final_identity_check
    root.children.side_effect = verified_children
    root.terminate.side_effect = verified_terminate
    factory = Mock(side_effect=lambda pid: replacement if stale else root)
    monkeypatch.setattr(psutil, "Process", factory)
    monkeypatch.setattr(psutil, "pid_exists", lambda pid: True)
    monkeypatch.setattr(recovery, "recorded_process", lambda record: root)
    monkeypatch.setattr(recovery, "_owner_is_dead", lambda record: True)

    stopped = recovery.stop_recorded_orphan()

    replacement_child.terminate.assert_not_called()
    replacement_child.kill.assert_not_called()
    replacement.terminate.assert_not_called()
    replacement.kill.assert_not_called()
    root.children.assert_called_once_with(recursive=True)
    factory.assert_not_called()
    assert stopped is (reuse_at == "never")
    if reuse_at != "never":
        root.terminate.assert_not_called()
        root.kill.assert_not_called()
        owned_child.terminate.assert_not_called()
        owned_child.kill.assert_not_called()
    else:
        root.terminate.assert_called_once_with()
        owned_child.terminate.assert_called_once_with()
        owned_child.kill.assert_called_once_with()


@pytest.mark.platforms("linux")
def test_reparented_router_keeps_its_endpoint(tmp_path, monkeypatch):
    from hermes_cli.local_runtime import endpoint, supervisor

    monkeypatch.setattr(supervisor, "runtimes_root", lambda: tmp_path)
    read_fd, write_fd = os.pipe()
    # EOF ends the test child without signaling outside the test's subtree after reparenting.
    with os.fdopen(write_fd, "wb") as control:
        try:
            owner = subprocess.Popen([sys.executable, "-c", """
import json, os, psutil, subprocess, sys
p = subprocess.Popen([sys.executable, '-c', 'import sys; sys.stdin.buffer.read(1)'],
                     stdin=int(sys.argv[1]), stdout=subprocess.DEVNULL)
proc = psutil.Process(p.pid)
print(json.dumps({'pid': proc.pid, 'create_time': proc.create_time(), 'executable': proc.exe(),
                  'owner_pid': os.getpid(), 'owner_create_time': psutil.Process().create_time()}), flush=True)
""", str(read_fd)], pass_fds=(read_fd,), stdout=subprocess.PIPE, text=True)
        finally:
            os.close(read_fd)
        state = json.loads(owner.stdout.readline())
        proc = psutil.Process(state["pid"])
        try:
            owner.wait(timeout=10)
            route = {"base_url": "http://127.0.0.1:59999/v1", "api_key": "test-only"}
            supervisor.state_path().write_text(json.dumps({**state, **route}))
            assert proc.ppid() != state["owner_pid"]
            assert endpoint._state_endpoint() == route
        finally:
            control.close()
            proc.wait(timeout=10)
            owner.stdout.close()


@pytest.mark.platforms("linux")
def test_clock_step_keeps_the_recorded_router(tmp_path, monkeypatch):
    """psutil's create_time moves with /proc/stat's boot time when the clock is stepped (WSL
    re-syncs within minutes); the live router must still resolve, and a reused PID must not."""
    import psutil._pslinux
    from hermes_cli.local_runtime import endpoint, supervisor

    monkeypatch.setattr(supervisor, "runtimes_root", lambda: tmp_path)
    sup = supervisor.LlamaServerSupervisor(tmp_path, tmp_path, port=59998)
    proc = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"])
    try:
        sup.proc = proc
        sup._write_state()
        stepped = psutil._pslinux.boot_time() + 22
        monkeypatch.setattr(psutil._pslinux, "boot_time", lambda: stepped)
        assert endpoint._state_endpoint() == {"base_url": sup.base_url, "api_key": sup.api_key}

        state = json.loads(supervisor.state_path().read_text())
        supervisor.state_path().write_text(json.dumps({**state, "start_time": state["start_time"] - 1000}))
        assert endpoint._state_endpoint() is None
    finally:
        proc.kill()
        proc.wait(timeout=5)


@pytest.mark.platforms("posix")
@pytest.mark.parametrize("owner", ["exited", "alive"])
def test_replacement_stops_only_an_orphaned_router(tmp_path, monkeypatch, owner):
    """Stale-preset replacement and the rescan bounce stop the recorded router only when its owner
    is gone; a live owner's router keeps running (its watchdog would respawn it anyway). POSIX: a
    Windows router dies with its owner's job object, so no orphan exists to stop."""
    from gateway.status import get_process_start_time
    from hermes_cli.local_runtime import bootstrap, endpoint, supervisor

    monkeypatch.setattr(supervisor, "runtimes_root", lambda: tmp_path)
    holder = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"])
    router = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"])
    try:
        holder_proc, router_proc = psutil.Process(holder.pid), psutil.Process(router.pid)
        supervisor.state_path().write_text(json.dumps({
            "base_url": "http://127.0.0.1:59997/v1", "api_key": "test-only",
            "pid": router.pid, "create_time": router_proc.create_time(),
            "start_time": get_process_start_time(router.pid), "executable": router_proc.exe(),
            "owner_pid": holder.pid, "owner_create_time": holder_proc.create_time(),
            "owner_start_time": get_process_start_time(holder.pid)}))
        if owner == "exited":
            holder.kill()
            holder.wait(timeout=5)

        bootstrap._stop_state_server()

        if owner == "exited":
            router.wait(timeout=10)
            assert endpoint._state_endpoint() is None
        else:
            assert router.poll() is None
    finally:
        for proc in (holder, router):
            if proc.poll() is None:
                proc.kill()
                proc.wait(timeout=5)


@pytest.mark.platforms("linux")
def test_a_stepped_clock_never_buries_a_live_owner_of_an_older_record(monkeypatch):
    """Records written before start_time compare create_time, which moves with a stepped clock
    (WSL). Only a newer incarnation proves the owner exited; a live owner must never read as
    dead, or a newer process would stop the server an older one is still using."""
    import psutil._pslinux
    from hermes_cli.local_runtime import recovery

    me = psutil.Process()
    state = {"owner_pid": me.pid, "owner_create_time": me.create_time()}
    stepped = psutil._pslinux.boot_time() - 22
    monkeypatch.setattr(psutil._pslinux, "boot_time", lambda: stepped)
    assert psutil.Process(me.pid).create_time() != state["owner_create_time"]

    assert recovery._owner_is_dead(state) is False


def test_the_process_that_boots_the_server_stops_it_on_a_clean_exit(tmp_path):
    """Every surface that boots the managed server (CLI, gateway, desktop backend) frees it when it
    exits; before, only the desktop backend's shutdown handler did and every other owner orphaned
    the router and its VRAM."""
    marker = tmp_path / "stopped"
    code = (
        "import sys\n"
        "from hermes_cli.local_runtime import bootstrap\n"
        "class Sup:\n"
        f"    def stop(self): open({str(marker)!r}, 'w').close()\n"
        "bootstrap._SUPERVISOR = Sup()\n"
        "bootstrap._stop_at_exit()\n"
        "sys.exit(0)\n"
    )
    subprocess.run([sys.executable, "-c", code], cwd=Path(__file__).resolve().parents[2], check=True, timeout=60)
    assert marker.exists()


