"""Generate golden fixtures for the Go Anthropic stream accumulator and response normaliser.

usage (from the repo root, with the test environment's interpreter):
    python scripts/port/gen_anthropic_stream_golden.py internal/provider/anthropic/testdata

Each scenario is a list of server-sent events. They are replayed through the real Anthropic SDK
(0.87, via an httpx mock transport) and Hermes's own `_stream_final_message` and
`AnthropicTransport.normalize_response`, so the Go port is compared with what Python produced from
the same bytes. The SSE text is stored next to the expected result.
"""
import json
import pathlib
import sys

import httpx

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2]))

import anthropic  # noqa: E402

from agent.anthropic_adapter import _stream_final_message  # noqa: E402
from agent.transports.anthropic import AnthropicTransport  # noqa: E402


def usage(i=10, o=1, cr=0, cw=0):
    return {"input_tokens": i, "output_tokens": o, "cache_read_input_tokens": cr, "cache_creation_input_tokens": cw}


def ms(u=None, model="claude-opus-4-5", mid="msg_01"):
    return ("message_start", {"type": "message_start", "message": {
        "id": mid, "type": "message", "role": "assistant", "model": model, "content": [], "stop_reason": None,
        "stop_sequence": None, "usage": u or usage()}})


def cbs(i, block):
    return ("content_block_start", {"type": "content_block_start", "index": i, "content_block": block})


def cbd(i, delta):
    return ("content_block_delta", {"type": "content_block_delta", "index": i, "delta": delta})


def cbx(i):
    return ("content_block_stop", {"type": "content_block_stop", "index": i})


def md(stop, o=5, extra_delta=None, **u):
    delta = {"stop_reason": stop, "stop_sequence": None, **(extra_delta or {})}
    return ("message_delta", {"type": "message_delta", "delta": delta, "usage": {"output_tokens": o, **u}})


STOP = ("message_stop", {"type": "message_stop"})
PING = ("ping", {"type": "ping"})


def text(i, *parts):
    return [cbs(i, {"type": "text", "text": ""}), *[cbd(i, {"type": "text_delta", "text": p}) for p in parts], cbx(i)]


def thinking(i, body, sig="sig-1"):
    return [cbs(i, {"type": "thinking", "thinking": "", "signature": ""}),
            *[cbd(i, {"type": "thinking_delta", "thinking": p}) for p in body],
            cbd(i, {"type": "signature_delta", "signature": sig}), cbx(i)]


def tool(i, tid, name, *json_parts):
    return [cbs(i, {"type": "tool_use", "id": tid, "name": name, "input": {}}),
            *[cbd(i, {"type": "input_json_delta", "partial_json": p}) for p in json_parts], cbx(i)]


