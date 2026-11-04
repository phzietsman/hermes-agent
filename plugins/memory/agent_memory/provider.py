from __future__ import annotations

import logging
import queue
import re
import threading
from collections.abc import Callable
from pathlib import Path
from typing import Any

from agent.memory_provider import MemoryProvider, spawn_context_thread  # pyright: ignore[reportUnknownVariableType]

from . import tools
from .breaker import Breaker
from .client import (
    MemoryAuthFailed, MemoryClient, MemoryRejected, MemoryTimeout, MemoryUnavailable, Recall, SurrealMemoryAdapter,
)
from .config import AgentMemorySettings, RecallMode, config_schema, load_settings, save_config_file
from .core_profile import CoreProfileCache, format_profile
from .outbox import Outbox, OutboxEntry, OutboxOp, apply_entry
from .transcript import direct_messages, transcript_digest

logger = logging.getLogger(__name__)

ClientFactory = Callable[[AgentMemorySettings], MemoryClient]
_STOP: Any = object()
# Skills are mirrored as "Skill '<name>': ..." pointers (the SDK's RecallHit carries no labels).
_SKILL_HIT = re.compile(r"^Skill '([^']+)':")
# Contexts that must never write: cron/subagent/flush runs are not the user's conversation.
_READ_ONLY_CONTEXTS = frozenset({"cron", "subagent", "flush"})


