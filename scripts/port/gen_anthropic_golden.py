"""Generate golden fixtures for the Go Anthropic converter by running the Python converter.

usage (from the repo root, with the test environment's interpreter):
    python scripts/port/gen_anthropic_golden.py internal/provider/anthropic/testdata

Each fixture records the scenario input and what the Python implementation produced, so the Go port
is checked against real behaviour rather than against a re-reading of the code. Re-run it only to
add scenarios: the Python converter is the reference until the cutover removes it.
"""
import json
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2]))

from agent.anthropic_message_convert import (  # noqa: E402
    convert_messages_to_anthropic,
    convert_tools_to_anthropic,
    normalize_model_name,
)
from agent.anthropic_thinking_policy import model_preserves_prior_thinking  # noqa: E402
from agent.image_eviction_policy import outbound_image_retire_count  # noqa: E402

PNG = "data:image/png;base64,iVBORw0KGgo="
JPG = "data:image/jpg;base64,/9j/4AAQ"
SVG = "data:image/svg+xml;base64,PHN2Zz48L3N2Zz4="
EPH = {"type": "ephemeral"}
EPH1H = {"type": "ephemeral", "ttl": "1h"}


def tc(i, name, args="{}"):
    return {"id": i, "type": "function", "function": {"name": name, "arguments": args}}


def sig(text, s="sig-abc"):
    return {"type": "thinking", "thinking": text, "signature": s}


OPUS = "claude-opus-4-5"
HAIKU = "claude-haiku-4-5"

