"""Generate golden fixtures for the Go skills index by running Python's build_skills_system_prompt.

usage (from the repo root, with the test environment's interpreter):
    python scripts/port/gen_skills_index_golden.py internal/agent/testdata

Each case is a tree under <home>/skills (plus optional external roots under <base>/ext1, ext2), a
config.yaml, the offered tools/toolsets, the session platform and the compact categories.
"""
import json
import os
import pathlib
import shutil
import sys
import tempfile

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2]))

import yaml  # noqa: E402

from agent import prompt_builder as pb  # noqa: E402

REPO = pathlib.Path(__file__).resolve().parents[2]


def sk(name, desc=None, extra="", body="Body text.\n"):
    fm = [f"name: {name}"]
    if desc is not None:
        fm.append(f"description: {desc}")
    return "---\n" + "\n".join(fm) + ("\n" + extra if extra else "") + "\n---\n" + body


def cond(**kw):
    lines = ["metadata:", "  hermes:"]
    for k, v in kw.items():
        lines.append(f"    {k}: [{', '.join(v)}]")
    return "\n".join(lines)


CASES = []


def case(name, files=None, cfg=None, tools=None, toolsets=None, platform="", compact=None, repo=False):
    CASES.append(dict(name=name, files=files or {}, cfg=cfg or {}, tools=tools, toolsets=toolsets,
                      platform=platform, compact=compact, repo=repo))


case("empty")
case("one_flat_skill", {"home/skills/alpha/SKILL.md": sk("alpha", "Does alpha things")})
case("categories_and_descriptions", {
    "home/skills/dev/DESCRIPTION.md": "---\ndescription: 'Developer tools'\n---\n",
    "home/skills/dev/git/SKILL.md": sk("git", "Git workflows"),
    "home/skills/dev/lint/SKILL.md": sk("lint", "Linting"),
    "home/skills/ops/deploy/SKILL.md": sk("deploy", "Deploy things"),
    "home/skills/solo/SKILL.md": sk("solo", "Top-level solo skill"),
    "home/skills/DESCRIPTION.md": "---\ndescription: general stuff\n---\n",
})
case("nested_categories", {
    "home/skills/a/b/c/SKILL.md": sk("c", "deep"),
    "home/skills/a/b/DESCRIPTION.md": "---\ndescription: AB group\n---\n",
    "home/skills/a/x/SKILL.md": sk("x", "shallow"),
})
case("skill_at_root", {"home/skills/SKILL.md": sk("rootskill", "At the root")})
case("long_description_truncated", {"home/skills/t/one/SKILL.md": sk("one", "x" * 200)})
case("quoted_description", {"home/skills/t/q/SKILL.md": sk("q", "\"quoted desc\"")})
case("no_description", {"home/skills/t/nd/SKILL.md": sk("nd")})
case("unicode_description", {"home/skills/t/u/SKILL.md": sk("u", "日本語の説明 " + "é" * 80)})
case("sorted_and_deduped", {
    "home/skills/z/b/SKILL.md": sk("b", "b"), "home/skills/z/a/SKILL.md": sk("a", "a"),
    "home/skills/m/c/SKILL.md": sk("c", "c"),
})
case("name_differs_from_dir", {"home/skills/t/dirname/SKILL.md": sk("declared-name", "d")})
case("disabled_by_name", {"home/skills/t/a/SKILL.md": sk("a", "a"), "home/skills/t/b/SKILL.md": sk("b", "b")},
     cfg={"skills": {"disabled": ["a"]}})
case("disabled_by_platform", {"home/skills/t/a/SKILL.md": sk("a", "a"), "home/skills/t/b/SKILL.md": sk("b", "b")},
     cfg={"skills": {"platform_disabled": {"telegram": ["b"]}}}, platform="telegram")
case("disabled_other_platform_not_applied", {"home/skills/t/a/SKILL.md": sk("a", "a"), "home/skills/t/b/SKILL.md": sk("b", "b")},
     cfg={"skills": {"platform_disabled": {"telegram": ["b"]}}}, platform="discord")
case("disabled_all_leaves_empty", {"home/skills/t/a/SKILL.md": sk("a", "a")}, cfg={"skills": {"disabled": ["a"]}})
case("conditions_fallback_for_tool", {
    "home/skills/t/ddg/SKILL.md": sk("ddg", "fallback", cond(fallback_for_tools=["web_search"])),
    "home/skills/t/plain/SKILL.md": sk("plain", "plain")}, tools=["web_search"], toolsets=["web"])
