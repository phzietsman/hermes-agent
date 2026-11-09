"""Assemble the "learning made visible" graph for desktop.

Scoped to what a user actually learns over time: non-base, learned/profile
skills (agent-created or used) plus ``MEMORY.md`` / ``USER.md`` chunks as
first-class nodes. Skill links come from declared ``related_skills``;
memory→skill links are derived from lexical overlap.
"""

from __future__ import annotations

import hashlib
import json
import re
from collections import Counter
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Optional

from hermes_constants import get_hermes_home

_SKIP_PARTS = {".archive", ".hub", ".locks", "node_modules", ".git"}
_USAGE_TS_KEYS = ("last_activity_at", "last_used_at", "last_viewed_at", "last_patched_at", "created_at")


@dataclass
class SkillNode:
    name: str
    category: str
    source: str = "profile"
    timestamp: Optional[int] = None
    use_count: int = 0
    state: str = "active"
    created_by: Optional[str] = None
    pinned: bool = False
    related: list[str] = field(default_factory=list)


def _fm_field(fm: dict[str, Any], key: str) -> Any:
    """Top-level ``key`` or ``metadata.hermes.<key>``; tolerant of the string-valued
    frontmatter that ``parse_frontmatter``'s malformed-YAML fallback produces."""
    if fm.get(key):
        return fm[key]
    meta = fm.get("metadata")
    hermes = meta.get("hermes") if isinstance(meta, dict) else None
    return hermes.get(key) if isinstance(hermes, dict) else None


def _related(fm: dict[str, Any]) -> list[str]:
    raw = _fm_field(fm, "related_skills")
    raw = raw.strip("[]").split(",") if isinstance(raw, str) else raw
    return [str(r).strip() for r in raw if str(r).strip()] if isinstance(raw, list) else []


def _load_usage() -> dict[str, dict[str, Any]]:
    try:
        from tools.skill_usage import load_usage
        return load_usage()
    except Exception:
        try:
            return json.loads((get_hermes_home() / "skills" / ".usage.json").read_text(encoding="utf-8-sig"))
        except Exception:
            return {}


def _to_int_ts(value: Any) -> Optional[int]:
    """Epoch seconds from a number, numeric string, or ISO timestamp; None otherwise."""
    try:
        if value is None or not (s := str(value).strip()):
            return None
        if isinstance(value, (int, float)):
            return int(value)
        try:
            return int(float(s))
        except ValueError:
            parsed = datetime.fromisoformat(s.replace("Z", "+00:00"))
            return int((parsed if parsed.tzinfo is not None else parsed.replace(tzinfo=timezone.utc)).timestamp())
    except Exception:
        return None


def build_skill_nodes(skill_roots: list[tuple[str, Path]]) -> dict[str, SkillNode]:
    usage = _load_usage()
    nodes: dict[str, SkillNode] = {}
    # Tag skills mounted from skills.external_dirs so the journey graph can keep them out of
    # learning milestones (#108032): a mount is configured, not learned. Path-based (the common
    # symlink into the profile tree resolves to the external root), so a local copy of the same
    # name still classifies as its own source.
    try:
        from agent.skill_utils import is_external_skill_path
    except Exception:
        is_external_skill_path = None  # type: ignore[assignment]
    for source, root in skill_roots:
        for skill_md in root.rglob("SKILL.md") if root.exists() else ():
            if _SKIP_PARTS.intersection(skill_md.parts):
                continue
            try:
                text = skill_md.read_text(encoding="utf-8-sig")[:4000]
            except OSError:
                continue
            try:
                from agent.skill_utils import parse_frontmatter
                fm = parse_frontmatter(text)[0] or {}
            except Exception:
                fm = {}
            name = str(fm.get("name") or skill_md.parent.name).strip()
            if not name or name in nodes:
                continue
            rec, cat, parts = usage.get(name, {}), _fm_field(fm, "category"), skill_md.parts  # …/skills/<category>/<skill>/SKILL.md
            usage_ts = next((ts for ts in (_to_int_ts(rec.get(k)) for k in _USAGE_TS_KEYS) if ts is not None), None)
            # Local variable: never overwrite `source` — the loop variable must keep its
            # per-root value for the NEXT skill in the same root, or every skill yielded
            # after the first external mount inherits "external" (ext4 hash order can
            # interleave a symlinked mount before a local skill in one root).
            node_source = "external" if (is_external_skill_path is not None and is_external_skill_path(skill_md)) else source
            nodes[name] = SkillNode(
                name=name, category=str(cat) if cat else parts[-3] if len(parts) >= 3 else "general", source=node_source,
                timestamp=usage_ts or _to_int_ts(skill_md.stat().st_mtime),
                use_count=int(rec.get("use_count", 0) or 0), state=str(rec.get("state", "active") or "active"),
                created_by=rec.get("created_by"), pinned=bool(rec.get("pinned", False)), related=_related(fm),
            )
    return nodes