CASES = {
    "simple": (OPUS, [
        {"role": "system", "content": "You are Hermes."},
        {"role": "user", "content": "hi"},
        {"role": "assistant", "content": "hello"},
        {"role": "user", "content": "bye"},
    ]),
    "system_blocks_cached": (OPUS, [
        {"role": "system", "content": [
            {"type": "text", "text": "stable prefix", "cache_control": EPH1H},
            {"type": "text", "text": "   "},
            {"type": "text", "text": "tail"},
        ]},
        {"role": "user", "content": "hi"},
    ]),
    "system_blocks_joined": (OPUS, [
        {"role": "system", "content": [{"type": "text", "text": "a"}, {"type": "text", "text": "b"}]},
        {"role": "user", "content": "hi"},
    ]),
    "last_system_wins": (OPUS, [
        {"role": "system", "content": "first"}, {"role": "system", "content": "second"},
        {"role": "user", "content": "hi"},
    ]),
    "tool_roundtrip_parallel": (OPUS, [
        {"role": "user", "content": "do two things"},
        {"role": "assistant", "content": "on it", "tool_calls": [tc("call_1", "read_file", '{"path":"a"}'), tc("call_2", "terminal", '{"cmd":"ls"}')]},
        {"role": "tool", "tool_call_id": "call_1", "content": "file a"},
        {"role": "tool", "tool_call_id": "call_2", "content": "x y z"},
        {"role": "assistant", "content": "done"},
    ]),
    "tool_id_sanitised_and_bad_args": (OPUS, [
        {"role": "user", "content": "go"},
        {"role": "assistant", "content": None, "tool_calls": [tc("call:1/x y", "t", "{not json"), tc("", "u", "")]},
        {"role": "tool", "tool_call_id": "call:1/x y", "content": "r1"},
        {"role": "tool", "tool_call_id": "", "content": ""},
    ]),
    "orphan_tool_use_invalidates_thinking": (OPUS, [
        {"role": "user", "content": "go"},
        {"role": "assistant", "content": "x", "reasoning_details": [sig("deep thought")], "tool_calls": [tc("c1", "t"), tc("c2", "t")]},
        {"role": "tool", "tool_call_id": "c1", "content": "only one result"},
        {"role": "assistant", "content": "after"},
    ]),
    "orphan_tool_use_nothing_left": (OPUS, [
        {"role": "user", "content": "go"},
        {"role": "assistant", "content": None, "tool_calls": [tc("c1", "t")]},
        {"role": "user", "content": "never mind"},
    ]),
    "orphan_tool_result": (OPUS, [
        {"role": "user", "content": "hi"},
        {"role": "tool", "tool_call_id": "gone", "content": "stale"},
        {"role": "assistant", "content": "ok"},
    ]),
    "blank_everything": (OPUS, [
        {"role": "user", "content": ""},
        {"role": "assistant", "content": ""},
        {"role": "user", "content": [{"type": "text", "text": "  ", "cache_control": EPH}, {"type": "text", "text": ""}]},
        {"role": "assistant", "content": None, "cache_control": EPH},
        {"role": "user", "content": "   "},
    ]),
    "merge_consecutive": (OPUS, [
        {"role": "user", "content": "one"},
        {"role": "user", "content": [{"type": "text", "text": "two"}]},
        {"role": "assistant", "content": "a", "reasoning_details": [sig("t1")]},
        {"role": "assistant", "content": "b", "reasoning_details": [sig("t2", "s2")]},
        {"role": "user", "content": "three"},
    ]),
    "leading_assistant": (OPUS, [
        {"role": "assistant", "content": "summary of earlier chat"},
        {"role": "user", "content": "continue"},
    ]),
    "thinking_modern_keeps_all": (OPUS, [
        {"role": "user", "content": "q1"},
        {"role": "assistant", "content": "a1", "reasoning_details": [sig("first", "s1")]},
        {"role": "user", "content": "q2"},
        {"role": "assistant", "content": "a2", "reasoning_details": [sig("second", "s2")]},
    ]),
    "thinking_haiku_last_turn_only": (HAIKU, [
        {"role": "user", "content": "q1"},
        {"role": "assistant", "content": "a1", "reasoning_details": [sig("first", "s1")]},
        {"role": "user", "content": "q2"},
        {"role": "assistant", "content": "a2", "reasoning_details": [sig("second", "s2")]},
    ]),
    "thinking_haiku_only_thinking_earlier": (HAIKU, [
        {"role": "user", "content": "q1"},
        {"role": "assistant", "content": None, "reasoning_details": [sig("first", "s1")]},
        {"role": "user", "content": "q2"},
        {"role": "assistant", "content": "a2"},
    ]),
    "thinking_unsigned_demoted": (OPUS, [
        {"role": "user", "content": "q"},
        {"role": "assistant", "content": "a", "reasoning_details": [{"type": "thinking", "thinking": "readable but unsigned"}]},
    ]),
    "thinking_redacted": (OPUS, [
        {"role": "user", "content": "q"},
        {"role": "assistant", "content": "a", "reasoning_details": [
            {"type": "redacted_thinking", "data": "opaque"},
            {"type": "redacted_thinking"},
            {"type": "REDACTED_THINKING", "data": "upper"},
            {"type": "text", "text": "not thinking"},
        ]},
    ]),
    "thinking_cache_control_stripped": (OPUS, [
        {"role": "user", "content": "q"},
        {"role": "assistant", "content": "a", "reasoning_details": [{**sig("t"), "cache_control": EPH}]},
    ]),
    "ordered_blocks_replay": (OPUS, [
        {"role": "user", "content": "q"},
        {"role": "assistant", "content": [{"type": "text", "text": "preamble", "cache_control": EPH}],
         "tool_calls": [tc("toolu_1", "read_file", '{"path": "[REDACTED]"}')],
         "anthropic_content_blocks": [
             {"type": "thinking", "thinking": "plan", "signature": "sg", "extra_field": 1},
             {"type": "text", "text": "preamble", "parsed_output": None, "citations": None},
             {"type": "text", "text": "  "},
             {"type": "tool_use", "id": "toolu_1", "name": "read_file", "input": {"path": "sk-secret"}, "caller": "x"},
             {"type": "server_tool_use", "id": "z"},
         ]},
        {"role": "tool", "tool_call_id": "toolu_1", "content": "ok"},
    ]),
    "ordered_blocks_nothing_cacheable": (OPUS, [
        {"role": "user", "content": "q"},
        {"role": "assistant", "content": "x", "cache_control": EPH,
         "anthropic_content_blocks": [sig("only thinking"), {"type": "text", "text": "", "cache_control": EPH}]},
    ]),
    "ordered_blocks_all_dropped_falls_back": (OPUS, [
        {"role": "user", "content": "q"},
        {"role": "assistant", "content": "fallback text",
         "anthropic_content_blocks": [{"type": "mystery"}, {"type": "redacted_thinking"}]},
    ]),
    "multimodal_tool_result": (OPUS, [
        {"role": "user", "content": "look"},
        {"role": "assistant", "content": None, "tool_calls": [tc("c1", "screenshot")]},
        {"role": "tool", "tool_call_id": "c1", "content": {"_multimodal": True, "text_summary": "a page", "content": [
            {"type": "text", "text": "caption"}, {"type": "image_url", "image_url": {"url": PNG}}, {"type": "text", "text": ""},
        ]}},
    ]),
    "multimodal_summary_fallback": (OPUS, [
        {"role": "user", "content": "look"},
        {"role": "assistant", "content": None, "tool_calls": [tc("c1", "screenshot")]},
        {"role": "tool", "tool_call_id": "c1", "content": {"_multimodal": True, "text_summary": "only the summary", "content": []}},
    ]),
    "tool_result_image_parts": (OPUS, [
        {"role": "user", "content": "look"},
        {"role": "assistant", "content": None, "tool_calls": [tc("c1", "view")]},
        {"role": "tool", "tool_call_id": "c1", "cache_control": EPH, "content": [
            {"type": "text", "text": "t"}, {"type": "image_url", "image_url": {"url": JPG}},
        ]},
    ]),
    "tool_result_stashed_blocks": (OPUS, [
        {"role": "user", "content": "x"},
        {"role": "assistant", "content": None, "tool_calls": [tc("c1", "view")]},
        {"role": "tool", "tool_call_id": "c1", "content": "text part",
         "_anthropic_content_blocks": [{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "AAAA"}}]},
    ]),
    "user_images": (OPUS, [
        {"role": "user", "content": [
            {"type": "input_text", "text": "see these"},
            {"type": "image_url", "image_url": {"url": JPG}},
            {"type": "image_url", "image_url": {"url": SVG}},
            {"type": "input_image", "image_url": "https://example.com/x.png"},
            {"type": "image_url", "image_url": {"url": "data:application/pdf;base64,AAAA"}},
            {"type": "text", "text": "cited", "citations": [{"type": "char_location"}]},
            {"type": "text", "text": "empty cites", "citations": []},
            {"type": "custom_block", "payload": {"a": [1, 2]}},
        ]},
    ]),
    "reasoning_content_injection": (OPUS, [
        {"role": "user", "content": "q"},
        {"role": "assistant", "content": None, "reasoning_content": "", "tool_calls": [tc("c1", "t")]},
        {"role": "tool", "tool_call_id": "c1", "content": "r"},
        {"role": "assistant", "content": "a", "reasoning_content": "plain reasoning"},
    ]),
    "cache_markers_everywhere": (OPUS, [
        {"role": "system", "content": [{"type": "text", "text": "sys", "cache_control": EPH1H}]},
        {"role": "user", "content": [{"type": "text", "text": "u", "cache_control": EPH}]},
        {"role": "assistant", "content": [{"type": "text", "text": "a"}, {"type": "text", "text": "b", "cache_control": EPH}],
         "tool_calls": [tc("c1", "t")]},
        {"role": "tool", "tool_call_id": "c1", "content": "r", "cache_control": EPH},
    ]),
    "unknown_role_is_user": (OPUS, [
        {"role": "developer", "content": "treated as user"},
        {"role": "assistant", "content": "ok"},
    ]),
    "empty_tool_call_entry_skipped": (OPUS, [
        {"role": "user", "content": "q"},
        {"role": "assistant", "content": "a", "tool_calls": [{}, tc("c1", "t")]},
        {"role": "tool", "tool_call_id": "c1", "content": "r"},
    ]),
}


