"""``persist_home_channel`` writes ``platforms.<name>.home_channel`` to config.yaml (the /sethome sink)."""

import pytest

from gateway.config import HomeChannel, Platform, persist_home_channel


@pytest.fixture
def config_file(tmp_path, monkeypatch):
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    return home / "config.yaml"


def _load():
    from hermes_cli.config import load_config

    return load_config()


def test_persists_home_channel_with_thread_id(config_file):
    persist_home_channel(HomeChannel(Platform.TELEGRAM, "-1001", "Ops", thread_id="42"))

    stored = _load()["platforms"]["telegram"]["home_channel"]
    assert stored == {"platform": "telegram", "chat_id": "-1001", "name": "Ops", "thread_id": "42"}


def test_new_platform_is_not_enabled_unless_asked(config_file):
    """A relay-fronted adapter must not be falsely enabled by setting its home."""
    persist_home_channel(HomeChannel(Platform.SLACK, "C1", "general"))
    assert "enabled" not in _load()["platforms"]["slack"]


def test_enabled_if_new_enables_only_when_unset(config_file):
    persist_home_channel(HomeChannel(Platform.DISCORD, "123", "main"), enabled_if_new=True)
    assert _load()["platforms"]["discord"]["enabled"] is True

    from hermes_cli.config import load_config, save_config

    cfg = load_config()
    cfg["platforms"]["discord"]["enabled"] = False
    save_config(cfg)
    persist_home_channel(HomeChannel(Platform.DISCORD, "456", "other"), enabled_if_new=True)

    stored = _load()["platforms"]["discord"]
    assert stored["enabled"] is False  # an explicit operator choice is never overridden
    assert stored["home_channel"]["chat_id"] == "456"


def test_non_dict_platform_slot_is_replaced(config_file):
    from hermes_cli.config import load_config, save_config

    cfg = load_config()
    cfg["platforms"] = {"telegram": "garbage"}
    save_config(cfg)

    persist_home_channel(HomeChannel(Platform.TELEGRAM, "1", "x"))

    assert _load()["platforms"]["telegram"]["home_channel"]["chat_id"] == "1"
