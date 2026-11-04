from __future__ import annotations

import logging
from pathlib import Path
from typing import Any

from .config import load_settings
from .outbox import Outbox

logger = logging.getLogger(__name__)

# on_skill_lifecycle actions (tools/skill_usage.py) -> what the pointer in Agent Memory should do.
_WRITE = frozenset({"created", "patched", "edited", "restored"})


def pointer_text(name: str, description: str) -> str:
    """Recalled hits matching this prefix become the "Relevant skills" hint (provider._SKILL_HIT)."""
    return f"Skill '{name}': {description.strip()}"


def _description(skill_name: str) -> str:
    from agent.skill_utils import parse_frontmatter  # pyright: ignore[reportUnknownVariableType]
    from tools.skill_manager_tool import _find_skill  # pyright: ignore[reportPrivateUsage, reportUnknownVariableType]

    found: dict[str, Any] | None = _find_skill(skill_name)  # pyright: ignore[reportUnknownVariableType]
    if not found:
        return ""
    frontmatter, _ = parse_frontmatter(Path(found["path"]).joinpath("SKILL.md").read_text(encoding="utf-8"))  # pyright: ignore[reportUnknownVariableType]
    return str(frontmatter.get("description") or "")  # pyright: ignore[reportUnknownMemberType, reportUnknownArgumentType]


def on_skill_lifecycle(*, action: str, skill_name: str, **_: object) -> None:
    """Mirror skill create/patch/archive as a discoverability pointer; skill bodies stay files on disk.

    Writes go to the profile's outbox (durable, replayed by the provider), so this never blocks the turn
    and works for whichever profile fired the hook: no provider instance is shared across profiles."""
    from hermes_constants import get_hermes_home  # pyright: ignore[reportUnknownVariableType]

    home = Path(get_hermes_home())  # pyright: ignore[reportUnknownArgumentType]
    settings, _reason = load_settings(home)
    if settings is None or (action not in _WRITE and action != "archived"):
        return
    outbox = Outbox(home / "agent-memory-outbox.jsonl")
    if action == "archived":
        outbox.append("forget", {"query": f"Skill '{skill_name}':", "purge": False})
        return
    description = _description(skill_name)
    if description:
        outbox.append("remember", {
            "text": pointer_text(skill_name, description),
            "labels": ["kind=skill", f"skill={skill_name}"],
            "category": "instructions",
            "scopes": settings.agent_scope,
        })
