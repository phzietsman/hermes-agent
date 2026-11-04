"""`hermes agent_memory ...` (wired by plugins.memory.discover_plugin_cli_commands for the active provider)."""

from __future__ import annotations

import argparse
import sys
from collections.abc import Callable
from pathlib import Path

from .client import MemoryClient, MemoryUnavailable, SurrealMemoryAdapter
from .config import AgentMemorySettings, config_path, load_settings
from .outbox import Outbox, apply_entry

ClientFactory = Callable[[AgentMemorySettings], MemoryClient]


def _home() -> Path:
    from hermes_constants import get_hermes_home  # pyright: ignore[reportUnknownVariableType]

    return Path(get_hermes_home())  # pyright: ignore[reportUnknownArgumentType]


def _connect(factory: ClientFactory) -> tuple[MemoryClient, AgentMemorySettings] | None:
    settings, reason = load_settings(_home())
    if settings is None:
        print(f"✗ not configured: {reason}")
        return None
    return factory(settings), settings


def cmd_status(factory: ClientFactory = SurrealMemoryAdapter) -> int:
    home = _home()
    settings, reason = load_settings(home)
    print(f"config file : {config_path(home)}")
    if settings is None:
        print(f"configured  : no ({reason})")
        return 1
    print(f"endpoint    : {settings.endpoint}\ncontext     : {settings.context}")
    print(f"scopes      : {settings.default_scope}, {settings.agent_scope}\nrecall mode : {settings.recall_mode.value}")
    print(f"outbox      : {len(Outbox(home / 'agent-memory-outbox.jsonl').pending())} pending write(s)")
    return 0


def cmd_doctor(factory: ClientFactory = SurrealMemoryAdapter) -> int:
    conn = _connect(factory)
    if conn is None:
        return 1
    client, _settings = conn
    ok = True
    for label, probe in (("whoami", client.whoami), ("profile", client.profile), ("recall", lambda: client.recall("ping", k=1))):
        try:
            probe()
            print(f"✓ {label}")
        except MemoryUnavailable as exc:
            ok = False
            print(f"✗ {label}: {exc}")
    client.close()
    return 0 if ok else 1


def cmd_scopes_init(factory: ClientFactory = SurrealMemoryAdapter) -> int:
    """SAM requires scopes to be registered before writes land in them."""
    conn = _connect(factory)
    if conn is None:
        return 1
    client, settings = conn
    for scope in dict.fromkeys((settings.default_scope, settings.agent_scope)):
        client.register_scope(scope)
        print(f"✓ registered {scope}")
    client.close()
    return 0


def cmd_replay(factory: ClientFactory = SurrealMemoryAdapter) -> int:
    conn = _connect(factory)
    if conn is None:
        return 1
    client, _settings = conn
    outbox = Outbox(_home() / "agent-memory-outbox.jsonl")
    before = len(outbox.pending())
    applied = outbox.replay(lambda entry: apply_entry(client, entry))
    client.close()
    print(f"replayed {applied}/{before}; {len(outbox.pending())} left")
    return 0 if applied == before else 1


COMMANDS: dict[str, Callable[[ClientFactory], int]] = {
    "status": cmd_status, "doctor": cmd_doctor, "scopes-init": cmd_scopes_init, "replay": cmd_replay,
}


def register_cli(subparser: argparse.ArgumentParser) -> None:
    sub = subparser.add_subparsers(dest="agent_memory_cmd")
    helps = {"status": "Show configuration and pending writes", "doctor": "Probe the Agent Memory service",
             "scopes-init": "Register the default user and agent scopes", "replay": "Replay queued writes now"}
    for name, text in helps.items():
        sub.add_parser(name, help=text)
    subparser.set_defaults(func=agent_memory_command)


def agent_memory_command(args: argparse.Namespace) -> int:
    name = getattr(args, "agent_memory_cmd", None)
    if name not in COMMANDS:
        print("usage: hermes agent_memory {" + "|".join(COMMANDS) + "}", file=sys.stderr)
        return 2
    return COMMANDS[name](SurrealMemoryAdapter)
