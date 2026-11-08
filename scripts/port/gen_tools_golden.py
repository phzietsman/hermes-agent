"""Golden fixtures for the Go tools package: argument coercion and error shaping, from the real Python.

usage: python scripts/port/gen_tools_golden.py internal/tools/testdata
"""
import json
import logging
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2]))
logging.disable(logging.CRITICAL)

import tools.arg_coercion as ac  # noqa: E402
import tools.registry as reg  # noqa: E402
from model_tools import _sanitize_tool_error  # noqa: E402

S = lambda **props: {"parameters": {"type": "object", "properties": props}}  # noqa: E731

SCHEMAS = {
    "scalars": S(n={"type": "integer"}, x={"type": "number"}, b={"type": "boolean"}, s={"type": "string"}),
    "containers": S(a={"type": "array", "items": {"type": "string"}}, o={"type": "object", "properties": {"k": {"type": "array"}}}),
    "nested": S(items={"type": "array", "items": {"type": "object", "properties": {"tags": {"type": "array"}, "n": {"type": "integer"}}}}),
    "nullable": S(n={"type": ["integer", "null"]}, s={"type": ["string", "null"]}, m={"anyOf": [{"type": "string"}, {"type": "null"}]}),
    "union": S(v={"type": ["integer", "boolean"]}, w={"type": ["number", "string"]}),
    "anyof_array": S(a={"anyOf": [{"type": "array", "items": {"type": "string"}}, {"type": "string"}]}),
    "untyped": S(u={}),
    "none": {"parameters": {"type": "object"}},
}

ARGS = {
    "scalars": [
        {"n": "42", "x": "3.5", "b": "true", "s": "42"}, {"n": "42.0"}, {"n": "3.5"}, {"n": " 7 "}, {"n": "abc"}, {"n": "1e3"}, {"n": "-0"},
        {"x": "1e-7"}, {"x": "inf"}, {"x": "nan"}, {"x": "12345678901234567890"}, {"n": "12345678901234567890"}, {"b": "FALSE"}, {"b": " True "},
        {"b": "yes"}, {"n": 5, "x": 1.5, "b": True}, {"n": "0x10"}, {"n": ""}, {"x": "1_000"}, {"n": "+5"}, {"x": ".5"}, {"unknown": "1"},
        {"n": None}, {"s": "true"},
    ],
    "containers": [
        {"a": '["x","y"]'}, {"a": "single"}, {"a": 5}, {"a": None}, {"a": ["x"]}, {"a": "[not json"}, {"a": '{"k":1}'}, {"o": '{"k":"[1,2]"}'},
        {"o": {"k": "[1,2]"}}, {"o": '["x"]'}, {"o": "text"}, {"a": '[1,2]'}, {"a": True}, {"a": ""},
    ],
    "nested": [
        {"items": '[{"tags":"[\\"a\\"]","n":"3"}]'}, {"items": [{"tags": '["a","b"]', "n": "3"}]}, {"items": ['{"tags":"x"}']}, {"items": "{}"},
        {"items": [{"tags": "plain"}]}, {"items": [1, 2]},
    ],
    "nullable": [{"n": "null"}, {"n": " NULL "}, {"n": "5"}, {"s": "null"}, {"s": "text"}, {"m": "null"}, {"m": "x"}, {"n": "nope"}],
    "union": [{"v": "5"}, {"v": "true"}, {"v": "x"}, {"w": "2.5"}, {"w": "text"}],
    "anyof_array": [{"a": '["x"]'}, {"a": "plain"}, {"a": "null"}],
    "untyped": [{"u": "5"}, {"u": "null"}],
    "none": [{"a": "1"}, {}],
}


def main(outdir):
    out = pathlib.Path(outdir)
    out.mkdir(parents=True, exist_ok=True)
    cases = []
    for sname, schema in SCHEMAS.items():
        reg.registry.get_schema = lambda name, _s=schema: _s
        for args in ARGS[sname]:
            src = json.loads(json.dumps(args))
            got = ac.coerce_tool_args("t", json.loads(json.dumps(args)))
            cases.append({"schema": schema["parameters"], "args": src, "coerced": got})
    errors = []
    for text in ["short", "x" * 3000, "<tool_call>boom</tool_call> after", "```json\n{\"a\":1}\n```", "<![CDATA[secret]]> tail", "", "plain <result>x</result>"]:
        errors.append({"text": text, "tool_error": json.loads(reg.tool_error(text)), "sanitized": _sanitize_tool_error(text)})
    bound = []
    for payload in [json.dumps({"error": "e" * 5000}), json.dumps({"error": "short"}), "not json" * 400, json.dumps({"ok": "x" * 5000}), json.dumps({"error": "x" * 5000, "extra": 1})]:
        bound.append({"in": payload, "out": reg._bound_json_error_result(payload)})
    (out / "coerce.json").write_text(json.dumps(cases, indent=1, ensure_ascii=True) + "\n")
    (out / "errors.json").write_text(json.dumps({"errors": errors, "bound": bound}, indent=1, ensure_ascii=True) + "\n")
    print(f"wrote {len(cases)} coercion cases, {len(errors)} error cases")


if __name__ == "__main__":
    main(sys.argv[1])
