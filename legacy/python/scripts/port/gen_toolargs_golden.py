"""Golden fixtures for Go RepairToolArguments from Python's _repair_tool_call_arguments.

usage: python scripts/port/gen_toolargs_golden.py internal/agent/testdata
"""
import json
import logging
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2]))
logging.disable(logging.CRITICAL)

from agent.message_sanitization import _repair_tool_call_arguments  # noqa: E402

INPUTS = [
    "", "   ", "None", "{}", '{"a": 1}', '{"a": 1,}', '{"a": [1, 2,]}', '{"a": 1', '{"a": [1, 2', '{"a": {"b": 1}',
    '{"items": [{"n": 1}, {"n": 2', '{"a": [{"b": 1}}', '{"a": [{"b": 1}, {"c": 2}}', '{"code": "}"}', '{"code": "}',
    '{"s": "line1\nline2"}', '{"s": "tab\there"}', '{"s": "a\nb", "t": [1,', '{"a": 1}}', '{"a": 1}}}', '[1, 2, 3]]',
    '{"a": "x\\"y"}', '{"a": "x\\"y', '{"a": 1} trailing', 'not json', '{', '[', '{"a":', '{"a": tru', '{"path": "/tmp/x", "content": "hello',
    '{"a": [1, 2, 3]', '{"a": {"b": {"c": [1, {"d": 2', '{"a": 1,\n}', '{"a": [\n1,\n2,\n]\n}', ' {"a": 1} ', '"just a string"', "42", "null",
    '{"unicode": "café 日本"}', '{"unicode": "café', '{"a": 1, "b": "new\nline", ', '{"a": "\\u00e9"}', '{"a": "ctrl\x01char"}',
    '{"a": ["x", "y",], "b": {"c": 1,},}', '{"a": 1]', '{"a": [1}', '{"a": "}"', '{"a": "]"', '{"a":"b"}{"c":"d"}', "{'a': 1}",
]


def main(outdir):
    out = pathlib.Path(outdir)
    out.mkdir(parents=True, exist_ok=True)
    cases = [{"input": i, "output": _repair_tool_call_arguments(i)} for i in INPUTS]
    (out / "toolargs.json").write_text(json.dumps(cases, indent=1, ensure_ascii=True) + "\n")
    print(f"wrote {len(cases)} toolargs cases")


if __name__ == "__main__":
    main(sys.argv[1])
