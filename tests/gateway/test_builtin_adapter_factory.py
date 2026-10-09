"""Core (non-plugin) adapter factory: ``gateway.run._instantiate_builtin_adapter``."""

from types import SimpleNamespace

import pytest

import gateway.run as gateway_run
from gateway.config import Platform


def test_unknown_platform_yields_no_adapter():
    assert gateway_run._instantiate_builtin_adapter(Platform.TELEGRAM, object()) is None


def test_unmet_requirements_warn_and_yield_no_adapter(monkeypatch, caplog):
    constructed = []
    monkeypatch.setattr(
        gateway_run, "_builtin_adapter_import",
        lambda module, name, requirement: (lambda cfg: constructed.append(cfg), lambda: False),
    )
    with caplog.at_level("WARNING"):
        assert gateway_run._instantiate_builtin_adapter(Platform.WEBHOOK, object()) is None
    assert "Webhook: aiohttp not installed" in caplog.text
    assert constructed == []


@pytest.mark.parametrize("requirements_ok", [True, lambda: True])
def test_met_requirements_construct_the_adapter_with_config(monkeypatch, requirements_ok):
    config = SimpleNamespace(tag="cfg")
    monkeypatch.setattr(
        gateway_run, "_builtin_adapter_import",
        lambda module, name, requirement: (lambda cfg: ("adapter", cfg), requirements_ok),
    )
    assert gateway_run._instantiate_builtin_adapter(Platform.API_SERVER, config) == ("adapter", config)


@pytest.mark.parametrize("platform", sorted(gateway_run._BUILTIN_ADAPTERS, key=lambda p: p.value), ids=lambda p: p.value)
def test_every_builtin_spec_resolves_to_a_real_adapter_and_requirements_probe(platform):
    module, adapter_name, requirement, _warning = gateway_run._BUILTIN_ADAPTERS[platform]
    adapter_cls, requirements_ok = gateway_run._builtin_adapter_import(module, adapter_name, requirement)
    assert isinstance(adapter_cls, type)
    assert callable(requirements_ok) or isinstance(requirements_ok, bool)
