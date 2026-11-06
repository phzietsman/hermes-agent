"""``--clone`` carries the active provider's per-home config, including agent_memory's dashed filename."""
from hermes_cli.profile_memory_config import clone_memory_provider_config


def test_agent_memory_config_file_is_cloned_owner_only(tmp_path):
    source, clone = tmp_path / "src", tmp_path / "clone"
    source.mkdir()
    clone.mkdir()
    (source / "agent-memory.json").write_text('{"endpoint": "https://x.example"}', encoding="utf-8")

    assert clone_memory_provider_config(source, clone, "agent_memory") is True
    assert (clone / "agent-memory.json").read_text(encoding="utf-8") == '{"endpoint": "https://x.example"}'
    assert (clone / "agent-memory.json").stat().st_mode & 0o777 == 0o600


def test_nothing_copied_when_the_provider_has_no_config(tmp_path):
    (tmp_path / "src").mkdir()
    (tmp_path / "clone").mkdir()
    assert clone_memory_provider_config(tmp_path / "src", tmp_path / "clone", "agent_memory") is False
