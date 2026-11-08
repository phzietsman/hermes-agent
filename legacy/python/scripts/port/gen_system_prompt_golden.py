"""Generate golden fixtures for the Go system-prompt assembly by running Python's build_system_prompt_parts.

usage (from the repo root, with the test environment's interpreter):
    python scripts/port/gen_system_prompt_golden.py internal/agent/testdata

The agent is a SimpleNamespace carrying only the fields the prompt reads. Features the Go port does not
carry (coding posture and workspace snapshot, bot mode, environment probe and hints, auto-load skills,
plugin sections, kanban guidance, the alibaba identity line) are stubbed out so the comparison covers
exactly the ported surface.
"""
import json
import os
import pathlib
import shutil
import sys
import tempfile
from datetime import datetime
from types import SimpleNamespace
from zoneinfo import ZoneInfo

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2]))

import hermes_cli.config as hcfg  # noqa: E402

hcfg.ensure_hermes_home = lambda *a, **k: None

import hermes_constants  # noqa: E402
import hermes_time  # noqa: E402
import yaml  # noqa: E402
from agent import prompt_builder as pb  # noqa: E402
from agent import system_prompt as sp  # noqa: E402

CORE = ["terminal", "read_file", "write_file", "patch", "search_files", "web_search", "web_extract"]
SKILLS = ["skills_list", "skill_view", "skill_manage"]
MEM = ["session_search", "agent_memory_recall"]
NOW = "2026-03-15T09:30:00"

CASES = []


def case(name, **kw):
    base = dict(name=name, files={}, cfg={}, tools=CORE, toolsets=["terminal", "file", "web"], platform="cli",
                model="claude-opus-4-5", provider="anthropic", session_id="20260315_081500_ab12cd", root_session_id=None,
                pass_session_id=False, system_message=None, skip_context_files=False, load_soul=False, ctx_len=None,
                enforcement=None, execution=None, task_completion=None, parallel=None, hints=None, rich=False,
                cron_delivery="", now=NOW, zone="UTC", session_start=None, memory="", home_kind="default")
    base.update(kw)
    CASES.append(base)


case("minimal_no_tools", tools=[], toolsets=[])
case("core_tools_cli")
case("core_tools_telegram", platform="telegram")
case("core_tools_discord", platform="discord")
case("unknown_platform", platform="myplat")
case("empty_platform", platform="")
case("platform_case_insensitive", platform="  Telegram ")
case("session_search_guidance", tools=CORE + MEM)
case("skills_tools_with_index", tools=CORE + SKILLS + MEM, toolsets=["terminal", "file", "skills"],
     files={"home/skills/dev/git/SKILL.md": "---\nname: git\ndescription: Git workflows\n---\nB\n",
            "home/skills/dev/DESCRIPTION.md": "---\ndescription: Dev tools\n---\n"})
case("skills_tools_hermes_agent_skill_switches_help_variant", tools=CORE + SKILLS,
     files={"home/skills/hermes-agent/SKILL.md": "---\nname: hermes-agent\ndescription: Operating manual\n---\nB\n"})
case("skill_view_only_no_hermes_agent_skill", tools=CORE + ["skill_view"],
     files={"home/skills/a/SKILL.md": "---\nname: a\ndescription: A\n---\nB\n"})
case("skills_guidance_needs_skill_manage", tools=CORE + ["skills_list"])
case("skills_empty_dir", tools=CORE + SKILLS)
case("skills_platform_disabled", tools=CORE + SKILLS, platform="telegram",
     cfg={"skills": {"platform_disabled": {"telegram": ["b"]}}},
     files={"home/skills/t/a/SKILL.md": "---\nname: a\ndescription: A\n---\nB\n", "home/skills/t/b/SKILL.md": "---\nname: b\ndescription: B\n---\nB\n"})
