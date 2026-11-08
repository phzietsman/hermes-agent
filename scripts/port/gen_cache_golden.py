"""Generate golden fixtures for the Go prompt-cache planner by running the Python planner.

usage (from the repo root, with the test environment's interpreter):
    python scripts/port/gen_cache_golden.py internal/agent/testdata

Each case is a conversation, planner options and the stable prefixes registered before planning; the
expected value is what `agent.prompt_caching.build_prompt_cache_plan` produced for the native Anthropic
route, and what it produced when its own output is planned again (the planner must be idempotent).
"""
import copy
import json
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2]))

from agent import prompt_cache_boundary as boundary  # noqa: E402
from agent.prompt_caching import build_prompt_cache_plan  # noqa: E402

STATIC = "You are Hermes.\n\nStable guidance."
SYSTEM = STATIC + "\n\nVolatile: skills, memory, date."
SCAFFOLD = "[IMPORTANT: The user has invoked the \"deploy\" skill. The full skill content is loaded below.]\n\nSteps: 1 2 3\n\n"

TOOLS = [{"type": "function", "function": {"name": n, "description": f"{n} tool", "parameters": {"type": "object", "properties": {}}}}
         for n in ("read_file", "terminal", "web_search")]


def tc(i):
    return {"id": i, "type": "function", "function": {"name": "t", "arguments": "{}"}}


def sys_(c=SYSTEM):
    return {"role": "system", "content": c}


def u(c):
    return {"role": "user", "content": c}


def a(c, calls=None):
    m = {"role": "assistant", "content": c}
    if calls:
        m["tool_calls"] = [tc(i) for i in calls]
    return m


def t(i, c="result"):
    return {"role": "tool", "tool_call_id": i, "content": c}


def convo(n):
    out = []
    for i in range(n):
        out += [u(f"question {i}"), a(f"answer {i}")]
    return out


CONVERSATIONS = {
    "empty": [],
    "system_only": [sys_()],
    "user_only": [u("hi")],
    "no_system_short": [u("hi"), a("hello"), u("more")],
    "no_system_long": convo(4) + [u("last")],
    "simple": [sys_(), u("hi")],
    "two_turns": [sys_(), u("q1"), a("a1"), u("q2")],
    "long_chat": [sys_()] + convo(5) + [u("final question")],
    "long_chat_ends_assistant": [sys_()] + convo(5),
    "tool_run": [sys_(), u("do it"), a(None, ["c1"]), t("c1"), a("done"), u("thanks")],
    "parallel_tool_run": [sys_(), u("do it"), a("on it", ["c1", "c2", "c3"]), t("c1"), t("c2"), t("c3"), a("all done")],
    "tool_run_unfinished": [sys_(), u("do it"), a(None, ["c1"])],
    "tool_run_then_open_user": [sys_(), u("go"), a(None, ["c1"]), t("c1", "r"), a("ok"), u("next")],
    "two_tool_runs": [sys_(), u("a"), a(None, ["c1"]), t("c1"), a(None, ["c2"]), t("c2"), a("finished")],
    "three_tool_runs": [sys_(), u("a"), a(None, ["c1"]), t("c1"), a(None, ["c2"]), t("c2"), a(None, ["c3"]), t("c3")],
    "empty_assistant_at_end": [sys_(), u("a"), a("b"), u("c"), a("")],
    "none_assistant_with_calls_end": [sys_(), u("a"), a(None, ["c1"]), t("c1")],
    "list_content_user": [sys_(), u([{"type": "text", "text": "part one"}, {"type": "text", "text": "part two"}]), a("ok"), u([{"type": "text", "text": "again"}])],
    "list_content_with_image": [sys_(), u([{"type": "text", "text": "look"}, {"type": "image_url", "image_url": {"url": "data:image/png;base64,AA"}}]), a("seen"), u("and?")],
    "empty_list_content": [sys_(), u([]), a("x"), u("y")],
    "tool_multimodal": [sys_(), u("shot"), a(None, ["c1"]), {"role": "tool", "tool_call_id": "c1", "content": {"_multimodal": True, "text_summary": "s", "content": []}}],
    "system_is_list": [{"role": "system", "content": [{"type": "text", "text": "sys a"}, {"type": "text", "text": "sys b"}]}, u("hi"), a("yo"), u("again")],
    "system_equals_static": [sys_(STATIC), u("hi")],
    "system_blank_suffix": [sys_(STATIC + "   \n"), u("hi")],
    "system_unrelated_to_prefix": [sys_("A completely different system prompt."), u("hi"), a("yo"), u("q")],
    "assistant_tool_calls_with_text": [sys_(), u("x"), a("thinking out loud", ["c1"]), t("c1", "r1"), a("final")],
    "tool_result_long_text": [sys_(), u("x"), a(None, ["c1", "c2"]), t("c1", "x" * 200), t("c2", "y" * 200)],
    "premarked_input": [sys_(), {"role": "user", "content": [{"type": "text", "text": "q", "cache_control": {"type": "ephemeral"}}]},
                        {"role": "assistant", "content": "a", "cache_control": {"type": "ephemeral"}}, u("again")],
    "many_system_then_user": [sys_(), sys_("second system"), u("hi")],
    "scaffold_last": [sys_(), u("earlier"), a("ok"), u(SCAFFOLD + "do it now please")],
    "scaffold_earlier": [sys_(), u(SCAFFOLD + "first request"), a("done"), u("plain"), a("ok"), u("plain 2"), a("ok 2"), u("plain 3")],
    "scaffold_with_tool_run": [sys_(), u(SCAFFOLD + "go"), a(None, ["c1"]), t("c1"), a("fin")],
    "scaffold_blank_tail": [sys_(), u(SCAFFOLD + "  ")],
    "scaffold_exact": [sys_(), u(SCAFFOLD)],
}


