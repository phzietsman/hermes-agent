"""Dump the compaction texts verbatim and generate golden fixtures for the Go compressor.

usage (from the repo root, with the test environment's interpreter):
    python scripts/port/gen_compaction_golden.py internal/agent

Writes prompttext/compaction.json (embedded by the Go package) and testdata/compaction.json: trigger and tail
budgets, summary prompts, tool-result stubs, summarizer input rendering, elision and token estimates, all
produced by the real Python implementation.
"""
import hashlib
import json
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2]))

import agent.context_compressor as cc  # noqa: E402
from agent.compression_marker import _ELISION_MARKER_TEMPLATE, elide, elide_middle  # noqa: E402
from agent.model_metadata import estimate_tokens_rough  # noqa: E402

CC = cc.ContextCompressor


def texts():
    return {
        "summary_prefix": cc.SUMMARY_PREFIX,
        "legacy_summary_prefix": cc.LEGACY_SUMMARY_PREFIX,
        "historical_task_heading": cc.HISTORICAL_TASK_HEADING,
        "end_marker": cc._SUMMARY_END_MARKER,
        "compression_note": CC._COMPRESSION_NOTE,
        "merged_prior_context_header": cc._MERGED_PRIOR_CONTEXT_HEADER,
        "merged_summary_delimiter": cc._MERGED_SUMMARY_DELIMITER,
        "pruned_tool_placeholder": cc._PRUNED_TOOL_PLACEHOLDER,
        "no_user_task_sentinel": cc._NO_USER_TASK_SENTINEL,
        "continuation_user_content": cc.COMPRESSION_CONTINUATION_USER_CONTENT,
        "elision_marker_template": _ELISION_MARKER_TEMPLATE,
        "lean_session_log_section": cc._LEAN_SESSION_LOG_SECTION,
        "lean_session_log_budget_tokens": cc._LEAN_SESSION_LOG_BUDGET_TOKENS,
        "pruned_skills_section_heading": cc._PRUNED_SKILLS_SECTION_HEADING,
        "lean_user_messages_heading": cc._LEAN_USER_MESSAGES_HEADING,
        "lean_recovery_heading": cc._LEAN_RECOVERY_HEADING,
        "section_instructions": {("true" if k else "false"): v for k, v in cc._SECTION_INSTRUCTIONS.items()},
    }


def thresholds():
    out = []
    for ctx in (8_000, 32_000, 64_000, 100_000, 128_000, 200_000, 400_000, 512_000, 1_000_000):
        for pct in (0.5, 0.75, 0.9):
            for max_tokens in (None, 4096, 32_000, 64_000):
                eff = CC._effective_threshold_percent(ctx, pct)
                out.append({"ctx": ctx, "pct": pct, "max_tokens": max_tokens, "effective_pct": eff,
                            "threshold": CC._compute_threshold_tokens(ctx, eff, max_tokens)})
    return out


def tail_budgets():
    out = []
    for ctx in (8_000, 32_000, 64_000, 128_000, 200_000, 1_000_000):
        c = object.__new__(CC)
        c._tail_token_budget = None
        c.tail_mode = "lean"
        c._resolved_context_length = ctx
        c._threshold_tokens = None
        out.append({"ctx": ctx, "tail_budget": c.tail_token_budget})
    return out


def prompts():
    cc._today_for_prompt = lambda: "2026-03-15"
    out = []
    for prev in (None, "## Goal\nOld goal\n"):
        for has_user in (True, False):
            for focus in (None, "the auth refactor"):
                c = object.__new__(CC)
                c._previous_summary = prev
                c.tail_mode = "lean"
                text = c._build_summary_prompt("[USER]: do the thing\n\n[ASSISTANT]: done", 3000, focus, "", has_user)
                out.append({"previous": prev, "has_user_turn": has_user, "focus": focus, "budget": 3000, "prompt": text})
    c = object.__new__(CC)
    c._previous_summary = "x" * 200_000
    c.tail_mode = "lean"
    # A huge previous summary is bounded; store the digest, the Go test rebuilds the input from its length.
    big = c._build_summary_prompt("[USER]: hi", 2000, None, "", True)
    out.append({"previous_x_len": 200_000, "has_user_turn": True, "focus": None, "budget": 2000,
                "prompt_len": len(big), "prompt_sha256": hashlib.sha256(big.encode()).hexdigest()})
    return out