case("model_gpt_gets_enforcement_and_execution", model="gpt-5", provider="openai")
case("model_gemini_gets_google_guidance", model="gemini-2.5-pro", provider="google")
case("model_deepseek_execution_only_models", model="deepseek-v3", provider="deepseek")
case("enforcement_forced_on", enforcement=True)
case("enforcement_forced_off_for_gpt", model="gpt-5", enforcement=False)
case("enforcement_string_false", model="gpt-5", enforcement="off")
case("enforcement_list", model="claude-opus-4-5", enforcement=["claude"])
case("enforcement_list_miss", model="gpt-5", enforcement=["claude"])
case("execution_forced_on", execution=True)
case("execution_forced_off", model="gpt-5", execution=False)
case("execution_filtered_by_tools", model="gpt-5", tools=["terminal", "web_search"], toolsets=["terminal", "web"])
case("task_completion_off", task_completion=False)
case("parallel_off", parallel=False)
case("both_universal_off", task_completion=False, parallel=False)
case("delegate_task_async_handoff", tools=CORE + ["delegate_task"])
case("system_message_in_context_tier", system_message="Extra instructions from the caller.")
case("system_message_blank_dropped", system_message="   ")
case("system_message_with_project_context", system_message="Caller text.", files={"proj/AGENTS.md": "Project rules.\n"})
case("project_context_agents", files={"proj/AGENTS.md": "Use tabs.\n"})
case("project_context_skipped", skip_context_files=True, files={"proj/AGENTS.md": "Use tabs.\n"})
case("soul_replaces_default_identity", files={"home/SOUL.md": "I am a careful agent.\n"})
case("soul_with_skip_context_but_load_soul", skip_context_files=True, load_soul=True, files={"home/SOUL.md": "Soul only.\n", "proj/AGENTS.md": "ignored\n"})
case("soul_skipped_when_skip_context_files", skip_context_files=True, files={"home/SOUL.md": "Not loaded.\n"})
case("soul_and_project_context", files={"home/SOUL.md": "Identity.\n", "proj/AGENTS.md": "Rules.\n"})
case("context_truncated_by_ctx_len", ctx_len=8000, files={"proj/AGENTS.md": "## H\n" + ("lorem ipsum " * 4000)})
case("memory_block", memory="## Memory\n- user likes tea")
case("memory_block_blank_dropped", memory="  \n")
case("pass_session_id", pass_session_id=True)
case("no_provider", provider="")
case("session_id_without_stamp", session_id="plainid", session_start="2026-03-10T12:00:00")
case("session_id_without_stamp_no_start", session_id="plainid")
case("started_earlier_day_adds_second_line", session_id="20260301_101010_zz")
case("root_session_stamp_wins", session_id="20260315_081500_new", root_session_id="20260101_000000_root")
case("zone_new_york_winter", zone="America/New_York", now="2026-01-15T09:30:00", session_id="20260115_081500_x")
case("zone_new_york_summer", zone="America/New_York", now="2026-07-15T09:30:00", session_id="20260715_081500_x")
case("zone_johannesburg", zone="Africa/Johannesburg")
case("zone_kolkata_half_hour", zone="Asia/Kolkata")
case("zone_numeric_abbrev", zone="America/Sao_Paulo")
case("zone_utc_midnight_boundary", now="2026-03-15T00:00:00", session_id="20260314_235959_x")
case("hint_replace", platform="telegram", hints={"telegram": {"replace": "Custom telegram hint."}})
case("hint_append", platform="telegram", hints={"telegram": {"append": "Extra advice."}})
case("hint_string_shorthand_append", platform="discord", hints={"discord": "Shorthand text."})
case("hint_replace_and_append", platform="slack", hints={"slack": {"replace": "R", "append": "A"}})
case("hint_blank_override_falls_back", platform="telegram", hints={"telegram": {"replace": "  ", "append": ""}})
case("hint_override_other_platform_ignored", platform="telegram", hints={"discord": {"replace": "nope"}})
case("hint_override_for_unknown_platform", platform="myplat", hints={"myplat": {"append": "Only this."}})
case("telegram_rich_messages", platform="telegram", rich=True)
case("cron_platform", platform="cron")
case("cron_with_delivery_telegram", platform="cron", cron_delivery="telegram")
case("cron_with_delivery_cron_ignored", platform="cron", cron_delivery="cron")
case("cron_delivery_with_override", platform="cron", cron_delivery="discord", hints={"discord": {"append": "Tail."}})
case("profile_named", home_kind="profile", files={"proj/AGENTS.md": "x\n"})
case("profile_named_skills", home_kind="profile", tools=CORE + SKILLS,
     files={"home/skills/a/SKILL.md": "---\nname: a\ndescription: A\n---\nB\n"})
case("all_features", tools=CORE + SKILLS + MEM + ["delegate_task"], toolsets=["terminal", "file", "web", "skills"],
     platform="telegram", rich=True, model="gpt-5", system_message="Caller.", memory="## Memory\n- a fact", pass_session_id=True,
     zone="Africa/Johannesburg", session_id="20260301_101010_zz", ctx_len=200000,
     files={"home/SOUL.md": "Identity.\n", "proj/AGENTS.md": "Rules.\n",
            "home/skills/hermes-agent/SKILL.md": "---\nname: hermes-agent\ndescription: Manual\n---\nB\n"})


