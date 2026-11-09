"""Bot deliveries must not pin the caller's obsolete dependency generation."""
import sys

import pytest

from tools import bot_relay


@pytest.fixture
def launchers(tmp_path, monkeypatch):
    root = tmp_path / "source install"
    module = root / "tools" / "bot_relay.py"
    module.parent.mkdir(parents=True)
    module.touch()
    monkeypatch.setattr(bot_relay, "__file__", str(module))
    old_bin = tmp_path / "old generation" / "bin"
    old_bin.mkdir(parents=True)
    sibling = old_bin / "hermes"
    sibling.touch()
    monkeypatch.setattr(sys, "executable", str(old_bin / "python"))
    published = root / ".hermes" / "bin" / "hermes"
    published.parent.mkdir(parents=True)
    return published, sibling


def test_delivery_prefers_install_launcher_over_old_generation(launchers):
    published, sibling = launchers
    published.touch()
    argv = bot_relay.local_delivery_command("researcher", "message with spaces.txt")
    assert argv[0] == str(published)
    assert str(sibling) not in argv
    assert argv[1:] == ["-p", "researcher", *bot_relay.BOT_CHAT_TURN_ARGS,
                       "--query-file", "message with spaces.txt"]


def test_unpublished_install_retains_interpreter_sibling(launchers):
    _, sibling = launchers
    assert bot_relay.local_delivery_command("default", "body.txt")[0] == str(sibling)


def test_path_then_bare_fallback_remain_available(launchers, monkeypatch):
    _, sibling = launchers
    sibling.unlink()
    monkeypatch.setattr(bot_relay.shutil, "which", lambda name: "external-hermes")
    assert bot_relay._hermes_cli() == "external-hermes"
    monkeypatch.setattr(bot_relay.shutil, "which", lambda name: None)
    assert bot_relay._hermes_cli() == "hermes"


def test_launcher_shape_preserves_profile_and_lock(tmp_path, monkeypatch):
    import contextlib
    from tools import bot_mode_dm, bot_mode_probe

    home = tmp_path / "home"
    target = home / "profiles" / "researcher"
    monkeypatch.setattr(bot_mode_dm, "_default_home", lambda: str(home))
    monkeypatch.setattr(bot_mode_probe, "_hermes_root", lambda path: home)
    monkeypatch.setattr(bot_mode_probe, "_roster", lambda root: [("researcher", target)])
    locked = []
    @contextlib.contextmanager
    def lock(root, profile):
        locked.append((root, profile))
        yield
    monkeypatch.setattr(bot_relay, "acquire_turn_lock", lock)
    argv = [str(tmp_path / "hermes"), "-p", "researcher", "chat"]
    assert bot_mode_dm._local_delivery_home(argv) == target
    with bot_mode_dm._delivery_lock(argv, stdin_file=False):
        assert locked == [(home, "researcher")]
    with bot_mode_dm._delivery_lock(argv, stdin_file=True):
        assert locked == [(home, "researcher")]