STUB_CASES = [
    ("terminal", {"command": "ls -la"}, '{"output": "a\\nb", "exit_code": 0}' + " " * 300),
    ("terminal", {"command": "x" * 120}, '{"exit_code": 127}' + "\n" * 5 + " " * 300),
    ("read_file", {"path": "/a/b.py", "offset": 10}, "line\n" * 100),
    ("read_file", {"path": "/a/b.py"}, '{"error": "no such file"}' + " " * 300),
    ("write_file", {"path": "/a.txt", "content": "a\nb\nc"}, '{"ok": true}' + " " * 300),
    ("write_file", {"path": "/a.txt", "content": "a"}, '{"error": "stale write"}' + " " * 300),
    ("patch", {"path": "/a.py", "mode": "patch"}, "diff\n" * 80),
    ("patch", {"path": "/a.py"}, "diff\n" * 80),
    ("search_files", {"pattern": "foo", "path": "src", "target": "files"}, '{"total_count": 12}' + " " * 300),
    ("search_files", {"pattern": "foo"}, "no json " * 60),
    ("web_search", {"query": "go generics"}, "result " * 100),
    ("unknown_tool", {"alpha": "x" * 80, "beta": 2, "gamma": 3}, "out " * 120),
    ("unknown_tool", {}, '{"success": false, "error": "rate limited\\nplease wait"}' + " " * 300),
    ("terminal", {"command": "rm -rf build"}, "BLOCKED: denied by user. NOT consented"),
    ("terminal", {"command": "ls"}, '{"status": "pending_approval"}'),
    ("write_file", {"path": "/etc/x"}, '{"status": "blocked", "error": "BLOCKED: sensitive path"}'),
]


def stubs():
    out = []
    for name, args, content in STUB_CASES:
        out.append({"name": name, "args": json.dumps(args), "content": content,
                    "stub": cc._summarize_tool_result(name, json.dumps(args), content)})
    out.append({"name": "terminal", "args": "not json", "content": "x" * 300, "stub": cc._summarize_tool_result("terminal", "not json", "x" * 300)})
    return out


def serialize():
    c = object.__new__(CC)
    turns = [
        {"role": "user", "content": "hello there"},
        {"role": "assistant", "content": "x" * 7000, "tool_calls": [
            {"id": "a", "type": "function", "function": {"name": "terminal", "arguments": json.dumps({"command": "ls"})}},
            {"id": "b", "type": "function", "function": {"name": "read_file", "arguments": json.dumps({"path": "p" * 2000})}}]},
        {"role": "tool", "tool_call_id": "a", "content": "y" * 9000},
        {"role": "tool", "tool_call_id": "b", "content": "short"},
        {"role": "assistant", "content": "<think>secret plan</think>Visible answer"},
        {"role": "user", "content": ""},
    ]
    return {"turns": turns, "rendered": c._serialize_for_summary(turns)}


def elisions():
    out = []
    for n, limit in ((100, 50), (100, 200), (5000, 300), (5000, 20), (1_234_567, 400)):
        text = ("abcdefghij" * (n // 10 + 1))[:n]
        out.append({"pattern": "abcdefghij", "n": n, "limit": limit, "elided": elide(text, limit)})
    for n, head, tail in ((100, 20, 20), (5000, 4000, 1500), (7000, 4000, 1500), (123_456, 100, 100)):
        text = ("0123456789" * (n // 10 + 1))[:n]
        out.append({"pattern": "0123456789", "n": n, "head": head, "tail": tail, "elided_middle": elide_middle(text, head, tail)})
    return out


def estimates():
    samples = ["", "a", "abcd", "abcde", "hello world" * 50, "日本語のテキスト", "Привет мир" * 10, "mixed 日本 text é", "x" * 1000]
    return [{"text": s, "tokens": estimate_tokens_rough(s)} for s in samples]


def verbatim_user_section():
    turns = [{"role": "user", "content": "first request"}, {"role": "assistant", "content": "ok"},
             {"role": "user", "content": "[System: scaffolding]"}, {"role": "user", "content": "second " * 1000},
             {"role": "user", "content": "third"}, {"role": "user", "content": "   "}]
    return {"turns": turns, "section": cc._build_verbatim_user_section(turns),
            "recovery": cc._build_recovery_footer("20260315_x", 7), "recovery_empty": cc._build_recovery_footer("", 7)}


def main(outdir):
    out = pathlib.Path(outdir)
    (out / "prompttext").mkdir(parents=True, exist_ok=True)
    (out / "testdata").mkdir(parents=True, exist_ok=True)
    (out / "prompttext" / "compaction.json").write_text(json.dumps(texts(), indent=1, sort_keys=True, ensure_ascii=False) + "\n", encoding="utf-8")
    golden = {"thresholds": thresholds(), "tail_budgets": tail_budgets(), "prompts": prompts(), "stubs": stubs(),
              "serialize": serialize(), "elisions": elisions(), "estimates": estimates(), "user_section": verbatim_user_section()}
    (out / "testdata" / "compaction.json").write_text(json.dumps(golden, indent=1, sort_keys=True, ensure_ascii=True) + "\n")
    print("wrote compaction texts and goldens")


if __name__ == "__main__":
    main(sys.argv[1])
