from __future__ import annotations

import hashlib
from collections.abc import Sequence
from typing import Any


def transcript_digest(messages: Sequence[dict[str, Any]]) -> str:
    h = hashlib.sha256()
    for m in messages:
        h.update(f"{m.get('role')}\0{m.get('content')}\0".encode())
    return h.hexdigest()[:16]


def direct_messages(messages: Sequence[dict[str, Any]]) -> list[dict[str, str]]:
    """User/assistant prose only: tool rows and system prompts never leave the machine."""
    return [{"role": str(m["role"]), "content": m["content"]} for m in messages
            if m.get("role") in ("user", "assistant") and isinstance(m.get("content"), str) and m["content"].strip()]
