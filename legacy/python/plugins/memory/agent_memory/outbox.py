from __future__ import annotations

import hashlib
import json
import os
import tempfile
import threading
from collections.abc import Callable
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict

from .client import MemoryClient

OutboxOp = Literal["remember", "remember_many", "forget"]


class OutboxEntry(BaseModel):
    model_config = ConfigDict(frozen=True)

    entry_id: str
    op: OutboxOp
    payload: dict[str, Any]
    created_at: datetime
    attempts: int = 0


def entry_id_for(op: str, payload: dict[str, Any]) -> str:
    """Content-addressed, so an identical write queued twice replays once."""
    return hashlib.sha256(f"{op}:{json.dumps(payload, sort_keys=True)}".encode()).hexdigest()[:24]


class Outbox:
    """Durable jsonl of writes that did not reach Agent Memory. Replay removes what it applied."""

    def __init__(self, path: Path) -> None:
        self._path = path
        self._lock = threading.Lock()

    def _read(self) -> list[OutboxEntry]:
        try:
            lines = self._path.read_text(encoding="utf-8").splitlines()
        except FileNotFoundError:
            return []
        return [OutboxEntry.model_validate_json(line) for line in lines if line.strip()]

    def _write(self, entries: list[OutboxEntry]) -> None:
        if not entries:
            self._path.unlink(missing_ok=True)
            return
        self._path.parent.mkdir(parents=True, exist_ok=True)
        fd, tmp = tempfile.mkstemp(dir=self._path.parent, prefix=".outbox-")
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as fh:
                fh.writelines(e.model_dump_json() + "\n" for e in entries)
            os.replace(tmp, self._path)
            os.chmod(self._path, 0o600)
        finally:
            if os.path.exists(tmp):
                os.unlink(tmp)

    def append(self, op: OutboxOp, payload: dict[str, Any]) -> bool:
        """False when an identical entry is already pending."""
        entry = OutboxEntry(entry_id=entry_id_for(op, payload), op=op, payload=payload,
                            created_at=datetime.now(timezone.utc))
        with self._lock:
            entries = self._read()
            if any(e.entry_id == entry.entry_id for e in entries):
                return False
            self._write([*entries, entry])
            return True

    def pending(self) -> list[OutboxEntry]:
        with self._lock:
            return self._read()

    def replay(self, apply: Callable[[OutboxEntry], None]) -> int:
        """Apply entries oldest-first, dropping each on success. Stops at the first failure (backend
        down: do not hammer it) and keeps that entry plus everything after it. Returns entries applied."""
        with self._lock:
            entries = self._read()
            applied = 0
            for i, entry in enumerate(entries):
                try:
                    apply(entry)
                except Exception:  # health: allow BLE001 -- any apply failure keeps the entry for the next replay
                    rest = [entries[i].model_copy(update={"attempts": entry.attempts + 1}), *entries[i + 1:]]
                    self._write(rest)
                    return applied
                applied += 1
            self._write([])
            return applied


def apply_entry(client: MemoryClient, entry: OutboxEntry) -> None:
    """Send one queued write. The single place an outbox op is turned back into a client call."""
    p = entry.payload
    if entry.op == "remember":
        client.remember(p["text"], labels=p.get("labels", ()), category=p.get("category"),
                        scopes=p.get("scopes"), session_id=p.get("session_id"))
    elif entry.op == "remember_many":
        client.remember_many(p["items"], session_id=p.get("session_id"), scopes=p.get("scopes"),
                             labels=p.get("labels", ()))
    else:
        client.forget(p["query"], purge=bool(p.get("purge", False)))
