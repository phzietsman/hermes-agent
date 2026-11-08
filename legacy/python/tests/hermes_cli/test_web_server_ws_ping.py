"""Test that start_server configures ws-ping keepalive.

The server now uses uvicorn.Server directly (not uvicorn.run) so we stub
Config + Server + asyncio.run to capture kwargs without starting an event loop.
"""

import asyncio
import contextlib

import pytest
import uvicorn

from hermes_cli import web_server


def _stub_uvicorn(monkeypatch):
    """Replace uvicorn.Config/Server with fakes so start_server returns
    immediately.  Returns a dict with captured Config kwargs."""
    captured: dict = {}

    class _FakeConfig:
        loaded = True
        host = "127.0.0.1"
        port = 8000
        _loop_factory = None

        def __init__(self, *args, **kwargs):
            captured.update(kwargs)

        def load(self):
            pass

        def get_loop_factory(self):
            return self._loop_factory

        class lifespan_class:
            should_exit = False
            state: dict = {}

            def __init__(self, *a, **kw):
                pass

            async def startup(self):
                pass

            async def shutdown(self):
                pass

    class _FakeServer:
        should_exit = False
        started = True
        servers: list = []
        lifespan = None

        @staticmethod
        def capture_signals():
            return contextlib.nullcontext()

        async def startup(self, sockets=None):
            pass

        async def main_loop(self):
            pass

        async def shutdown(self, sockets=None):
            pass

    monkeypatch.setattr(uvicorn, "Config", _FakeConfig)
    monkeypatch.setattr(uvicorn, "Server", lambda config: _FakeServer())
    return captured


def test_start_server_applies_process_local_ssh_bootstrap_state(monkeypatch, tmp_path):
    captured = _stub_uvicorn(monkeypatch)
    owner_watchdogs: list[dict] = []
    monkeypatch.setattr("hermes_cli.web_server_owner_exit.start_owner_watchdog",
                        lambda server, **kw: owner_watchdogs.append(kw))
    lock_path = tmp_path / "backend.lock.json"

    def serve(**extra):
        web_server.start_server(
            host="127.0.0.1",
            port=0,
            open_browser=False,
            ssh_session_token="s" * 64,
            ssh_owner_nonce="0123456789abcdef",
            **extra,
        )

    serve(ssh_lock_path=lock_path)

    assert web_server._SESSION_TOKEN == "s" * 64
    assert web_server._SSH_OWNER_NONCE == "0123456789abcdef"
    assert captured["port"] == 0
    # The Desktop's ownership lock arms supersession retirement (#132034); no lock path, no watchdog.
    assert owner_watchdogs == [{"lock_path": lock_path, "nonce": "0123456789abcdef"}]
    serve()
    assert len(owner_watchdogs) == 1


def test_start_server_disables_ws_ping_on_loopback(monkeypatch):
    """Loopback binds (the Desktop case) MUST disable uvicorn's protocol-level
    keepalive ping so an event-loop stall can never trigger a false disconnect.

    uvicorn's ws ping runs on the same event loop as agent turns. A single
    synchronous GIL-holding call on a worker thread can starve that loop for
    minutes, so the loop can't process the pong and uvicorn kills an
    otherwise-healthy local connection (#53773 "event loop stalled 226.3s",
    #48445/#50005). On loopback there is no network/proxy path where a
    half-open connection can occur — a dead local client tears the socket down
    with a real FIN/RST that surfaces as WebSocketDisconnect regardless — so
    the ping provides no liveness value and only harms. Assert it is disabled.
    """
    captured = _stub_uvicorn(monkeypatch)

    # Loopback bind => no auth gate, so this reaches the Config constructor.
    web_server.start_server(host="127.0.0.1", port=0, open_browser=False)

    assert captured["ws_ping_interval"] is None
    assert captured["ws_ping_timeout"] is None


