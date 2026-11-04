from __future__ import annotations

import json
import os
import tempfile
from pathlib import Path

from .client import Profile

HEADING = "## About the user / standing instructions"


def format_profile(profile: Profile, max_chars: int) -> str:
    """Instructions first (they are commands), then preferences, then static facts; cut at a line boundary."""
    lines = [*profile.instructions, *profile.preferences, *profile.static]
    out: list[str] = []
    size = len(HEADING)
    for line in lines:
        item = f"- {line.strip()}"
        if size + len(item) + 1 > max_chars:
            break
        out.append(item)
        size += len(item) + 1
    return f"{HEADING}\n" + "\n".join(out) if out else ""


class CoreProfileCache:
    """Last good core profile on disk, served when Agent Memory is unreachable."""

    def __init__(self, path: Path) -> None:
        self._path = path

    def load(self) -> str:
        try:
            data = json.loads(self._path.read_text(encoding="utf-8"))
        except (FileNotFoundError, ValueError, OSError):
            return ""
        text = data.get("text") if isinstance(data, dict) else None  # pyright: ignore[reportUnknownVariableType, reportUnknownMemberType]
        return text if isinstance(text, str) else ""

    def save(self, text: str) -> None:
        self._path.parent.mkdir(parents=True, exist_ok=True)
        fd, tmp = tempfile.mkstemp(dir=self._path.parent, prefix=".core-profile-")
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as fh:
                json.dump({"text": text}, fh)
            os.replace(tmp, self._path)
            os.chmod(self._path, 0o600)
        finally:
            if os.path.exists(tmp):
                os.unlink(tmp)
