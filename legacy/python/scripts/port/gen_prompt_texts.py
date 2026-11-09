"""Dump the system-prompt texts from agent/prompt_builder.py into an embedded JSON file, verbatim.

usage (from the repo root, with the test environment's interpreter):
    python scripts/port/gen_prompt_texts.py internal/agent/prompttext

Writes texts.json (what the Go package embeds) and execution_guidance_golden.json (what Python's
execution_guidance_text returned for several toolsets, to test the Go port of it).
"""
import json
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2]))

from agent import prompt_builder as pb  # noqa: E402

# Platforms the Go port serves. The desktop and TUI hints belong to TypeScript frontends that are out of scope.
KEPT_PLATFORMS = ("api_server", "cli", "cron", "discord", "email", "slack", "telegram", "whatsapp", "whatsapp_cloud")


def main(outdir: str) -> None:
    out = pathlib.Path(outdir)
    out.mkdir(parents=True, exist_ok=True)
    texts = {
        "default_agent_identity": pb.DEFAULT_AGENT_IDENTITY,
        "help_guidance": pb.HERMES_AGENT_HELP_GUIDANCE,
        "help_guidance_no_skills": pb.HERMES_AGENT_HELP_GUIDANCE_NO_SKILLS,
        "session_search_guidance": pb.SESSION_SEARCH_GUIDANCE,
        "skills_guidance": pb.SKILLS_GUIDANCE,
        "skill_safety_heading": pb.SKILL_SAFETY_HEADING,
        "tool_use_enforcement_guidance": pb.TOOL_USE_ENFORCEMENT_GUIDANCE,
        "tool_use_enforcement_models": list(pb.TOOL_USE_ENFORCEMENT_MODELS),
        "execution_guidance_models": list(pb.EXECUTION_GUIDANCE_MODELS),
        "execution_guidance": pb.OPENAI_MODEL_EXECUTION_GUIDANCE,
        "execution_guidance_line_tools": [[line, sorted(tools)] for line, tools in pb._EXECUTION_GUIDANCE_LINE_TOOLS.items()],
        "task_completion_guidance": pb.TASK_COMPLETION_GUIDANCE,
        "async_handoff_guidance": pb.ASYNC_HANDOFF_GUIDANCE,
        "parallel_tool_call_guidance": pb.PARALLEL_TOOL_CALL_GUIDANCE,
        "steer_channel_note": pb.STEER_CHANNEL_NOTE,
        "google_model_operational_guidance": pb.GOOGLE_MODEL_OPERATIONAL_GUIDANCE,
        "telegram_rich_messages_hint": pb.TELEGRAM_RICH_MESSAGES_HINT,
        "runtime_environment_heading": pb.RUNTIME_ENVIRONMENT_HEADING,
        "runtime_environment_end": pb.RUNTIME_ENVIRONMENT_END,
        "platform_hints": {k: pb.PLATFORM_HINTS[k] for k in KEPT_PLATFORMS},
    }
    (out / "texts.json").write_text(json.dumps(texts, indent=1, sort_keys=True, ensure_ascii=False) + "\n", encoding="utf-8")
    toolsets = [None, [], ["terminal"], ["read_file"], ["search_files"], ["execute_code"], ["terminal", "read_file"],
                ["web_search"], ["session_search", "skill_view"], ["terminal", "execute_code", "read_file", "search_files"]]
    golden = [{"tools": ts, "text": pb.execution_guidance_text(ts)} for ts in toolsets]
    (out / "execution_guidance_golden.json").write_text(json.dumps(golden, indent=1, ensure_ascii=False) + "\n", encoding="utf-8")
    print(f"wrote {len(texts)} texts and {len(golden)} execution-guidance cases to {out}")


if __name__ == "__main__":
    main(sys.argv[1])
