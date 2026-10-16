"""Skill content hashes agree between the on-disk and in-memory bundle paths."""

from pathlib import Path


class TestSkillHashSymmetry:
    """#62310 — disk hash and bundle hash must agree."""

    def _make_skill(self, root: Path) -> Path:
        skill = root / "demo-skill"
        (skill / "references" / "methods").mkdir(parents=True)
        (skill / "SKILL.md").write_text("---\nname: demo\n---\nbody\n", encoding="utf-8")
        (skill / "references" / "methods" / "x.md").write_text("x\n", encoding="utf-8")
        # Mixed-case name exercises the case-sensitive sort divergence.
        (skill / "Zeta.md").write_text("z\n", encoding="utf-8")
        return skill

    def test_disk_and_bundle_hashes_match(self, tmp_path):
        from tools.skills_guard import content_hash
        from tools.skills_hub_install import bundle_content_hash
        from tools.skills_hub_models import SkillBundle

        skill = self._make_skill(tmp_path)
        disk = content_hash(skill)

        files = {}
        for f in skill.rglob("*"):
            if f.is_file():
                files[str(f.relative_to(skill))] = f.read_bytes()
        bundle = SkillBundle(
            name="demo-skill", files=files, source="test",
            identifier="test/demo-skill", trust_level="community",
        )
        assert bundle_content_hash(bundle) == disk

    def test_backslash_and_posix_keys_hash_identically(self):
        from tools.skills_hub_install import bundle_content_hash
        from tools.skills_hub_models import SkillBundle

        posix = SkillBundle(
            name="s",
            files={"references/a.md": b"a", "SKILL.md": b"s"},
            source="test",
            identifier="t/s",
            trust_level="community",
        )
        backslash = SkillBundle(
            name="s",
            files={"references\\a.md": b"a", "SKILL.md": b"s"},
            source="test",
            identifier="t/s",
            trust_level="community",
        )
        assert bundle_content_hash(posix) == bundle_content_hash(backslash)