case("conditions_fallback_shown_when_primary_missing", {
    "home/skills/t/ddg/SKILL.md": sk("ddg", "fallback", cond(fallback_for_tools=["web_search"]))}, tools=["terminal"], toolsets=["terminal"])
case("conditions_requires_tool", {
    "home/skills/t/needs/SKILL.md": sk("needs", "needs terminal", cond(requires_tools=["terminal"])),
    "home/skills/t/other/SKILL.md": sk("other", "o")}, tools=["web_search"], toolsets=["web"])
case("conditions_requires_toolset", {
    "home/skills/t/needs/SKILL.md": sk("needs", "needs toolset", cond(requires_toolsets=["browser"]))}, tools=["x"], toolsets=["browser"])
case("conditions_fallback_toolset", {
    "home/skills/t/fb/SKILL.md": sk("fb", "fb", cond(fallback_for_toolsets=["browser"]))}, tools=["x"], toolsets=["browser"])
case("conditions_ignored_without_tool_info", {
    "home/skills/t/needs/SKILL.md": sk("needs", "needs", cond(requires_tools=["terminal"]))}, tools=None, toolsets=None)
case("conditions_empty_sets_hide_requires", {
    "home/skills/t/needs/SKILL.md": sk("needs", "needs", cond(requires_tools=["terminal"]))}, tools=[], toolsets=[])
case("session_platform_match", {
    "home/skills/t/tg/SKILL.md": sk("tg", "tg only", cond(session_platforms=["Telegram"]))}, platform="telegram")
case("session_platform_mismatch", {
    "home/skills/t/tg/SKILL.md": sk("tg", "tg only", cond(session_platforms=["telegram"])),
    "home/skills/t/any/SKILL.md": sk("any", "any")}, platform="discord")
case("session_platform_unknown_fails_open", {
    "home/skills/t/tg/SKILL.md": sk("tg", "tg only", cond(session_platforms=["telegram"]))}, platform="")
case("os_gate_hides", {
    "home/skills/t/win/SKILL.md": sk("win", "windows only", "platforms: [windows]"),
    "home/skills/t/mac/SKILL.md": sk("mac", "mac only", "platforms: [macos]"),
    "home/skills/t/lin/SKILL.md": sk("lin", "linux only", "platforms: [linux]")})
case("requires_apps_hides", {"home/skills/t/app/SKILL.md": sk("app", "needs app", "requires_apps: [nonexistent-app-xyz]")})
case("no_web_tools_note", {"home/skills/t/a/SKILL.md": sk("a", "a")}, tools=["terminal"], toolsets=["terminal"])
case("web_tools_note", {"home/skills/t/a/SKILL.md": sk("a", "a")}, tools=["terminal", "web_search"], toolsets=["web"])
case("compact_categories", {
    "home/skills/dev/git/SKILL.md": sk("git", "git"), "home/skills/dev/lint/SKILL.md": sk("lint", "lint"),
    "home/skills/dev/sub/inner/SKILL.md": sk("inner", "inner"), "home/skills/ops/deploy/SKILL.md": sk("deploy", "deploy")},
     compact=["dev"])
case("compact_nested_follows_parent", {
    "home/skills/dev/a/b/SKILL.md": sk("b", "b"), "home/skills/other/c/SKILL.md": sk("c", "c")}, compact=["dev"])
case("external_dir", {
    "home/skills/t/local/SKILL.md": sk("local", "local"), "ext1/x/ext/SKILL.md": sk("ext", "external")},
     cfg={"skills": {"external_dirs": ["{BASE}/ext1"]}})
case("external_shadowed_by_local", {
    "home/skills/t/same/SKILL.md": sk("same", "local wins"), "ext1/x/same/SKILL.md": sk("same", "external loses")},
     cfg={"skills": {"external_dirs": ["{BASE}/ext1"]}})
case("external_category_description", {
    "ext1/x/DESCRIPTION.md": "---\ndescription: from external\n---\n", "ext1/x/e/SKILL.md": sk("e", "e"),
    "home/skills/x/DESCRIPTION.md": "---\ndescription: from local\n---\n", "home/skills/x/l/SKILL.md": sk("l", "l")},
     cfg={"skills": {"external_dirs": ["{BASE}/ext1"]}})
