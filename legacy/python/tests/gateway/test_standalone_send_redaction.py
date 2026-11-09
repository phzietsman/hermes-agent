"""Invariant: a standalone sender's failure envelope never carries vendor secrets to the model.

Every ``plugins/platforms/*/adapter.py::_standalone_send`` used to build ``{"error": f"... {e}"}``
by hand; a token or signed URL inside an httpx/aiohttp exception went straight into the tool
result. They now share ``gateway.platforms._shared.send_error`` (the ``send_message`` redactor).
This pins the shared redactor on a secret-bearing error and drives real senders end-to-end.
"""

import pytest

import agent.redact as _redact
from gateway.config import PlatformConfig
from gateway.platforms._shared import send_error
from tests.gateway._plugin_adapter_loader import load_plugin_adapter

_FAKE_TOKEN = "hermes-test-bearer-credential-ABCDEFGHIJKLMNOPQRSTUVWX"
_FAKE_URL_SECRET = "https://hooks.example/send?access_token=sk_live_ABCDEF0123456789"


@pytest.fixture(autouse=True)
def _redaction_on(monkeypatch):
    # The switch is snapshotted at import; a developer shell with HERMES_REDACT_SECRETS=false must not
    # turn this contract test into a no-op.
    monkeypatch.setattr(_redact, "_REDACT_ENABLED", True)


def test_send_error_redacts_token_and_signed_url():
    out = send_error(f"upstream said 401 for Authorization: Bearer {_FAKE_TOKEN} at {_FAKE_URL_SECRET}")
    assert set(out) == {"error"}
    assert _FAKE_TOKEN not in out["error"]
    assert "sk_live_ABCDEF0123456789" not in out["error"]
    assert "401" in out["error"]  # the diagnostic shape survives


@pytest.mark.asyncio
async def test_email_standalone_failure_is_redacted(monkeypatch):
    email = load_plugin_adapter("email")
    monkeypatch.setenv("EMAIL_ADDRESS", "bot@example.com")
    monkeypatch.setenv("EMAIL_PASSWORD", "pw")
    monkeypatch.setenv("EMAIL_SMTP_HOST", "smtp.example.com")

    def _boom(*a, **k):
        raise OSError(f"connect failed: Authorization: Bearer {_FAKE_TOKEN} {_FAKE_URL_SECRET}")

    monkeypatch.setattr(email, "_open_smtp", _boom)
    result = await email._standalone_send(PlatformConfig(enabled=True, extra={}), "you@example.com", "hi")
    assert "error" in result
    assert _FAKE_TOKEN not in result["error"] and "sk_live_ABCDEF0123456789" not in result["error"]
    assert "connect failed" in result["error"]


@pytest.mark.asyncio
async def test_teams_standalone_failure_is_redacted(monkeypatch):
    aiohttp = pytest.importorskip("aiohttp")
    teams = load_plugin_adapter("teams")
    monkeypatch.setenv("TEAMS_CLIENT_ID", "cid")
    monkeypatch.setenv("TEAMS_CLIENT_SECRET", "csecret")
    monkeypatch.setenv("TEAMS_TENANT_ID", "tenant1")

    class _Boom:
        def __init__(self, *a, **k):
            raise RuntimeError(f"refused; proxy Authorization: Basic {_FAKE_TOKEN} {_FAKE_URL_SECRET}")

    monkeypatch.setattr(aiohttp, "ClientSession", _Boom)
    result = await teams._standalone_send(PlatformConfig(enabled=True, extra={}), "conv123", "hi")
    assert "error" in result
    assert _FAKE_TOKEN not in result["error"] and "sk_live_ABCDEF0123456789" not in result["error"]
    assert "refused" in result["error"]
