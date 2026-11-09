"""Tests for hermes_cli.relaunch — unified self-relaunch utility."""

import sys

import pytest

from hermes_cli import relaunch as relaunch_mod


class TestResolveHermesBin:
    def test_prefers_absolute_argv0_when_executable(self, monkeypatch):
        fake = "/nix/store/abc/bin/hermes"
        monkeypatch.setattr(sys, "argv", [fake])
        monkeypatch.setattr(relaunch_mod.os.path, "isfile", lambda p: p == fake)
        monkeypatch.setattr(relaunch_mod.os, "access", lambda p, mode: p == fake)
        assert relaunch_mod.resolve_hermes_bin() == fake

    def test_resolves_relative_argv0(self, monkeypatch, tmp_path):
        fake = tmp_path / "hermes"
        fake.write_text("#!/bin/sh\n")
        fake.chmod(0o755)
        monkeypatch.setattr(sys, "argv", [str(fake.name)])
        monkeypatch.chdir(tmp_path)
        # Ensure we don't accidentally match a real 'hermes' on PATH
        monkeypatch.setattr(relaunch_mod.shutil, "which", lambda _name: None)
        assert relaunch_mod.resolve_hermes_bin() == str(fake)

    def test_falls_back_to_path_which(self, monkeypatch):
        monkeypatch.setattr(sys, "argv", ["-c"])  # not a real path
        monkeypatch.setattr(
            relaunch_mod.shutil, "which", lambda name: "/usr/bin/hermes" if name == "hermes" else None
        )
        assert relaunch_mod.resolve_hermes_bin() == "/usr/bin/hermes"


class TestExtractInheritedFlags:
    def test_extracts_tui_and_dev(self):
        argv = ["--tui", "--dev", "chat"]
        assert relaunch_mod._extract_inherited_flags(argv) == ["--tui", "--dev"]


    def test_preserves_multiple_skills(self):
        argv = ["-s", "foo", "-s", "bar", "--tui"]
        assert relaunch_mod._extract_inherited_flags(argv) == ["-s", "foo", "-s", "bar", "--tui"]


class TestInheritedFlagTable:
    """Sanity-check the argparse-introspected table that drives extraction."""


    def test_excluded_flags_are_not_inherited(self):
        table = dict(relaunch_mod._INHERITED_FLAGS_TABLE)
        # --worktree creates a new worktree per process; inheriting would
        # orphan the parent's. Chat-only flags (--quiet/-Q, --verbose/-v,
        # --source) can't be in argv at the existing relaunch callsites.
        for flag in ["-w", "--worktree", "-Q", "--quiet", "-v", "--verbose", "--source"]:
            assert flag not in table, f"{flag} should not be inherited"


class TestBuildRelaunchArgv:


    def test_preserves_inherited_flags(self, monkeypatch):
        monkeypatch.setattr(relaunch_mod, "resolve_hermes_bin", lambda: "/usr/bin/hermes")
        original = ["--tui", "--dev", "--profile", "work", "sessions", "browse"]
        argv = relaunch_mod.build_relaunch_argv(["--resume", "abc"], original_argv=original)
        assert "--tui" in argv
        assert "--dev" in argv
        assert "--profile" in argv
        assert "work" in argv
        assert "--resume" in argv
        assert "abc" in argv
        # The original subcommand should not survive
        assert "sessions" not in argv
        assert "browse" not in argv

    def test_can_disable_preserve(self, monkeypatch):
        monkeypatch.setattr(relaunch_mod, "resolve_hermes_bin", lambda: "/usr/bin/hermes")
        original = ["--tui", "chat"]
        argv = relaunch_mod.build_relaunch_argv(
            ["--resume", "abc"], preserve_inherited=False, original_argv=original
        )
        assert "--tui" not in argv
        assert argv == ["/usr/bin/hermes", "--resume", "abc"]


class TestRelaunch:
    def test_calls_execvp(self, monkeypatch):
        calls = []

        def fake_execvp(path, argv):
            calls.append((path, argv))
            raise SystemExit(0)

        monkeypatch.setattr(relaunch_mod.os, "execvp", fake_execvp)
        monkeypatch.setattr(relaunch_mod, "resolve_hermes_bin", lambda: "/usr/bin/hermes")

        with pytest.raises(SystemExit):
            relaunch_mod.relaunch(["--resume", "abc"])

        assert calls == [("/usr/bin/hermes", ["/usr/bin/hermes", "--resume", "abc"])]

class TestResolveHermesBinPythonLauncherGuard:
    """resolve_hermes_bin never returns a Python launcher whose shebang would leave the venv."""

    def test_posix_python_launcher_falls_through_to_path(self, monkeypatch, tmp_path):
        """A Python source launcher must not be re-executed through its shebang.

        The git installer invokes ``hermes`` with the venv interpreter while the
        source launcher's shebang uses ``/usr/bin/env python3``.  Re-executing
        that source file directly can therefore escape the venv and lose core
        dependencies such as PyYAML.
        """
        script = tmp_path / "hermes"
        script.write_text("#!/usr/bin/env python3\n")
        script.chmod(0o755)
        wrapper = tmp_path / "bin" / "hermes"
        wrapper.parent.mkdir()
        wrapper.write_text("#!/usr/bin/env bash\n")
        wrapper.chmod(0o755)
        monkeypatch.setattr(relaunch_mod.sys, "argv", [str(script), "chat"])
        monkeypatch.setattr(
            relaunch_mod.shutil, "which",
            lambda name: str(wrapper) if name == "hermes" else None,
        )
        assert relaunch_mod.resolve_hermes_bin() == str(wrapper)

    def test_posix_python_launcher_on_path_falls_back_to_current_python(
        self, monkeypatch, tmp_path
    ):
        """PATH must not re-select the Python launcher rejected from argv[0]."""
        script = tmp_path / "hermes"
        script.write_text("#!/usr/bin/env python3\n")
        script.chmod(0o755)
        monkeypatch.setattr(relaunch_mod.sys, "argv", [str(script), "chat"])
        monkeypatch.setattr(
            relaunch_mod.shutil,
            "which",
            lambda name: str(script) if name == "hermes" else None,
        )

        assert relaunch_mod.resolve_hermes_bin() is None

        # A console script pinned to the running interpreter keeps the venv: still exec-able.
        pinned = tmp_path / "pinned" / "hermes"
        pinned.parent.mkdir()
        pinned.write_text(f"#!{sys.executable}\n")
        pinned.chmod(0o755)
        monkeypatch.setattr(relaunch_mod.sys, "argv", [str(pinned), "chat"])
        assert relaunch_mod.resolve_hermes_bin() == str(pinned)
