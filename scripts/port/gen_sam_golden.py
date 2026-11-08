"""Golden fixtures for the Go SAM package: outbox entry ids (Python's json.dumps layout), core-profile formatting
and profile-entry flattening, from the real plugins.memory.agent_memory code.

usage: python scripts/port/gen_sam_golden.py internal/memory/sam/testdata
"""
import json
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2]))

from plugins.memory.agent_memory.client import Profile, _entry_text  # noqa: E402
from plugins.memory.agent_memory.core_profile import format_profile  # noqa: E402
from plugins.memory.agent_memory.outbox import entry_id_for  # noqa: E402

ENTRIES = [
    ("remember", {"text": "likes tea", "labels": ["session=1"], "category": "identity", "scopes": "user/p", "session_id": None}),
    ("remember", {"text": "café ☕ 日本語 😀 \"quoted\" back\\slash\nnewline\ttab \x01 ctrl \x7f del"}),
    ("remember_many", {"items": [{"role": "user", "content": "hi"}, {"role": "assistant", "content": "yo"}], "session_id": "s", "labels": []}),
    ("forget", {"query": "x", "purge": True}),
    ("forget", {"query": "x", "purge": False}),
    ("remember", {"z": 1, "a": [1, 2.5, True, None, {"b": 1, "a": 2}], "n": -3}),
    ("remember", {"text": ""}),
    ("remember", {}),
]
PROFILES = [
    (Profile(), 1500),
    (Profile(instructions=("Be brief.",), preferences=("tea",), static=("lives in Cape Town",)), 1500),
    (Profile(instructions=("  padded  ",), static=("x" * 100,)), 120),
    (Profile(instructions=tuple(f"instruction {i}" for i in range(50))), 400),
    (Profile(static=("日本語のテキスト",) * 10), 200),
    (Profile(static=("a",)), 10),
]
FLAT = [
    {"description": "Always answer in English", "key": "k"},
    {"text": "t", "value": "v"},
    {"value": "only value"},
    {"key": "tz", "value": "UTC"},
    {"content": "c"},
    {"other": 1},
    {"description": 5, "text": "falls through"},
]


def main(outdir):
    out = pathlib.Path(outdir)
    out.mkdir(parents=True, exist_ok=True)
    data = {
        "entry_ids": [{"op": op, "payload": p, "id": entry_id_for(op, p)} for op, p in ENTRIES],
        "profiles": [{"instructions": list(p.instructions), "preferences": list(p.preferences), "static": list(p.static),
                      "max": m, "out": format_profile(p, m)} for p, m in PROFILES],
        "flat": [{"entry": e, "text": _entry_text(e)} for e in FLAT],
    }
    (out / "sam.json").write_text(json.dumps(data, indent=1, ensure_ascii=False, sort_keys=True) + "\n")
    print({k: len(v) for k, v in data.items()})


if __name__ == "__main__":
    main(sys.argv[1])
