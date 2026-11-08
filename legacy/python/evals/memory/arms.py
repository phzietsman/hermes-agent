from __future__ import annotations

from typing import Protocol


class MemoryArm(Protocol):
    """A system under test. Live arms drive a real agent; fakes validate the harness."""

    name: str

    def start_session(self) -> None: ...
    def tell(self, key: str, statement: str) -> None: ...
    def ask(self, key: str, question: str) -> str | None: ...
    def writes_claimed(self) -> int: ...
    def writes_landed(self) -> int: ...
    def landing_detail(self) -> dict[str, int]: ...


class DictArm:
    """Deterministic in-memory arm: remembers every statement, latest wins."""

    name = "dict"

    def __init__(self, drop_every: int = 0) -> None:
        self._store: dict[str, str] = {}
        self._drop_every = drop_every
        self._claimed = 0
        self._landed = 0

    def start_session(self) -> None:
        return None

    def tell(self, key: str, statement: str) -> None:
        self._claimed += 1
        if self._drop_every and self._claimed % self._drop_every == 0:
            return
        self._store[key] = statement
        self._landed += 1

    def ask(self, key: str, question: str) -> str | None:
        return self._store.get(key)

    def writes_claimed(self) -> int:
        return self._claimed

    def writes_landed(self) -> int:
        return self._landed

    def landing_detail(self) -> dict[str, int]:
        return {}
