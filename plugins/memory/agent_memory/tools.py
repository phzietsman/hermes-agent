from __future__ import annotations

import json
from typing import Any, Literal, TypedDict

from .client import MemoryClient

Kind = Literal["fact", "preference", "instruction", "lesson"]

# SAM accepts only identity | knowledge | context. Who the user is and how they want the agent to behave is
# identity; learned facts and lessons are knowledge.
_CATEGORY: dict[str, str | None] = {"fact": "knowledge", "preference": "identity", "instruction": "identity",
                                    "lesson": "knowledge"}


class ToolSchema(TypedDict):
    name: str
    description: str
    parameters: dict[str, Any]


def _schema(name: str, description: str, props: dict[str, Any], required: list[str]) -> ToolSchema:
    return {"name": name, "description": description,
            "parameters": {"type": "object", "properties": props, "required": required}}


SCHEMAS: list[ToolSchema] = [
    _schema("agent_memory_recall", "Search long-term memory for facts, preferences and past lessons.",
            {"query": {"type": "string"}, "k": {"type": "integer", "description": "Max results (default 5)."}},
            ["query"]),
    _schema("agent_memory_remember",
            "Save a durable fact, user preference, standing instruction or lesson learned. Procedures belong "
            "in skills, not here.",
            {"text": {"type": "string"},
             "kind": {"type": "string", "enum": ["fact", "preference", "instruction", "lesson"]}},
            ["text"]),
    _schema("agent_memory_forget", "Forget memories matching a description. Soft by default.",
            {"query": {"type": "string"}, "purge": {"type": "boolean", "description": "Hard delete."}}, ["query"]),
    _schema("agent_memory_reflect", "Reflect over memory to surface patterns on a question.",
            {"query": {"type": "string"}, "persist": {"type": "boolean"}}, ["query"]),
]
TOOL_NAMES = frozenset(s["name"] for s in SCHEMAS)


def remember_args(text: str, kind: str, origin: str) -> dict[str, Any]:
    return {"text": text, "labels": [f"kind={kind}", f"origin={origin}"], "category": _CATEGORY.get(kind)}


def result(payload: dict[str, Any]) -> str:
    return json.dumps({"success": True, "result": payload}, default=str)


def error(message: str) -> str:
    return json.dumps({"error": message, "provider": "agent_memory"})


def recall(client: MemoryClient, args: dict[str, Any], default_k: int, lens: list[str]) -> str:
    hits = client.recall(str(args["query"]), k=int(args.get("k") or default_k), lens=lens).hits
    return result({"hits": [{"text": h.text, "score": h.score, "id": h.id} for h in hits]})


def reflect(client: MemoryClient, args: dict[str, Any]) -> str:
    return result(client.reflect(str(args["query"]), persist=bool(args.get("persist", False))))