SCENARIOS = {
    "plain_text": [ms(), PING, *text(0, "Hel", "lo ", "world"), md("end_turn", 3), STOP],
    "empty_content_end_turn": [ms(), md("end_turn", 0), STOP],
    "two_text_blocks_joined": [ms(), *text(0, "first"), *text(1, "second"), md("end_turn"), STOP],
    "thinking_then_text": [ms(), *thinking(0, ["Let me ", "think."], "sigA"), *text(1, "Answer"), md("end_turn"), STOP],
    "thinking_unsigned_empty_signature": [ms(), *thinking(0, ["hm"], ""), *text(1, "ok"), md("end_turn"), STOP],
    "interleaved_thinking_tool": [
        ms(), *thinking(0, ["plan A"], "s0"), *text(1, "I will read it."), *tool(2, "toolu_1", "read_file", '{"pa', 'th": "/a.txt"}'),
        *thinking(3, ["plan B"], "s1"), *tool(4, "toolu_2", "terminal", '{"cmd":', ' "ls -la"}'), md("tool_use", 40), STOP],
    "thinking_with_tool_unsigned": [
        ms(), *thinking(0, ["no sig"], ""), *tool(1, "toolu_1", "t", '{"a":1}'), md("tool_use"), STOP],
    "parallel_tools_split_json": [
        ms(), *tool(0, "toolu_a", "one", '{"x"', ':', '[1,2,', '3]}'), *tool(1, "toolu_b", "two", '{"s": "he said \\"hi\\""}'),
        md("tool_use", 20), STOP],
    "tool_use_empty_input": [ms(), *tool(0, "toolu_e", "noargs"), md("tool_use"), STOP],
    "tool_use_unicode_args": [ms(), *tool(0, "toolu_u", "say", '{"t": "caf\\u00e9 日本"}'), md("tool_use"), STOP],
    "redacted_thinking": [
        ms(), cbs(0, {"type": "redacted_thinking", "data": "opaque-blob"}), cbx(0), *text(1, "done"), md("end_turn"), STOP],
    "redacted_with_tool": [
        ms(), cbs(0, {"type": "redacted_thinking", "data": "blob"}), cbx(0), *tool(1, "toolu_r", "t", '{}'), md("tool_use"), STOP],
    "cache_usage": [ms(usage(100, 1, 5000, 300)), *text(0, "x"), md("end_turn", 77), STOP],
    "usage_in_delta_updates_input": [ms(usage(1, 1)), *text(0, "x"), md("end_turn", 9, input_tokens=222, cache_read_input_tokens=11), STOP],
    "max_tokens": [ms(), *text(0, "cut off mid"), md("max_tokens", 4096), STOP],
    "stop_sequence": [ms(), *text(0, "x"), ("message_delta", {"type": "message_delta", "delta": {"stop_reason": "stop_sequence", "stop_sequence": "END"}, "usage": {"output_tokens": 2}}), STOP],
    "refusal_with_details": [
        ms(), md("refusal", 0, extra_delta={"stop_details": {"type": "refusal", "category": "cyber", "explanation": "no"}}), STOP],
    "context_window_exceeded": [ms(), *text(0, "x"), md("model_context_window_exceeded"), STOP],
    "citations": [
        ms(), cbs(0, {"type": "text", "text": "", "citations": []}), cbd(0, {"type": "text_delta", "text": "cited"}),
        cbd(0, {"type": "citations_delta", "citation": {"type": "char_location", "cited_text": "c", "document_index": 0,
                                                       "document_title": "d", "start_char_index": 0, "end_char_index": 1}}),
        cbx(0), md("end_turn"), STOP],
    "server_tool_blocks_dropped_from_ordered": [
        ms(), *thinking(0, ["t"], "s"), cbs(1, {"type": "server_tool_use", "id": "srv_1", "name": "web_search", "input": {}}),
        cbd(1, {"type": "input_json_delta", "partial_json": '{"query": "x"}'}), cbx(1),
        *tool(2, "toolu_9", "read_file", '{"p": 1}'), md("tool_use"), STOP],
    "pings_everywhere": [PING, ms(), PING, *text(0, "a"), PING, md("end_turn"), PING, STOP],
    "text_before_tool_no_thinking": [ms(), *text(0, "Let me check"), *tool(1, "toolu_t", "ls", '{"d": "."}'), md("tool_use"), STOP],
    "whitespace_text_kept_in_content": [ms(), *text(0, "  "), *tool(1, "toolu_w", "x", '{}'), md("tool_use"), STOP],
}


def sse_text(events):
    return "".join(f"event: {name}\ndata: {json.dumps(data)}\n\n" for name, data in events)


def run(sse: str):
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, headers={"content-type": "text/event-stream"}, content=sse.encode())

    client = anthropic.Anthropic(api_key="test", base_url="http://test", max_retries=0,
                                 http_client=httpx.Client(transport=httpx.MockTransport(handler)))
    message = _stream_final_message(client.messages.stream, dict(model="claude-opus-4-5", max_tokens=100,
                                                                  messages=[{"role": "user", "content": "x"}]), "", None, None)
    transport = AnthropicTransport()
    norm = transport.normalize_response(message)
    u = message.usage
    return {
        "stop_reason": message.stop_reason,
        "usage": {"input": u.input_tokens, "output": u.output_tokens,
                  "cache_read": getattr(u, "cache_read_input_tokens", 0) or 0,
                  "cache_write": getattr(u, "cache_creation_input_tokens", 0) or 0},
        "valid": transport.validate_response(message),
        "normalized": {
            "content": norm.content,
            "tool_calls": [{"id": t.id, "name": t.name, "arguments": json.loads(t.arguments)} for t in (norm.tool_calls or [])],
            "finish_reason": norm.finish_reason,
            "reasoning": norm.reasoning,
            "provider_data": norm.provider_data,
        },
    }


def main(outdir: str) -> None:
    out = pathlib.Path(outdir)
    out.mkdir(parents=True, exist_ok=True)
    cases = []
    for name, events in SCENARIOS.items():
        sse = sse_text(events)
        cases.append({"name": name, "sse": sse, "expected": run(sse)})
    (out / "stream.json").write_text(json.dumps(cases, indent=1, sort_keys=True) + "\n")
    print(f"wrote {len(cases)} stream fixtures to {out}")


if __name__ == "__main__":
    main(sys.argv[1])
