"""Activation syncs through setup before selecting or changing the caller's env."""
import json
import os
from pathlib import Path
import shlex
import subprocess
import textwrap

import pytest

from tests.pm.activation_support import bash, fake_store, sync_checkout

# Spawns children with a home it builds itself; the parent's must stay real.
pytestmark = pytest.mark.real_machine_home


def _assert_syncs(root: Path):
    calls = [json.loads(line) for line in (root / "calls.jsonl").read_text(encoding="utf-8").splitlines()]
    assert calls == [{"argv": ["runtime-only", "--trust-recorded", "--test-environment"], "python_env": {
        "PYTHONHOME": None, "PYTHONPATH": None, "VIRTUAL_ENV": None,
    }}] * 3
    assert (root / "builds").read_text(encoding="utf-8").splitlines() == ["first", "second"]


@pytest.mark.platforms("posix")
@pytest.mark.parametrize("canary", [None, "caller-canary"])
def test_bash_cold_sync_changed_input_and_warm_noop(tmp_path, canary):
    root, env = sync_checkout(tmp_path)
    assert not (root / ".venv").exists()
    if canary is not None:
        env["HERMES_PM_ACTIVATE_CANARY"] = canary
    fake_store(tmp_path)
    script = f'''
        set -e
        export PYTHONPATH=caller-original VIRTUAL_ENV=caller-venv
        original_path="$PATH"
        source "{root / 'activate'}"
        printf '%s\\n' "$PYTHONPATH"
        test "$HERMES_PM_ACTIVATE_CANARY" = env-ok
        printf second > "{root / 'input'}"
        source "{root / 'activate'}"
        printf '%s\\n' "$PYTHONPATH"
        source "{root / 'activate'}"
        printf '%s\\n' "$PYTHONPATH"
        test "$PWD" = "{tmp_path}"
        deactivate
        test "$PATH" = "$original_path"
        test "$PYTHONPATH" = caller-original
        test "$VIRTUAL_ENV" = caller-venv
        {('test "$HERMES_PM_ACTIVATE_CANARY" = caller-canary' if canary else 'test -z "${HERMES_PM_ACTIVATE_CANARY+set}"')}
        test -z "${{__HERMES_ACTIVATED+set}}"
        ! declare -F deactivate >/dev/null
    '''
    run = subprocess.run([bash(), "-c", script], cwd=tmp_path, env=env,
                         capture_output=True, text=True, timeout=40)
    assert run.returncode == 0, run.stdout + run.stderr
    first, second, warm = run.stdout.splitlines()
    assert first.startswith(str(root) + os.pathsep) and "/first/venv/" in first
    assert second.startswith(str(root) + os.pathsep) and "/second/venv/" in second
    assert warm == second
    assert run.stderr.count("setup progress") == 3
    _assert_syncs(root)


@pytest.mark.platforms("posix")
@pytest.mark.parametrize("already_active", [False, True])
def test_bash_setup_failure_preserves_caller(tmp_path, already_active):
    root, env = sync_checkout(tmp_path)
    activate = shlex.quote(str(root / "activate"))
    script = f'''
        set -e
        {f'source {activate}' if already_active else ':'}
        export PYTHONHOME=caller-home PYTHONPATH=caller-path VIRTUAL_ENV=caller-venv
        before_env=$(export -p)
        before_function=$(declare -f deactivate || :)
        before_active=${{__HERMES_ACTIVATED-unset}}
        before_cwd="$PWD"
        touch {shlex.quote(str(root / 'fail'))}
        if source {activate}; then exit 9; fi
        test "$(export -p)" = "$before_env"
        test "$(declare -f deactivate || :)" = "$before_function"
        test "${{__HERMES_ACTIVATED-unset}}" = "$before_active"
        test "$PWD" = "$before_cwd"
        {"deactivate" if already_active else ':'}
        printf preserved
    '''
    run = subprocess.run([bash(), "-c", script], cwd=tmp_path, env=env,
                         capture_output=True, text=True, timeout=40)
    assert run.returncode == 0, run.stdout + run.stderr
    assert run.stdout == "preserved"
    calls = (root / "calls.jsonl").read_text(encoding="utf-8").splitlines()
    assert len(calls) == (2 if already_active else 1)
    assert json.loads(calls[-1])["python_env"] == dict.fromkeys(("PYTHONHOME", "PYTHONPATH", "VIRTUAL_ENV"))