def build_edges(nodes: dict[str, SkillNode]) -> list[tuple[str, str]]:
    """Undirected related_skills edges where BOTH endpoints exist (deduped, first-seen order)."""
    return list(dict.fromkeys(
        (min(node.name, target), max(node.name, target)) for node in nodes.values() for target in node.related if target in nodes and target != node.name
    ))


def density_stats(nodes: dict[str, SkillNode], edges: list[tuple[str, str]]) -> dict[str, Any]:
    linked, cats, n = {x for edge in edges for x in edge}, Counter(x.category for x in nodes.values()), len(nodes) or 1
    return {
        "nodes": len(nodes), "related_edges": len(edges), "edges_per_node": round(len(edges) / n, 3),
        "linked_nodes": len(linked), "isolated_pct": round(100 * (n - len(linked)) / n, 1), "categories": len(cats),
        "agent_created": sum(1 for x in nodes.values() if x.created_by == "agent"),
        "used": sum(1 for x in nodes.values() if x.use_count > 0),
        "top_categories": sorted(cats.items(), key=lambda kv: -kv[1])[:8],
    }


def memory_node_id(card: dict[str, Any], index: int) -> str:
    """``memory:<source>:<index>:<fingerprint>``. Kept for the renderer; the graph no longer has memory
    nodes (memory is in Agent Memory), so nothing mints these ids today."""
    return f"memory:{card['source']}:{index}:{card['fingerprint']}"


def _has_learning_signal(node: SkillNode) -> bool:
    """Graph-worthy: agent-created, user-taught (/learn), or actually used.

    ``created_by="learn"`` is a learning-signal marker only — curator management stays keyed
    strictly on ``"agent"`` (see ``tools.skill_usage._is_curator_managed_record``). External
    mounts are never a learning milestone: they were configured by the user, not learned, so
    even a used external skill stays out of the journey graph (#108032).
    """
    return node.source != "external" and (node.created_by in {"agent", "learn"} or node.use_count > 0)


def build_learning_graph() -> dict[str, Any]:
    """Full payload for the desktop learning panel: non-base skills with real
    learning signal (agent-created or used)."""
    roots = [("base", Path(__file__).resolve().parent.parent / "skills"), ("profile", get_hermes_home() / "skills")]
    learned_skills = {
        name: node for name, node in build_skill_nodes(roots).items()
        if node.source != "base" and _has_learning_signal(node)
    }
    skill_edges = build_edges(learned_skills)
    memory_cards: list[dict[str, Any]] = []  # payload shape kept for clients; memory is not in this graph
    memory_edges: list[tuple[str, str]] = []
    clusters = Counter(node.category for node in learned_skills.values())

    graph_nodes = [
        {
            "id": n.name, "label": n.name, "kind": "skill", "timestamp": n.timestamp, "category": n.category,
            "useCount": n.use_count, "state": n.state, "createdBy": n.created_by, "pinned": n.pinned,
        }
        for n in learned_skills.values()
    ] + [
        {
            "id": memory_node_id(card, i), "label": card["title"], "kind": "memory",
            "memorySource": card["source"], "timestamp": card.get("timestamp"), "category": "memory",
            "useCount": 0, "state": "active", "createdBy": "memory", "pinned": False,
        }
        for i, card in enumerate(memory_cards)
    ]
    return {
        "nodes": graph_nodes,
        "edges": [{"source": a, "target": b} for a, b in skill_edges + memory_edges],
        "clusters": [{"category": c, "count": n} for c, n in sorted(clusters.items(), key=lambda kv: -kv[1])],
        "memory": memory_cards,
        "stats": {
            **density_stats(learned_skills, skill_edges),
            "memory_nodes": len(memory_cards), "memory_skill_edges": len(memory_edges), "learned_skills": len(learned_skills),
        },
    }