def test_start_server_accepts_base64_desktop_attachments_above_preview_limit(monkeypatch):
    """The gateway frame cap must fit the Desktop attachment default after
    base64 expansion and JSON framing; uvicorn's 16 MiB default would reject
    the request before ``file.attach`` can stage it.
    """
    captured = _stub_uvicorn(monkeypatch)

    web_server.start_server(host="127.0.0.1", port=0, open_browser=False)

    raw_attachment_bytes = 256 * 1024 * 1024
    base64_bytes = ((raw_attachment_bytes + 2) // 3) * 4
    assert captured["ws_max_size"] > base64_bytes


def test_start_server_enables_ws_ping_for_half_open_detection(monkeypatch):
    """Non-loopback (public) binds MUST keep the ws ping enabled so half-open
    connections (reverse-proxy 524, dropped Cloudflare Tunnel) raise
    WebSocketDisconnect into the reaping path (#32377).

    The invariant asserted here is that ping stays enabled (non-None, positive)
    and the timeout is never shorter than the interval — not a frozen literal,
    which churns every time the window is retuned. Loopback disables the ping
    (see test_start_server_disables_ws_ping_on_loopback); this covers the
    public-bind half-open case, so the auth gate is active here.
    """
    captured = _stub_uvicorn(monkeypatch)

    # Non-loopback bind so the _is_loopback branch selects the enabled-ping
    # window. Neutralize the auth gate so start_server reaches uvicorn.Config
    # without requiring a registered provider (a real public bind would raise
    # SystemExit here). The ping window keys off the host, not the auth flag.
    monkeypatch.setattr(web_server, "should_require_auth", lambda *a, **k: False)
    web_server.start_server(host="0.0.0.0", port=0, open_browser=False)

    assert captured["ws_ping_interval"] and captured["ws_ping_interval"] > 0
    assert captured["ws_ping_timeout"] and captured["ws_ping_timeout"] > 0
    assert captured["ws_ping_timeout"] >= captured["ws_ping_interval"]


def test_start_server_treats_posix_keyboardinterrupt_as_clean_shutdown(monkeypatch):
    """Ctrl+C is the normal foreground-dashboard shutdown path.

    Uvicorn re-raises captured SIGINT as ``KeyboardInterrupt`` after it has
    restored the original signal handlers.  The dashboard should treat that as a
    clean user-requested shutdown instead of leaking a traceback to the terminal.
    """
    _stub_uvicorn(monkeypatch)

    def _raise_keyboard_interrupt(coro):
        coro.close()
        raise KeyboardInterrupt

    monkeypatch.setattr(asyncio, "run", _raise_keyboard_interrupt)

    # Catch rather than let it escape: pytest treats a propagating
    # KeyboardInterrupt as a session abort, not a test failure, so a
    # regression here would kill the run instead of reporting red.
    try:
        web_server.start_server(host="127.0.0.1", port=0, open_browser=False)
    except KeyboardInterrupt:
        pytest.fail(
            "start_server must treat serve-time KeyboardInterrupt as a clean "
            "shutdown, not propagate it"
        )


def test_start_server_bounds_graceful_shutdown_within_desktop_kill_budget(monkeypatch):
    """uvicorn's default timeout_graceful_shutdown=None waits on lingering
    ASGI tasks forever, so the single SIGTERM Desktop sends (no second signal
    to force-exit) hangs the backend until it is orphaned past the desktop's
    ~5s SIGKILL budget (#76244). The configured bound must be positive and
    stay under that kill budget.
    """
    captured = _stub_uvicorn(monkeypatch)

    web_server.start_server(host="127.0.0.1", port=0, open_browser=False)

    assert 0 < captured["timeout_graceful_shutdown"] < 5


def test_shutdown_returns_when_lingering_task_never_completes(monkeypatch):
    """Behavior-level #76244 regression: with the production Config kwargs, a
    live ASGI task that never finishes (a mid-turn request) must not make
    Server.shutdown() hang — the orphan forensics (sockets closed, worker
    threads parked, event loop never returning) is exactly that unbounded
    task wait. shutdown() must honour the grace window, then cancel the
    lingerer and return instead of waiting forever.
    """
    # Real classes grabbed before the stub replaces them (monkeypatch patches
    # uvicorn.Config/Server for the whole test, which is exactly how the
    # production kwargs get captured without binding a port).
    real_config, real_server = uvicorn.Config, uvicorn.Server
    captured = _stub_uvicorn(monkeypatch)
    web_server.start_server(host="127.0.0.1", port=0, open_browser=False)
    bound = captured.get("timeout_graceful_shutdown")
    assert bound is not None, "graceful shutdown must be bounded (#76244)"

    async def _linger():
        await asyncio.Event().wait()

    async def scenario():
        # Real uvicorn objects built from the production kwargs. The ASGI app
        # is unused on the task-wait path and lifespan="off" isolates the
        # lifespan handshake (which needs a started server), keeping the test
        # focused on the task-wait bound.
        config = real_config(None, lifespan="off", **captured)
        server = real_server(config)
        # startup() owns the servers list; empty mirrors the #76244 forensic
        # state (sockets already closed, only the lingering task remains).
        # LifespanOff makes the lifespan shutdown a no-op: its handshake needs
        # a started server and is not what this test is about.
        from uvicorn.lifespan.off import LifespanOff

        server.servers = []
        server.lifespan = LifespanOff(config)
        lingerer = asyncio.ensure_future(_linger())
        server.server_state.tasks.add(lingerer)
        loop = asyncio.get_running_loop()
        start = loop.time()
        # Pre-fix this never returns (timeout=None) until wait_for aborts.
        await asyncio.wait_for(server.shutdown(), timeout=bound + 5)
        # shutdown() requests the cancel on timeout; the CancelledError is
        # delivered on the lingerer's next loop pass, so yield once before
        # asserting the cancellation actually landed.
        with contextlib.suppress(asyncio.CancelledError):
            await lingerer
        elapsed = loop.time() - start
        assert elapsed >= bound, "the grace window must be honoured, not skipped"
        assert elapsed < bound + 5, "shutdown must be bounded by the grace timeout"
        assert lingerer.cancelled(), "the lingering task must be cancelled, not leaked"

    asyncio.run(scenario())