def materialize(base, files):
    for rel, content in files.items():
        p = base / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(content, encoding="utf-8")
    (base / "proj").mkdir(exist_ok=True)


def run(c):
    base = pathlib.Path(tempfile.mkdtemp()).resolve()
    try:
        if c["home_kind"] == "profile":
            home = base / "root" / "profiles" / "work"
            files = {k.replace("home/", "root/profiles/work/", 1): v for k, v in c["files"].items()}
        else:
            home = base / "home"
            files = c["files"]
        materialize(base, files)
        home.mkdir(parents=True, exist_ok=True)
        if c["cfg"]:
            (home / "config.yaml").write_text(yaml.safe_dump(c["cfg"]))
        os.environ["HERMES_HOME"] = str(home)
        os.environ.pop("HERMES_PLATFORM", None)
        os.environ.pop("HERMES_SESSION_PLATFORM", None)
        os.environ["HERMES_CRON_AUTO_DELIVER_PLATFORM"] = c["cron_delivery"]
        if c["platform"]:
            os.environ["HERMES_PLATFORM"] = c["platform"]
        hermes_constants.set_hermes_home_override(str(home))
        zone = ZoneInfo(c["zone"])
        now = datetime.fromisoformat(c["now"]).replace(tzinfo=zone)
        hermes_time.now = lambda: now
        hermes_time.get_timezone = lambda: zone
        sp._coding_parts = lambda a: ([], [], [])
        sp._bot_mode_parts = lambda a: []
        sp._auto_load_parts = lambda a: []
        sp._frozen_plugin_prompt_sections = lambda a: ()
        sp._memory_parts = lambda a: [a._mem] if a._mem else []
        sp._telegram_rich_messages_enabled = lambda: c["rich"]
        sp._post_workspace_parts = lambda a: [sp.platform_hint(a)]
        sp._pb.build_environment_hints = lambda: ""
        sp.resolve_context_cwd = lambda include_session_override=True: str(base / "proj")
        home_p = home

        def skills_prompt(a):
            if not any(n in a.valid_tool_names for n in SKILLS):
                return ""
            pb.clear_skills_system_prompt_cache(clear_snapshot=True)
            return pb.build_skills_system_prompt(available_tools=a.valid_tool_names, available_toolsets=set(c["toolsets"]),
                                                 compact_categories=None, skills_dir_override=home_p / "skills")
        sp._skills_prompt = skills_prompt
        root = c["root_session_id"]
        agent = SimpleNamespace(
            valid_tool_names=set(c["tools"]), platform=c["platform"], model=c["model"], provider=c["provider"],
            session_id=c["session_id"], pass_session_id=c["pass_session_id"], load_soul_identity=c["load_soul"],
            skip_context_files=c["skip_context_files"], _tool_use_enforcement=c["enforcement"] if c["enforcement"] is not None else "auto",
            _execution_guidance=c["execution"] if c["execution"] is not None else "auto",
            _task_completion_guidance=True if c["task_completion"] is None else c["task_completion"],
            _parallel_tool_call_guidance=True if c["parallel"] is None else c["parallel"],
            _platform_hint_overrides=c["hints"], _memory_manager=None, _mem=c["memory"], _kanban_worker_guidance="",
            context_compressor=SimpleNamespace(context_length=c["ctx_len"]) if c["ctx_len"] else None,
            _session_db=SimpleNamespace(get_conversation_root=lambda sid: root, db_path=None) if root else None,
            session_start=datetime.fromisoformat(c["session_start"]).replace(tzinfo=zone) if c["session_start"] else None,
            tools=[], _environment_probe=False, _bot_mode_protocol=False)
        pb.drain_truncation_warnings()
        parts = sp.build_system_prompt_parts(agent, system_message=c["system_message"])
        warnings = pb.drain_truncation_warnings()
        fix = lambda s: s.replace(str(base), "<BASE>")
        return {k: fix(v) for k, v in parts.items()}, [fix(w) for w in warnings]
    finally:
        hermes_constants.set_hermes_home_override(None) if hasattr(hermes_constants, "set_hermes_home_override") else None
        shutil.rmtree(base, ignore_errors=True)


def main(outdir):
    out = pathlib.Path(outdir)
    out.mkdir(parents=True, exist_ok=True)
    cases = []
    for c in CASES:
        parts, warnings = run(c)
        cases.append({**c, "expected": parts, "expected_warnings": warnings})
    (out / "system_prompt.json").write_text(json.dumps(cases, indent=1, sort_keys=True, ensure_ascii=True) + "\n")
    print(f"wrote {len(cases)} system-prompt cases to {out}")


if __name__ == "__main__":
    main(sys.argv[1])