def plan(messages, tools, ttl, prefix, direct):
    p = build_prompt_cache_plan(copy.deepcopy(messages), copy.deepcopy(tools), cache_ttl=ttl, native_anthropic=True,
                                static_system_prefix=prefix, direct_native_tool_cache=direct)
    return p.messages, p.tools


def main(outdir):
    out = pathlib.Path(outdir)
    out.mkdir(parents=True, exist_ok=True)
    cases = []
    for name, msgs in CONVERSATIONS.items():
        for tools_on in (False, True):
            for ttl in ("5m", "1h"):
                for prefix in (None, STATIC):
                    boundary._prefixes.clear()
                    boundary.register_stable_prefix(SCAFFOLD)
                    direct = tools_on
                    tools = TOOLS if tools_on else []
                    m1, t1 = plan(msgs, tools, ttl, prefix, direct)
                    m2, t2 = plan(m1, t1, ttl, prefix, direct)  # idempotence: plan the plan
                    cases.append({"name": f"{name}|tools={tools_on}|ttl={ttl}|prefix={'yes' if prefix else 'no'}",
                                  "messages": msgs, "tools": tools, "ttl": ttl, "prefix": prefix, "direct": direct,
                                  "scaffold": SCAFFOLD, "expected": {"messages": m1, "tools": t1},
                                  "expected_replan": {"messages": m2, "tools": t2}})
    # tools present but the direct layout off: the legacy layout must apply and the tools stay unmarked
    for name in ("two_turns", "tool_run"):
        boundary._prefixes.clear()
        m1, t1 = plan(CONVERSATIONS[name], TOOLS, "5m", STATIC, False)
        m2, t2 = plan(m1, t1, "5m", STATIC, False)
        cases.append({"name": f"{name}|tools=True|direct=False", "messages": CONVERSATIONS[name], "tools": TOOLS, "ttl": "5m",
                      "prefix": STATIC, "direct": False, "scaffold": SCAFFOLD, "expected": {"messages": m1, "tools": t1},
                      "expected_replan": {"messages": m2, "tools": t2}})
    (out / "cache.json").write_text(json.dumps(cases, indent=1, sort_keys=True) + "\n")
    print(f"wrote {len(cases)} planner cases to {out}")


if __name__ == "__main__":
    main(sys.argv[1])