def image_eviction_cases():
    def shot(i):
        return [
            {"role": "assistant", "content": None, "tool_calls": [tc(f"c{i}", "screenshot")]},
            {"role": "tool", "tool_call_id": f"c{i}", "content": {"_multimodal": True, "text_summary": f"s{i}", "content": [
                {"type": "image_url", "image_url": {"url": f"data:image/png;base64,IMG{i}"}}]}},
        ]

    cases = {}
    for n, uploads in ((15, 0), (21, 0), (30, 0), (30, 5), (45, 0)):
        msgs = [{"role": "user", "content": [{"type": "image_url", "image_url": {"url": f"data:image/png;base64,UP{u}"}} for u in range(uploads)] or "go"}]
        for i in range(n):
            msgs += shot(i)
        cases[f"image_eviction_{n}_shots_{uploads}_uploads"] = (OPUS, msgs)
    return cases


def run_case(model, messages):
    system, out = convert_messages_to_anthropic(json.loads(json.dumps(messages)), None, model)
    return {"model": model, "messages": messages, "expected_system": system, "expected_messages": out}


TOOLS = {
    "plain": [
        {"type": "function", "function": {"name": "read_file", "description": "Read a file", "parameters": {
            "type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]}}},
    ],
    "duplicates_and_cache": [
        {"type": "function", "function": {"name": "a", "description": "first", "parameters": {"type": "object", "properties": {}}}, "cache_control": EPH},
        {"type": "function", "function": {"name": "a", "description": "second", "parameters": {"type": "object", "properties": {}}}},
        {"type": "function", "function": {"name": "b"}},
    ],
    "nullable_unions": [
        {"type": "function", "function": {"name": "n", "description": "d", "parameters": {
            "type": "object",
            "properties": {
                "opt": {"anyOf": [{"type": "string"}, {"type": "null"}], "description": "outer", "default": "x"},
                "nested": {"type": "object", "properties": {"inner": {"oneOf": [{"type": "integer"}, {"type": "null"}], "title": "T"}}},
                "multi": {"anyOf": [{"type": "string"}, {"type": "integer"}, {"type": "null"}]},
                "ref": {"anyOf": [{"$ref": "#/$defs/x"}, {"type": "null"}], "default": None},
                "list": {"type": "array", "items": {"anyOf": [{"type": "string"}, {"type": "null"}]}},
            }}}},
    ],
    "top_level_combinators": [
        {"type": "function", "function": {"name": "c1", "description": "d", "parameters": {"anyOf": [{"type": "object"}, {"type": "string"}]}}},
        {"type": "function", "function": {"name": "c2", "description": "d", "parameters": {"type": "object", "oneOf": [{"required": ["a"]}], "properties": {"a": {"type": "string"}}}}},
        {"type": "function", "function": {"name": "c3", "description": "d", "parameters": {"type": "object"}}},
        {"type": "function", "function": {"name": "c4", "description": "d", "parameters": {}}},
        {"type": "function", "function": {"name": "c5", "description": "d", "parameters": None}},
    ],
}

MODEL_NAMES = [
    "claude-opus-4.6", "anthropic/claude-sonnet-4.5", "Anthropic/Claude-Haiku-4.5", "claude-3.5-sonnet",
    "gpt-5.4", "qwen3.5-plus", "claude-opus-4-20250514", "CLAUDE-OPUS-4.5", "anthropic/gpt-5.4", "",
]
THINKING_MODELS = [
    "claude-opus-4-5", "claude-opus-4-4", "claude-opus-4", "claude-opus-4-20250514", "claude-opus-4-1", "claude-opus-5-5",
    "claude-sonnet-4-6", "claude-sonnet-4-5", "claude-sonnet-5-5", "claude-haiku-4-5", "claude-3-5-sonnet", "claude-3",
    "claude-fable-5-1", "claude-mythos-4", "claude-fable-4-9", "claude-future-9", "claude", "gpt-5", "", "anthropic/claude-opus-4.5",
    "claude-opus-4-5-20251101", "claude-sonnet-4.6", "claude-opus-4_5", "claude-sonnet-45",
]


def retire_cases():
    carriers = [[], [1], [1] * 20, [1] * 21, [1] * 30, [1] * 45, [1] * 100, [2] * 15, [3, 1, 4, 1, 5, 9, 2, 6, 5, 3, 5, 8, 9, 7, 9, 3, 2, 3, 8, 4, 6],
                [5] * 5, [1] * 10 + [8] * 10]
    out = []
    for c in carriers:
        for reserved in (0, 1, 5, 19, 20, 21, 40):
            out.append({"carriers": c, "reserved": reserved, "want": outbound_image_retire_count(c, reserved)})
    return out


def main(outdir: str) -> None:
    out = pathlib.Path(outdir)
    out.mkdir(parents=True, exist_ok=True)
    for old in out.glob("*.json"):
        old.unlink()
    cases = {**CASES, **image_eviction_cases()}
    for name, (model, messages) in cases.items():
        (out / f"convert_{name}.json").write_text(json.dumps(run_case(model, messages), indent=1, sort_keys=True) + "\n")
    for name, tools in TOOLS.items():
        (out / f"tools_{name}.json").write_text(
            json.dumps({"tools": tools, "expected": convert_tools_to_anthropic(json.loads(json.dumps(tools)))}, indent=1, sort_keys=True) + "\n")
    (out / "models.json").write_text(json.dumps({
        "normalize": [{"in": m, "out": normalize_model_name(m)} for m in MODEL_NAMES],
        "preserves_prior_thinking": [{"model": m, "want": model_preserves_prior_thinking(m)} for m in THINKING_MODELS],
        "image_retire": retire_cases(),
    }, indent=1) + "\n")
    print(f"wrote {len(cases)} convert, {len(TOOLS)} tools fixtures to {out}")


if __name__ == "__main__":
    main(sys.argv[1])
