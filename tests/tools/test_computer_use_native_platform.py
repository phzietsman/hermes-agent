"""Exercise existing computer-use actions on the actual macOS CI host."""

import json
import sys

import pytest

from tools.computer_use import tool


@pytest.fixture
def backend(monkeypatch):
    tool.reset_backend_for_tests()
    monkeypatch.setattr(tool, "_new_backend", lambda mode: tool._NoopBackend())
    # Input actions are approval-gated; a CI runner has no approver, so the gate would block
    # before the dispatch this test is about. Same seam the capture-fence tests use.
    monkeypatch.setattr(tool, "_request_approval", lambda *a, **k: None)
    value = tool._get_backend()
    yield value
    tool.reset_backend_for_tests()


@pytest.mark.platforms("macos")
@pytest.mark.parametrize("args, expected_call", [
    ({"action": "capture", "mode": "ax"}, "capture"),
    ({"action": "click", "coordinate": [10, 10]}, "click"),
    ({"action": "type", "text": "test input"}, "type"),
    ({"action": "list_windows"}, "list_windows"),
])
def test_native_computer_use_dispatches_to_backend(backend, args, expected_call):
    import tools.computer_use_tool  # noqa: F401 - register the real tool handler
    from tools.registry import registry

    assert sys.platform == "darwin"
    result = registry.dispatch("computer_use", args)

    assert "error" not in json.loads(result)
    assert [name for name, _ in backend.calls] == [expected_call]
