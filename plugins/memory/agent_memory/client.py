from __future__ import annotations

import importlib
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from typing import Any, Protocol

from .config import AgentMemorySettings


class MemoryUnavailable(Exception):
    """Transport/service failure: counts toward the breaker, writes go to the outbox."""


class MemoryAuthFailed(MemoryUnavailable):
    """401/403/scope failure: opens the breaker immediately."""


@dataclass(frozen=True)
class Hit:
    id: str
    text: str
    score: float


@dataclass(frozen=True)
class Recall:
    hits: tuple[Hit, ...]


@dataclass(frozen=True)
class Profile:
    """Standing facts about the user, already flattened to text."""

    instructions: tuple[str, ...] = ()
    preferences: tuple[str, ...] = ()
    static: tuple[str, ...] = ()


class MemoryClient(Protocol):
    """The seam to Agent Memory. Faked in tests; the adapter below is the only SDK importer."""

    def remember(self, text: str, *, labels: Sequence[str] = (), category: str | None = None,
                 scopes: str | None = None, session_id: str | None = None) -> dict[str, Any]: ...
    def remember_many(self, items: Sequence[Mapping[str, str]], *, session_id: str | None = None,
                      scopes: str | None = None, labels: Sequence[str] = ()) -> dict[str, Any]: ...
    def recall(self, query: str, *, k: int, lens: Sequence[str] = ()) -> Recall: ...
    def query_context(self, query: str, *, k: int, lens: Sequence[str] = ()) -> str: ...
    def consolidate(self) -> dict[str, Any]: ...
    def reflect(self, query: str, *, persist: bool = False) -> dict[str, Any]: ...
    def forget(self, query: str, *, purge: bool = False) -> dict[str, Any]: ...
    def profile(self) -> Profile: ...
    def register_scope(self, path: str) -> None: ...
    def whoami(self) -> dict[str, Any]: ...
    def close(self) -> None: ...


def _dump(obj: Any) -> dict[str, Any]:
    dump = getattr(obj, "model_dump", None)
    out = dump() if callable(dump) else getattr(obj, "__dict__", {})
    return dict(out) if isinstance(out, dict) else {}  # pyright: ignore[reportUnknownArgumentType]


def _entry_text(entry: Any) -> str:
    if isinstance(entry, Mapping):
        for key in ("text", "value", "content"):
            if isinstance(entry.get(key), str):  # pyright: ignore[reportUnknownMemberType]
                return str(entry[key])  # pyright: ignore[reportUnknownArgumentType]
        return str(dict(entry))  # pyright: ignore[reportUnknownArgumentType]
    name, value = getattr(entry, "key", None), getattr(entry, "value", None)
    return f"{name}: {value}" if name is not None else str(entry)


class SurrealMemoryAdapter:
    """surrealdb.memory.Memory behind MemoryClient. SDK errors become MemoryUnavailable/MemoryAuthFailed."""

    def __init__(self, settings: AgentMemorySettings) -> None:
        sdk = importlib.import_module("surrealdb.memory")
        self._auth = (sdk.MemoryAuthError, sdk.MemoryScopeError)
        self._sdk_errors = (sdk.MemoryAPIError,)
        self._client: Any = sdk.Memory(
            settings.context,
            endpoint=str(settings.endpoint).rstrip("/"),
            api_key=settings.api_key.get_secret_value(),
            timeout=settings.timeout_s,
            max_retries=settings.max_retries,
        )

    def _call(self, fn: Any, *args: Any, **kwargs: Any) -> Any:
        try:
            return fn(*args, **kwargs)
        except self._auth as exc:
            raise MemoryAuthFailed(str(exc)) from exc
        except self._sdk_errors as exc:
            raise MemoryUnavailable(str(exc)) from exc
        except OSError as exc:  # connection reset, DNS, timeouts surface as OSError subclasses
            raise MemoryUnavailable(str(exc)) from exc

    def remember(self, text: str, *, labels: Sequence[str] = (), category: str | None = None,
                 scopes: str | None = None, session_id: str | None = None) -> dict[str, Any]:
        return _dump(self._call(self._client.remember, text, labels=list(labels) or None,
                                memory_category=category, scopes=scopes, session_id=session_id))

    def remember_many(self, items: Sequence[Mapping[str, str]], *, session_id: str | None = None,
                      scopes: str | None = None, labels: Sequence[str] = ()) -> dict[str, Any]:
        return _dump(self._call(self._client.remember_many, list(items), session_id=session_id, scopes=scopes,
                                labels=list(labels) or None))

    def recall(self, query: str, *, k: int, lens: Sequence[str] = ()) -> Recall:
        resp = self._call(self._client.recall, query, k=k, lens=list(lens) or None)
        return Recall(tuple(Hit(str(h.id), str(h.text), float(h.score)) for h in resp.hits))

    def query_context(self, query: str, *, k: int, lens: Sequence[str] = ()) -> str:
        return str(self._call(self._client.query_context, query, k=k, lens=list(lens) or None).context)

    def consolidate(self) -> dict[str, Any]:
        return _dump(self._call(self._client.consolidate))

    def reflect(self, query: str, *, persist: bool = False) -> dict[str, Any]:
        return _dump(self._call(self._client.reflect, query, persist=persist))

    def forget(self, query: str, *, purge: bool = False) -> dict[str, Any]:
        return _dump(self._call(self._client.forget, query, purge=purge))

    def profile(self) -> Profile:
        p = self._call(self._client.profile)
        return Profile(
            instructions=tuple(_entry_text(e) for e in p.instructions),
            preferences=tuple(_entry_text(e) for e in p.preferences),
            static=tuple(_entry_text(e) for e in p.static),
        )

    def register_scope(self, path: str) -> None:
        self._call(self._client.scopes.register, path)

    def whoami(self) -> dict[str, Any]:
        return _dump(self._call(self._client.whoami))

    def close(self) -> None:
        self._client.close()