class AgentMemoryProvider(MemoryProvider):  # pyright: ignore[reportUntypedBaseClass]
    def __init__(self, client_factory: ClientFactory | None = None) -> None:
        self._factory: ClientFactory = client_factory or SurrealMemoryAdapter
        self._settings: AgentMemorySettings | None = None
        self._client: MemoryClient | None = None
        self._breaker = Breaker(3)
        self._outbox: Outbox | None = None
        self._cache: CoreProfileCache | None = None
        self._core_text = ""
        self._home = Path()
        self._session_id = ""
        self._writes_enabled = True
        self._turns = 0
        self._jobs: queue.Queue[Any] = queue.Queue()
        self._worker: threading.Thread | None = None
        self._done: tuple[str, str] = ("", "")  # (query, formatted) of the last completed recall
        self._inflight: tuple[str, threading.Event] | None = None
        self._recall_lock = threading.Lock()
        self._remembered: set[str] = set()  # transcript digests already queued this session

    @property
    def name(self) -> str:
        return "agent_memory"

    # -- readiness ---------------------------------------------------------------------------------------------

    def _home_dir(self, hermes_home: str | None = None) -> Path:
        if hermes_home:
            return Path(hermes_home)
        from hermes_constants import get_hermes_home  # pyright: ignore[reportUnknownVariableType]

        return Path(get_hermes_home())  # pyright: ignore[reportUnknownArgumentType]

    def is_available(self) -> bool:
        return load_settings(self._home_dir(str(self._home) if self._home != Path() else None))[0] is not None

    def unavailable_reason(self) -> str:
        return load_settings(self._home_dir(str(self._home) if self._home != Path() else None))[1]

    # -- lifecycle ---------------------------------------------------------------------------------------------

    def initialize(self, session_id: str, **kwargs: Any) -> None:
        self._session_id = session_id
        self._home = self._home_dir(kwargs.get("hermes_home"))
        self._writes_enabled = kwargs.get("agent_context", "primary") not in _READ_ONLY_CONTEXTS
        settings, _ = load_settings(self._home)
        if settings is None:
            return
        self._settings = settings
        self._breaker = Breaker(settings.breaker_threshold)
        self._outbox = Outbox(self._home / "agent-memory-outbox.jsonl")
        self._cache = CoreProfileCache(self._home / "core_profile_cache.json")
        user = kwargs.get("user_id") or kwargs.get("user_id_alt")
        if user and settings.default_scope == "user/default":
            self._settings = settings.model_copy(update={"default_scope": f"user/{user}"})
        try:
            self._client = self._factory(self._settings)
        except Exception:  # health: allow BLE001 -- SDK import/config errors must degrade, not break agent init
            logger.exception("Agent Memory client could not be built; memory disabled")
            return
        self._core_text = self._load_core_profile()
        self._worker = spawn_context_thread(self._work, name="agent-memory-writer")  # pyright: ignore[reportUnknownVariableType]
        self._worker.start()
        self._jobs.put(self._replay)

    def _load_core_profile(self) -> str:
        """Frozen for the session (a mid-session change would break the cached prompt prefix); the
        cache file lets a degraded start still get the last good profile."""
        assert self._client and self._settings and self._cache
        try:
            text = format_profile(self._client.profile(), self._settings.core_profile_max_chars)
        except MemoryRejected:
            return self._cache.load()
        except MemoryUnavailable as exc:
            self._breaker.record_failure("profile", auth=isinstance(exc, MemoryAuthFailed))
            return self._cache.load()
        self._breaker.record_ok()
        if text:
            self._cache.save(text)
        return text

    def shutdown(self) -> None:
        if self._worker is not None and self._worker.is_alive():
            self._jobs.put(_STOP)
            self._worker.join(timeout=5.0)
        if self._client is not None:
            self._client.close()

    def _active(self) -> bool:
        return self._client is not None and self._settings is not None and not self._breaker.is_open

    # -- recall ------------------------------------------------------------------------------------------------

    def system_prompt_block(self) -> str:
        if self._client is None:
            return ""
        guide = (
            "You have persistent long-term memory (SurrealDB Agent Memory). Call agent_memory_remember for "
            "durable facts, corrections, preferences and lessons; recalled memory appears under "
            "\"## Recalled memory\". If the user refers to something you may have been told before and it is "
            "not in context, call agent_memory_recall. Procedures belong in skills (skill_manage), not memory."
        )
        return f"{guide}\n\n{self._core_text}" if self._core_text else guide

    def _recall_text(self, query: str) -> str:
        assert self._client and self._settings
        s = self._settings
        lens = [s.default_scope]
        if s.recall_mode is RecallMode.CONTEXT:
            body = self._client.query_context(query, k=s.top_k, lens=lens).strip()
            return f"## Recalled memory\n{body}"[: s.max_prefetch_chars] if body else ""
        return self._format_hits(self._client.recall(query, k=s.top_k, lens=lens), s.max_prefetch_chars)

    @staticmethod
    def _format_hits(recall: Recall, limit: int) -> str:
        lines: list[str] = []
        skills: list[str] = []
        for h in recall.hits:
            if m := _SKILL_HIT.match(h.text):
                skills.append(m.group(1))
            lines.append(f"- {h.text.strip()} ({h.score:.2f})")
        if not lines:
            return ""
        out = "## Recalled memory\n" + "\n".join(lines)
        if skills:
            out += f"\nRelevant skills: {', '.join(dict.fromkeys(skills))} (load with skill_view)"
        return out[:limit]

    def _ensure_recall(self, query: str) -> threading.Event:
        """Start (or join) a background recall for ``query``; the Event fires when it has finished."""
        with self._recall_lock:
            if self._inflight is not None and self._inflight[0] == query and not self._inflight[1].is_set():
                return self._inflight[1]
            done = threading.Event()
            self._inflight = (query, done)

        def _run() -> None:
            try:
                self._done = (query, self._recall_text(query))
                self._breaker.record_ok()
            except MemoryTimeout:
                logger.debug("Agent Memory recall timed out; serving the last completed recall")
            except MemoryUnavailable as exc:
                self._breaker.record_failure("recall", auth=isinstance(exc, MemoryAuthFailed))
            finally:
                done.set()

        spawn_context_thread(_run, name="agent-memory-prefetch").start()  # pyright: ignore[reportUnknownMemberType]
        return done

    def prefetch(self, query: str, *, session_id: str = "") -> str:
        """Never blocks long: waits up to prefetch_wait_s for this query's recall, else returns the last
        completed one (the usual case after queue_prefetch warmed it during the previous turn)."""
        if not self._active() or not query.strip() or self._settings is None:
            return ""
        if self._settings.recall_mode is RecallMode.TOOLS:
            return ""
        if self._done[0] != query:
            self._ensure_recall(query).wait(self._settings.prefetch_wait_s)
        return self._done[1]

    def queue_prefetch(self, query: str, *, session_id: str = "") -> None:
        if self._active() and query.strip():
            self._ensure_recall(query)

    # -- writes (all through the outbox) -----------------------------------------------------------------------

    def _work(self) -> None:
        while (job := self._jobs.get()) is not _STOP:
            try:
                job()
            except Exception:  # health: allow BLE001 -- a bad job must not kill the writer; traceback logged
                logger.exception("Agent Memory writer job failed")

    def _write(self, op: OutboxOp, payload: dict[str, Any]) -> None:
        """Queue-or-send. A failure (or an open breaker) leaves the write in the outbox for replay."""
        if self._outbox is None or not self._writes_enabled:
            return
        if not self._outbox.append(op, payload):
            return  # an identical write is already waiting
        if self._active():
            self._jobs.put(self._replay)

    def _replay(self) -> None:
        if self._outbox is None or not self._active():
            return

        def _apply(entry: OutboxEntry) -> None:
            assert self._client
            try:
                apply_entry(self._client, entry)
            except MemoryRejected as exc:
                # Retrying the same request cannot succeed; dropping it keeps the queue moving.
                logger.warning("Agent Memory rejected a queued %s write; dropped: %s", entry.op, exc)
                return
            except MemoryUnavailable as exc:
                self._breaker.record_failure(entry.op, auth=isinstance(exc, MemoryAuthFailed))
                raise
            self._breaker.record_ok()

        self._outbox.replay(_apply)

    def _session_label(self, session_id: str = "") -> str:
        """Sessions are labels, not the SDK's session_id: the service only accepts session ids it created
        for the caller, so passing Hermes's own id is a 403."""
        return f"session={(session_id or self._session_id).replace(' ', '_')}"

    def _scope(self) -> str | None:
        return self._settings.default_scope if self._settings else None

    def sync_turn(self, user_content: str, assistant_content: str, *, session_id: str = "",
                  messages: list[dict[str, Any]] | None = None, turn_author: dict[str, Any] | None = None) -> None:
        if self._settings is None:
            return
        items = direct_messages([{"role": "user", "content": user_content}, {"role": "assistant", "content": assistant_content}])
        if self._settings.send_tool_outputs and messages:
            items += [{"role": "tool", "content": str(m["content"])[:2000]} for m in messages
                      if m.get("role") == "tool" and isinstance(m.get("content"), str)][-5:]
        if items:
            self._write("remember_many", {"items": items, "scopes": self._scope(),
                                          "labels": ["origin=turn", self._session_label(session_id)]})

    def _remember_transcript(self, messages: list[dict[str, Any]]) -> None:
        items = direct_messages(messages)
        digest = transcript_digest(messages)
        # pre-compress and session-end often see the same transcript; the outbox only dedupes while an
        # entry is still pending, so remember what was already queued once the writer has sent it.
        if items and digest not in self._remembered:
            self._remembered.add(digest)
            self._write("remember_many", {"items": items, "scopes": self._scope(),
                                          "labels": [f"digest={digest}", "origin=session", self._session_label()]})

    def on_pre_compress(self, messages: list[dict[str, Any]]) -> str:
        self._remember_transcript(messages)
        return ""

    def on_session_end(self, messages: list[dict[str, Any]]) -> None:
        self._remember_transcript(messages)
        if self._settings and self._settings.consolidate_on_end and self._writes_enabled:
            self._jobs.put(self._consolidate)

    def _consolidate(self) -> None:
        if not self._active():
            return
        self._replay()  # land pending writes first so consolidation sees them
        assert self._client
        try:
            self._client.consolidate()
        except MemoryUnavailable as exc:
            self._breaker.record_failure("consolidate", auth=isinstance(exc, MemoryAuthFailed))

    def on_session_switch(self, new_session_id: str, *, parent_session_id: str = "", reset: bool = False,
                          rewound: bool = False, **kwargs: Any) -> None:
        self._session_id = new_session_id

    def on_turn_start(self, turn_number: int, message: str, **kwargs: Any) -> None:
        self._turns += 1
        if self._settings and self._turns % self._settings.replay_every_n_turns == 0:
            self._jobs.put(self._replay)

    # -- tools -------------------------------------------------------------------------------------------------

    def get_tool_schemas(self) -> list[dict[str, Any]]:
        return [dict(s) for s in tools.SCHEMAS]

    def handle_tool_call(self, tool_name: str, args: dict[str, Any], **kwargs: Any) -> str:
        if tool_name not in tools.TOOL_NAMES:
            return tools.error(f"unknown tool {tool_name}")
        if not self._active() or self._client is None or self._settings is None:
            return tools.error("Agent Memory is unavailable")
        try:
            if tool_name == "agent_memory_recall":
                out = tools.recall(self._client, args, self._settings.top_k, [self._settings.default_scope])
            elif tool_name == "agent_memory_reflect":
                out = tools.reflect(self._client, args)
            elif tool_name == "agent_memory_forget":
                self._write("forget", {"query": str(args["query"]), "purge": bool(args.get("purge", False))})
                out = tools.result({"queued": True})
            else:
                from tools.skill_provenance import get_current_write_origin  # pyright: ignore[reportUnknownVariableType]

                origin = str(get_current_write_origin())  # pyright: ignore[reportUnknownArgumentType]
                kind = str(args.get("kind") or "fact")
                payload = tools.remember_args(str(args["text"]), kind, origin)
                payload["labels"].append(self._session_label())
                self._write("remember", {**payload, "scopes": self._scope()})
                out = tools.result({"queued": True})
        except MemoryTimeout as exc:
            return tools.error(f"Agent Memory is slow right now ({exc}); try again or continue without it")
        except MemoryRejected as exc:
            return tools.error(f"Agent Memory rejected the request: {exc}")
        except MemoryUnavailable as exc:
            self._breaker.record_failure(tool_name, auth=isinstance(exc, MemoryAuthFailed))
            return tools.error(str(exc))
        self._breaker.record_ok()
        return out

    # -- config ------------------------------------------------------------------------------------------------

    def get_config_schema(self) -> list[dict[str, Any]]:
        return config_schema()

    def save_config(self, values: dict[str, Any], hermes_home: str) -> None:
        save_config_file(values, Path(hermes_home))

