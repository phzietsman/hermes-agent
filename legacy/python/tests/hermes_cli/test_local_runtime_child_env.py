"""A managed llama-server child never inherits credential-shaped environment variables.

Credentials have no business in a native inference child (#116109), so the supervisor scrubs
them at its spawn.
"""

import os
from types import SimpleNamespace

from hermes_cli.local_runtime import supervisor
from hermes_cli.local_runtime.processes import server_child_env


def test_supervisor_spawns_llama_server_without_credentials_but_keeps_runtime_env(tmp_path, monkeypatch):
    monkeypatch.setenv("FIRECRAWL_API_KEY", "fc-secret")
    monkeypatch.setenv("SOME_TOKEN", "t")
    monkeypatch.setenv("DB_PASSWORD", "p")
    monkeypatch.setenv("CUDA_VISIBLE_DEVICES", "0")
    monkeypatch.setenv("OMP_NUM_THREADS", "4")
    monkeypatch.setattr(supervisor, "runtimes_root", lambda: tmp_path / "runtime")
    monkeypatch.setattr(supervisor, "_direct_io_args", lambda exe: [])
    spawns = []
    monkeypatch.setattr(supervisor, "spawn_server", lambda argv, **kwargs: (
        spawns.append(kwargs) or SimpleNamespace(pid=123, poll=lambda: 0), None,
    ))
    sup = supervisor.LlamaServerSupervisor(tmp_path / "engine" / "llama-server", tmp_path / "models", port=19002)
    monkeypatch.setattr(sup, "_write_state", lambda: None)
    try:
        sup._spawn()
    finally:
        sup.stop()
    child_env = spawns[0]["env"]
    assert "FIRECRAWL_API_KEY" not in child_env
    assert "SOME_TOKEN" not in child_env
    assert "DB_PASSWORD" not in child_env
    assert child_env["CUDA_VISIBLE_DEVICES"] == "0"
    assert child_env["OMP_NUM_THREADS"] == "4"
    assert child_env["PATH"] == os.environ["PATH"]


def test_server_child_env_is_case_insensitive_and_pure():
    base = {"openai_api_key": "k", "Path": "/x", "GITHUB_TOKEN": "g", "HSA_OVERRIDE_GFX_VERSION": "11"}
    assert server_child_env(base) == {"Path": "/x", "HSA_OVERRIDE_GFX_VERSION": "11"}
    assert base["openai_api_key"] == "k"  # input untouched
