"""User-initiated edit/delete for journey nodes (learned skills).

Node ids (from ``agent.learning_graph``) are skill names. Memory entries live in Agent Memory and are
not journey nodes; a ``memory:`` id is refused with a pointer to ``hermes agent_memory``.
Shared by CLI ``hermes journey``, the TUI ``/journey`` overlay and the desktop.
Deleting a skill *archives* it (``hermes curator restore`` recovers it).
"""

from __future__ import annotations

from pathlib import Path
from typing import Any, Callable

_MEMORY_REFUSAL = {"ok": False, "message": "memory entries are managed by Agent Memory (hermes agent_memory), "
                                           "not the journey graph"}


def parse_node_kind(node_id: str) -> str:
    return "memory" if node_id.startswith("memory:") else "skill"


def _clear_skill_cache() -> None:
    try:
        from agent.prompt_builder import clear_skills_system_prompt_cache
        clear_skills_system_prompt_cache(clear_snapshot=True)
    except Exception:
        pass


def _dispatch(node_id: str, skill_fn: Callable[..., dict[str, Any]], *args: Any) -> dict[str, Any]:
    if parse_node_kind(node_id) == "memory":
        return dict(_MEMORY_REFUSAL)
    return skill_fn(node_id, *args)


# ── Inspect (edit prefill) ──────────────────────────────────────────────────

def node_detail(node_id: str) -> dict[str, Any]:
    """Current content for an edit prefill: the full SKILL.md."""
    return _dispatch(node_id, _skill_detail)


def _skill_detail(node_id: str) -> dict[str, Any]:
    from tools.skill_manager_tool import _find_skill
    found = _find_skill(node_id)
    if not found:
        return {"ok": False, "message": f"skill '{node_id}' not found"}
    skill_md = Path(found["path"]) / "SKILL.md"
    if not skill_md.exists():
        return {"ok": False, "message": f"SKILL.md missing for '{node_id}'"}
    return {"ok": True, "kind": "skill", "id": node_id, "label": node_id, "content": skill_md.read_text(encoding="utf-8-sig")}


def delete_node(node_id: str) -> dict[str, Any]:
    return _dispatch(node_id, _delete_skill)


def _delete_skill(name: str) -> dict[str, Any]:
    from tools import skill_usage
    # Pin must be respected by autonomous maintenance. The curator already skips pinned skills from every
    # auto-transition; the background review fork is the same kind of autonomous, no-user-present actor, so
    # it must not write to a pinned skill either (issue #25839). This is stricter than the foreground
    # ``_pinned_guard`` (which only blocks deletion) precisely because there is no user in the loop to
    # consent to an edit here.
    if skill_usage.get_record(name).get("pinned"):
        return {"ok": False, "message": f"'{name}' is pinned — unpin it first (hermes curator unpin {name})"}
    ok, message = skill_usage.archive_skill(name)
    if ok:
        _clear_skill_cache()
    return {"ok": ok, "message": f"archived '{name}' — restore with: hermes curator restore {name}" if ok else message}


# ── Edit ────────────────────────────────────────────────────────────────────

def edit_node(node_id: str, content: str) -> dict[str, Any]:
    return _dispatch(node_id, _edit_skill, content)


def _edit_skill(name: str, content: str) -> dict[str, Any]:
    from tools.skill_manager_tool import _edit_skill as _do_edit
    result = _do_edit(name, content)
    if result.get("success"):
        _clear_skill_cache()
        return {"ok": True, "message": f"updated '{name}'"}
    return {"ok": False, "message": result.get("error", "edit failed")}