case("external_only_no_local_dir", {"ext1/x/e/SKILL.md": sk("e", "e")}, cfg={"skills": {"external_dirs": ["{BASE}/ext1"]}})
case("two_external_first_wins_description", {
    "ext1/x/DESCRIPTION.md": "---\ndescription: first\n---\n", "ext1/x/a/SKILL.md": sk("a", "a"),
    "ext2/x/DESCRIPTION.md": "---\ndescription: second\n---\n", "ext2/x/b/SKILL.md": sk("b", "b")},
     cfg={"skills": {"external_dirs": ["{BASE}/ext1", "{BASE}/ext2"]}})
case("same_tier_duplicate_ambiguous", {
    "ext1/x/dup/SKILL.md": sk("dup", "one"), "ext2/y/dup/SKILL.md": sk("dup", "two")},
     cfg={"skills": {"external_dirs": ["{BASE}/ext1", "{BASE}/ext2"]}})
case("identical_copies_dedup", {
    "home/skills/a/same/SKILL.md": sk("same", "s"), "home/skills/b/same/SKILL.md": sk("same", "s")})
case("different_same_name_local_ambiguous", {
    "home/skills/a/same/SKILL.md": sk("same", "one"), "home/skills/b/same/SKILL.md": sk("same", "two")})
case("support_dirs_not_scanned", {
    "home/skills/t/pkg/SKILL.md": sk("pkg", "pkg"), "home/skills/t/pkg/references/SKILL.md": sk("ref", "hidden"),
    "home/skills/t/pkg/scripts/inner/SKILL.md": sk("inner", "hidden")})
case("excluded_dirs_not_scanned", {
    "home/skills/t/ok/SKILL.md": sk("ok", "ok"), "home/skills/.git/x/SKILL.md": sk("git-x", "hidden"),
    "home/skills/node_modules/y/SKILL.md": sk("nm", "hidden")})
case("bom_skill", {"home/skills/t/b/SKILL.md": "﻿" + sk("b", "bom ok")})
case("frontmatter_missing", {"home/skills/t/nofm/SKILL.md": "Just prose, no frontmatter.\n"})
case("description_block_scalar", {"home/skills/t/bs/SKILL.md": "---\nname: bs\ndescription: >\n  folded text\n  here\n---\nB\n"})
case("repo_skills", repo=True)
case("repo_skills_telegram_compact", repo=True, platform="telegram", compact=["github", "mlops"],
     tools=["terminal", "read_file"], toolsets=["terminal", "file"])


def materialize(base, files):
    for rel, content in files.items():
        p = base / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(content, encoding="utf-8")
    (base / "home").mkdir(exist_ok=True)


def run(c):
    base = pathlib.Path(tempfile.mkdtemp()).resolve()
    try:
        materialize(base, c["files"])
        cfg = json.loads(json.dumps(c["cfg"]).replace("{BASE}", str(base)))
        if c["repo"]:
            (base / "home" / "skills").symlink_to(REPO / "skills")
            cfg.setdefault("skills", {})["external_dirs"] = [str(REPO / "optional-skills")]
        (base / "home" / "config.yaml").write_text(yaml.safe_dump(cfg))
        os.environ["HERMES_HOME"] = str(base / "home")
        os.environ.pop("HERMES_PLATFORM", None)
        if c["platform"]:
            os.environ["HERMES_PLATFORM"] = c["platform"]
        os.chdir(base / "home")
        pb.clear_skills_system_prompt_cache(clear_snapshot=True)
        out = pb.build_skills_system_prompt(
            available_tools=None if c["tools"] is None else set(c["tools"]),
            available_toolsets=None if c["toolsets"] is None else set(c["toolsets"]),
            compact_categories=None if c["compact"] is None else frozenset(c["compact"]),
            skills_dir_override=base / "home" / "skills")
        return out.replace(str(base), "<BASE>")
    finally:
        os.chdir(REPO)
        shutil.rmtree(base, ignore_errors=True)


def main(outdir):
    out = pathlib.Path(outdir)
    out.mkdir(parents=True, exist_ok=True)
    cases = []
    for c in CASES:
        cases.append({**c, "expected": run(c)})
    (out / "skills_index.json").write_text(json.dumps(cases, indent=1, sort_keys=True, ensure_ascii=True) + "\n")
    print(f"wrote {len(cases)} skills-index cases ({sum(1 for c in cases if c['expected'])} with output) to {out}")


if __name__ == "__main__":
    main(sys.argv[1])
