"""Spawn the managed llama-server router."""

from __future__ import annotations

from collections.abc import Mapping
import subprocess


_CREDENTIAL_ENV_MARKERS = ("_API_KEY", "_TOKEN", "_SECRET", "PASSWORD", "_CREDENTIALS")


def server_child_env(base_env: Mapping[str, str]) -> dict[str, str]:
    """Return the environment a native inference child (llama-server) gets.

    Provider and tool credentials never belong in a native child that talks to nobody but
    us. Everything else (PATH, CUDA_*, HSA_*, OMP_*, TEMP, …) passes through untouched. Applied
    by the llama-server supervisor only: generic probe children legitimately need GH_TOKEN,
    HF_TOKEN, …
    """
    return {
        key: value for key, value in base_env.items()
        if not any(marker in key.upper() for marker in _CREDENTIAL_ENV_MARKERS)
    }


def spawn_server(cmd, **kwargs) -> tuple[subprocess.Popen, None]:
    """Start a router: ``(process, None)``.

    The pair shape is kept because shipped updaters import this module from the NEW checkout
    mid-update and unpack it; the second slot is always ``None``.
    """
    return subprocess.Popen(cmd, **kwargs), None
