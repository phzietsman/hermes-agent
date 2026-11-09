"""One-shot import of the builtin memory (MEMORY.md / USER.md) and, optionally, recent session transcripts.

Idempotent: every imported item's digest is recorded in a ledger next to the config, so running it twice
writes nothing the second time. The builtin store's code is gone by the time this matters, so the file
format (entries joined by "\\n§\\n") is read here directly.
"""

from __future__ import annotations

import hashlib
import importlib
import json
import os
import tempfile
import time
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Literal, NamedTuple, cast

from .client import MemoryClient, MemoryRejected, MemoryUnavailable
from .config import AgentMemorySettings
from .transcript import direct_messages, transcript_digest

ENTRY_DELIMITER = "\n§\n"
LEDGER_FILENAME = "agent-memory-import.json"
Source = Literal["memory", "user"]
# MEMORY.md holds learned knowledge; USER.md is who the user is and how they want to be treated.
_FILES: tuple[tuple[Source, str, str], ...] = (("memory", "MEMORY.md", "knowledge"), ("user", "USER.md", "identity"))


class ImportEntry(NamedTuple):
    source: Source
    category: str
    text: str
    digest: str


@dataclass
class ImportReport:
    planned: int = 0
    written: int = 0
    skipped: int = 0  # already imported (ledger hit)
    failed: int = 0


def _digest(kind: str, text: str) -> str:
    return hashlib.sha256(f"{kind}\0{text}".encode()).hexdigest()[:16]


def read_entries(home: Path) -> list[ImportEntry]:
    entries: list[ImportEntry] = []
    for source, filename, category in _FILES:
        try:
            raw = (home / "memories" / filename).read_text(encoding="utf-8")
        except FileNotFoundError:
            continue
        for text in (e.strip() for e in raw.split(ENTRY_DELIMITER)):
            if text:
                entries.append(ImportEntry(source, category, text, _digest(source, text)))
    return entries


class Ledger:
    def __init__(self, path: Path) -> None:
        self._path = path
        self._done: set[str] = set()
        try:
            data = cast("dict[str, list[str]]", json.loads(path.read_text(encoding="utf-8")))
            self._done = set(data.get("digests", []))
        except (FileNotFoundError, ValueError, OSError):
            pass

    def __contains__(self, digest: str) -> bool:
        return digest in self._done

    def add(self, digest: str) -> None:
        self._done.add(digest)
        self._path.parent.mkdir(parents=True, exist_ok=True)
        fd, tmp = tempfile.mkstemp(dir=self._path.parent, prefix=".agent-memory-import-")
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as fh:
                json.dump({"digests": sorted(self._done)}, fh)
            os.replace(tmp, self._path)
            os.chmod(self._path, 0o600)
        finally:
            if os.path.exists(tmp):
                os.unlink(tmp)


def _preview(text: str) -> str:
    one = " ".join(text.split())
    return one if len(one) <= 90 else one[:89] + "…"


def import_memory_files(
    client: MemoryClient, home: Path, settings: AgentMemorySettings, *, dry_run: bool = False, rate_s: float = 0.0,
    sleep: Callable[[float], None] = time.sleep, out: Callable[[str], None] = print,
) -> ImportReport:
    report, ledger = ImportReport(), Ledger(home / LEDGER_FILENAME)
    for entry in read_entries(home):
        if entry.digest in ledger:
            report.skipped += 1
            continue
        report.planned += 1
        if dry_run:
            out(f"  would import [{entry.source} -> {entry.category}] {_preview(entry.text)}")
            continue
        try:
            client.remember(entry.text, category=entry.category, scopes=settings.default_scope,
                            labels=["origin=import", f"source={entry.source}", f"digest={entry.digest}"])
        except (MemoryUnavailable, MemoryRejected) as exc:
            report.failed += 1
            out(f"  ✗ [{entry.source}] {_preview(entry.text)}: {exc}")
            continue
        ledger.add(entry.digest)  # recorded per entry: an interrupted import resumes where it stopped
        report.written += 1
        sleep(rate_s)
    return report


def import_sessions(
    client: MemoryClient, home: Path, settings: AgentMemorySettings, *, limit: int, dry_run: bool = False,
    rate_s: float = 0.0, sleep: Callable[[float], None] = time.sleep, out: Callable[[str], None] = print,
    db: Any = None,
) -> ImportReport:
    """Backfill the last ``limit`` sessions' user/assistant text from state.db."""
    report, ledger = ImportReport(), Ledger(home / LEDGER_FILENAME)
    if db is None:
        db = importlib.import_module("hermes_state").SessionDB(home / "state.db", read_only=True)
    for row in db.list_sessions_rich(limit=limit, order_by_last_active=True):
        messages: list[dict[str, Any]] = db.get_messages_as_conversation(row["id"])
        items = direct_messages(messages)
        digest = transcript_digest(messages)
        if not items:
            continue
        if digest in ledger:
            report.skipped += 1
            continue
        report.planned += 1
        if dry_run:
            out(f"  would import session {row['id']} ({len(items)} messages)")
            continue
        try:
            client.remember_many(items, scopes=settings.default_scope,
                                 labels=["origin=import-session", f"digest={digest}", f"session={row['id']}"])
        except (MemoryUnavailable, MemoryRejected) as exc:
            report.failed += 1
            out(f"  ✗ session {row['id']}: {exc}")
            continue
        ledger.add(digest)
        report.written += 1
        sleep(rate_s)
    return report
