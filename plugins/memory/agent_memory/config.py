from __future__ import annotations

import json
import logging
import os
import tempfile
from enum import Enum
from pathlib import Path
from typing import Any

from pydantic import BaseModel, ConfigDict, Field, HttpUrl, SecretStr, ValidationError

logger = logging.getLogger(__name__)

CONFIG_FILENAME = "agent-memory.json"
API_KEY_ENV = "AGENT_MEMORY_API_KEY"


class RecallMode(str, Enum):
    HYBRID = "hybrid"  # recalled hits injected every turn
    CONTEXT = "context"  # SAM-synthesised context injected every turn
    TOOLS = "tools"  # nothing injected; the model calls agent_memory_recall


class AgentMemorySettings(BaseModel):
    """Everything is explicit: the SDK reads no environment variables."""

    model_config = ConfigDict(frozen=True, extra="forbid")

    endpoint: HttpUrl
    context: str = Field(min_length=1)
    api_key: SecretStr
    default_scope: str = "user/default"
    agent_scope: str = "agent/hermes"
    recall_mode: RecallMode = RecallMode.HYBRID
    top_k: int = Field(5, ge=1, le=20)
    max_prefetch_chars: int = Field(4000, ge=500, le=20000)
    core_profile_max_chars: int = Field(1500, ge=200, le=6000)
    # Must stay under MemoryManager's 8s prefetch timeout.
    timeout_s: float = Field(6.0, gt=0, lt=8.0)
    max_retries: int = Field(2, ge=0, le=5)
    breaker_threshold: int = Field(3, ge=1)
    send_tool_outputs: bool = False
    consolidate_on_end: bool = True
    replay_every_n_turns: int = Field(10, ge=1)


def _secret(name: str) -> str:
    # Scoped lookup: under multiplexing a plain os.environ read would leak the launch profile's key.
    from agent.secret_scope import get_secret_str  # pyright: ignore[reportUnknownVariableType]

    return str(get_secret_str(name))  # pyright: ignore[reportUnknownArgumentType]


def config_path(hermes_home: Path) -> Path:
    return hermes_home / CONFIG_FILENAME


def read_config_file(hermes_home: Path) -> dict[str, Any]:
    try:
        data = json.loads(config_path(hermes_home).read_text(encoding="utf-8"))
    except (FileNotFoundError, ValueError, OSError):
        return {}
    return data if isinstance(data, dict) else {}  # pyright: ignore[reportUnknownVariableType]


def load_settings(hermes_home: Path) -> tuple[AgentMemorySettings | None, str]:
    """(settings, "") when usable, else (None, reason). No network."""
    values = read_config_file(hermes_home)
    key = _secret(API_KEY_ENV)
    if not key:
        return None, f"{API_KEY_ENV} is not set"
    if not values.get("endpoint") or not values.get("context"):
        return None, f"endpoint and context are required in {config_path(hermes_home)}"
    try:
        return AgentMemorySettings.model_validate({**values, "api_key": key}), ""
    except ValidationError as exc:
        fields = ", ".join(str(e["loc"][0]) for e in exc.errors())
        return None, f"invalid agent-memory settings: {fields}"


_PERSISTED = tuple(n for n in AgentMemorySettings.model_fields if n != "api_key")


def save_config_file(values: dict[str, Any], hermes_home: Path) -> Path:
    """Persist non-secret settings atomically; the API key never touches this file."""
    path = config_path(hermes_home)
    path.parent.mkdir(parents=True, exist_ok=True)
    merged = read_config_file(hermes_home)
    merged.update({k: v for k, v in values.items() if k in _PERSISTED and v not in (None, "")})
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=".agent-memory-", suffix=".json")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            json.dump(merged, fh, indent=2, sort_keys=True)
        os.replace(tmp, path)
        os.chmod(path, 0o600)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)
    return path


def config_schema() -> list[dict[str, Any]]:
    """Fields for `hermes memory setup`."""
    return [
        {"key": "api_key", "description": "Agent Memory API key", "secret": True, "required": True,
         "env_var": API_KEY_ENV, "url": "https://surrealdb.com/agent-memory"},
        {"key": "endpoint", "description": "Agent Memory endpoint, e.g. https://your-instance.agent-memory.dev",
         "required": True},
        {"key": "context", "description": "Agent Memory context this agent's memory is pinned to", "required": True},
        {"key": "default_scope", "description": "Scope for user memories, e.g. user/alice", "default": "user/default"},
        {"key": "recall_mode", "description": "How memory is surfaced each turn", "default": "hybrid",
         "choices": [m.value for m in RecallMode]},
        {"key": "top_k", "description": "Memories recalled per turn", "default": 5, "type": "integer",
         "minimum": 1, "maximum": 20},
    ]
