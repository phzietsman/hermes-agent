"""The /api/pty WebSocket handler binds the fcntl/termios PTY bridge at import time.

It must expose ``PtyBridge`` / ``PtyUnavailableError`` and set ``_PTY_BRIDGE_AVAILABLE``,
without ever raising at import when ``ptyprocess`` is missing — the dashboard's non-chat
tabs must keep loading.
"""

from __future__ import annotations

import hermes_cli.web_server_chat as _web_server_chat


def test_web_server_uses_the_pty_bridge():
    from hermes_cli.pty_bridge import PtyBridge, PtyUnavailableError

    assert _web_server_chat.PtyBridge is PtyBridge
    assert _web_server_chat._PTY_BRIDGE_AVAILABLE is True
    assert _web_server_chat.PtyUnavailableError is PtyUnavailableError
