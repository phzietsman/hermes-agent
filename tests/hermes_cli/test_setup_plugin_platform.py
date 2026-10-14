"""Tests for plugin-platform gateway configuration via `hermes setup gateway` UI.

Covers the full plugin-platform discovery → status → configure flow so that
a fresh Hermes install (no state, no env vars) can set up a plugin platform through the
interactive setup menus.
"""

import os

from gateway.platform_registry import PlatformEntry, platform_registry


def _register_exampleplat_plugin(**overrides):
    """Manually register the Example platform entry as if discover_plugins() found it.

    Tests run outside the normal plugin-discovery path, so we inject the entry
    directly into the singleton registry and yield its dict shape.
    """
    defaults = dict(
        name="exampleplat",
        label="Example Platform",
        adapter_factory=lambda cfg: None,
        check_fn=lambda: bool(os.getenv("EXAMPLEPLAT_SERVER", "") and os.getenv("EXAMPLEPLAT_CHANNEL", "")),
        validate_config=None,
        required_env=["EXAMPLEPLAT_SERVER", "EXAMPLEPLAT_CHANNEL", "EXAMPLEPLAT_NICKNAME"],
        install_hint="No extra packages needed (stdlib only)",
        setup_fn=lambda: None,
        source="plugin",
        plugin_name="exampleplat_plugin",
        allowed_users_env="EXAMPLEPLAT_ALLOWED_USERS",
        allow_all_env="EXAMPLEPLAT_ALLOW_ALL_USERS",
        max_message_length=450,
        pii_safe=False,
        emoji="💬",
        allow_update_command=True,
        platform_hint="You are chatting via Example.",
    )
    defaults.update(overrides)
    entry = PlatformEntry(**defaults)
    platform_registry.register(entry)
    return {
        "key": entry.name,
        "label": entry.label,
        "emoji": entry.emoji,
        "token_var": entry.required_env[0] if entry.required_env else "",
        "install_hint": entry.install_hint,
        "_registry_entry": entry,
    }


def _unregister_exampleplat_plugin():
    platform_registry.unregister("exampleplat")


# ── Fresh-install discovery ─────────────────────────────────────────────────


class TestPluginPlatformFreshInstallDiscovery:
    """Example appears in the setup menu on a brand-new Hermes install."""

    def test_example_appears_in_all_platforms(self, monkeypatch):
        """When the Example plugin is registered, _all_platforms() surfaces it."""
        import hermes_cli.gateway as gateway_mod

        _register_exampleplat_plugin()
        try:
            # Ensure no stale env vars leak in
            for key in ("EXAMPLEPLAT_SERVER", "EXAMPLEPLAT_CHANNEL", "EXAMPLEPLAT_NICKNAME"):
                monkeypatch.delenv(key, raising=False)

            platforms = gateway_mod._all_platforms()
            keys = {p["key"] for p in platforms}
            assert "exampleplat" in keys
        finally:
            _unregister_exampleplat_plugin()


    def test_example_status_configured_when_env_set(self, monkeypatch):
        """After the user sets EXAMPLEPLAT_SERVER and EXAMPLEPLAT_CHANNEL, status is 'configured'."""
        import hermes_cli.gateway as gateway_mod

        plat = _register_exampleplat_plugin()
        try:
            monkeypatch.setenv("EXAMPLEPLAT_SERVER", "example.invalid")
            monkeypatch.setenv("EXAMPLEPLAT_CHANNEL", "hermes-room")
            monkeypatch.setenv("EXAMPLEPLAT_NICKNAME", "hermes-bot")

            status = gateway_mod._platform_status(plat)
            assert status == "configured"
        finally:
            _unregister_exampleplat_plugin()


# ── Interactive setup dispatch ──────────────────────────────────────────────


class TestPluginPlatformInteractiveSetup:
    """The setup UI dispatches to Example's interactive_setup() correctly."""

    def test_configure_platform_dispatches_to_example_setup_fn(self, monkeypatch, capsys):
        """_configure_platform() calls the Example plugin's setup_fn when selected."""
        import hermes_cli.gateway as gateway_mod

        calls = []

        def fake_setup():
            calls.append("setup_called")
            print("Example setup complete!")

        plat = _register_exampleplat_plugin(setup_fn=fake_setup)
        try:
            gateway_mod._configure_platform(plat)
        finally:
            _unregister_exampleplat_plugin()

        assert "setup_called" in calls
        out = capsys.readouterr().out
        assert "Example setup complete!" in out


    def test_configure_platform_fallback_when_no_setup_fn(self, monkeypatch, capsys):
        """A plugin with no setup_fn falls back to env-var instructions."""
        import hermes_cli.gateway as gateway_mod

        plat = _register_exampleplat_plugin(setup_fn=None)
        try:
            gateway_mod._configure_platform(plat)
        finally:
            _unregister_exampleplat_plugin()

        out = capsys.readouterr().out
        assert "Example Platform" in out
        assert "EXAMPLEPLAT_SERVER" in out


# ── End-to-end fresh-install gateway setup ──────────────────────────────────


class TestPluginPlatformGatewaySetupFreshInstall:
    """Simulate the full `hermes setup gateway` experience with Example present."""

    def test_setup_gateway_shows_example_in_platform_menu(self, monkeypatch, capsys, tmp_path):
        """The gateway setup menu lists Example among the available platforms."""
        import hermes_cli.gateway as gateway_mod
        from hermes_cli import setup as setup_mod

        monkeypatch.setenv("HERMES_HOME", str(tmp_path))
        _register_exampleplat_plugin()
        try:
            for key in ("EXAMPLEPLAT_SERVER", "EXAMPLEPLAT_CHANNEL", "EXAMPLEPLAT_NICKNAME"):
                monkeypatch.delenv(key, raising=False)

            # Sanity-check: Example must be visible to _all_platforms()
            platforms = gateway_mod._all_platforms()
            assert any(p["key"] == "exampleplat" for p in platforms), \
                f"Example not in platforms: {[p['key'] for p in platforms]}"

            # Capture what prompt_checklist is asked to display
            checklist_calls = []

            def capture_prompt_checklist(question, choices, pre_selected=None):
                checklist_calls.append({"question": question, "choices": choices})
                return []  # nothing selected → clean exit

            monkeypatch.setattr(setup_mod, "prompt_yes_no", lambda *a, **kw: False)
            monkeypatch.setattr(setup_mod, "prompt_checklist", capture_prompt_checklist)
            monkeypatch.setattr(gateway_mod, "supports_systemd_services", lambda: False)
            monkeypatch.setattr(gateway_mod, "is_macos", lambda: False)
            monkeypatch.setattr(gateway_mod, "_is_service_installed", lambda: False)
            monkeypatch.setattr(gateway_mod, "_is_service_running", lambda: False)

            setup_mod.setup_gateway({})

            # Find the platform-selection prompt
            platform_prompt = next(
                (c for c in checklist_calls if "platform" in c["question"].lower()),
                None,
            )
            assert platform_prompt is not None, \
                f"No platform prompt found in {checklist_calls}"
            choices_text = "\n".join(platform_prompt["choices"])
            assert "Example Platform" in choices_text
            assert "not configured" in choices_text.lower()
        finally:
            _unregister_exampleplat_plugin()

    def test_setup_gateway_example_counts_as_messaging_platform(self, monkeypatch, capsys, tmp_path):
        """When Example is configured, setup_gateway counts it as a messaging platform."""
        import hermes_cli.gateway as gateway_mod
        from hermes_cli import setup as setup_mod

        monkeypatch.setenv("HERMES_HOME", str(tmp_path))
        _register_exampleplat_plugin()
        try:
            monkeypatch.setenv("EXAMPLEPLAT_SERVER", "example.invalid")
            monkeypatch.setenv("EXAMPLEPLAT_CHANNEL", "hermes-room")
            monkeypatch.setenv("EXAMPLEPLAT_NICKNAME", "hermes-bot")

            monkeypatch.setattr(setup_mod, "prompt_yes_no", lambda *a, **kw: False)
            monkeypatch.setattr(setup_mod, "prompt_choice", lambda *a, **kw: 0)
            # Select ONLY the Example row. Without this, the non-TTY checklist
            # falls back to its cancel value (the pre-selected "configured"
            # platforms) — on a dev machine with real platforms configured
            # that runs their interactive setup_fn, which calls input() and
            # dies under captured stdin. Example's setup_fn is a no-op lambda.
            monkeypatch.setattr(
                setup_mod,
                "prompt_checklist",
                lambda title, items, pre=None: [
                    i for i, item in enumerate(items) if "Example Platform" in item
                ],
            )
            monkeypatch.setattr(gateway_mod, "supports_systemd_services", lambda: False)
            monkeypatch.setattr(gateway_mod, "is_macos", lambda: False)
            monkeypatch.setattr(gateway_mod, "_is_service_installed", lambda: False)
            monkeypatch.setattr(gateway_mod, "_is_service_running", lambda: False)

            setup_mod.setup_gateway({})

            out = capsys.readouterr().out
            assert "Messaging platforms configured!" in out
        finally:
            _unregister_exampleplat_plugin()
