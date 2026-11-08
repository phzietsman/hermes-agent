"""Fixtures shared by the shell-activation tests.

An isolated checkout holds the real activation scripts, the real PM environment
reader and fake installed artifacts. Setup is replaced at its process boundary:
the stub records each sync and publishes a selected environment. Nothing here
sources the working checkout or runs its installer against the developer's home.
"""

from __future__ import annotations

import json
import os
import shlex
import shutil
import subprocess
import sys
import textwrap
from pathlib import Path

from pm.store import current_target

REPO_ROOT = Path(__file__).resolve().parents[2]
ACTIVATE = REPO_ROOT / "activate"
ACTIVATE_FISH = REPO_ROOT / "activate.fish"
SETUP_HERMES_SH = REPO_ROOT / "setup-hermes.sh"
CANARY = "HERMES_PM_ACTIVATE_CANARY"


def bash() -> str:
    return shutil.which("bash") or "bash"


def child_env() -> dict:
    return os.environ.copy()


def spawnable_python() -> Path:
    """An interpreter this process can actually spawn. A macOS venv shim stops
    working after activation sanitizes its parent environment. Prefer the
    venv's base interpreter, then whichever python can run a trivial child;
    last resort is sys.executable."""
    candidates: list[Path] = []
    for env_name in ("HERMES_TEST_PYTHON",):
        val = os.environ.get(env_name)
        if val:
            candidates.append(Path(val))
    exe = Path(sys.executable)
    base = Path(getattr(sys, "_base_executable", "") or exe)
    if base != exe:
        candidates.append(base)
    candidates.append(exe)
    for cand in candidates:
        if can_spawn(cand):
            return cand
    return exe


def can_spawn(python: Path) -> bool:
    try:
        r = subprocess.run(
            [str(python), "-c", "print(1)"],
            capture_output=True,
            text=True,
            timeout=30,
            env=child_env(),
        )
        return r.returncode == 0
    except Exception:
        return False


def fake_store(tmp_path: Path) -> tuple[Path, Path]:
    """A store laid out like pm's: facts.json marking the python package
    installed (identity matches pm/lock.json) with a canary env export, and
    an entry whose interpreter is a wrapper that runs the real python."""
    lock = json.loads(
        (REPO_ROOT / "pm" / "lock.json").read_text(encoding="utf-8-sig")
    )["packages"]
    target = current_target()
    python_pkg = lock["python"]
    sha = python_pkg["artifacts"][target]["sha256"]

    store = tmp_path / "store"
    entry = store / f"python-{python_pkg['version']}-{target}"
    entry.mkdir(parents=True)

    # The store interpreter only needs to run `python -m pm.cli env` —
    # delegate to a real, spawnable interpreter via a #!/bin/sh wrapper (a
    # copied CPython would miss its DLLs/stdlib; the wrapper is the honest
    # minimal fake), resolved through spawnable_python.
    interpreter = entry / "bin/python3"
    interpreter.parent.mkdir(parents=True, exist_ok=True)
    real = spawnable_python()
    wrapper = "#!/bin/sh\nexec '%s' \"$@\"\n" % real
    interpreter.write_text(wrapper, encoding="utf-8")
    interpreter.chmod(0o755)

    (store / "facts.json").write_text(
        json.dumps(
            {
                "schema": 1,
                "packages": {
                    "python": {
                        "entry": entry.name,
                        "version": python_pkg["version"],
                        "env": {CANARY: "env-ok"},
                        "target": target,
                        "artifacts": [sha],
                    }
                },
            }
        ),
        encoding="utf-8",
    )
    return store, entry


def isolated_checkout(tmp_path: Path) -> Path:
    root = tmp_path / "checkout with spaces"
    root.mkdir()
    shutil.copytree(REPO_ROOT / "pm", root / "pm", ignore=shutil.ignore_patterns("__pycache__"))
    (root / "hermes_cli").mkdir()
    (root / "scripts").mkdir()
    for relative in ("activate", "activate.fish", "scripts/_activation.sh",
                     "hermes_constants.py", "hermes_cli/__init__.py",
                     "pm/environments.py", "hermes_cli/runtime_state.py"):
        shutil.copy2(REPO_ROOT / relative, root / relative)
    # Environment-only tests do not exercise provisioning; the runtime tests
    # replace these stubs with a publisher that records and applies each sync.
    (root / "setup-hermes.sh").write_text(
        'test "$#" = 2 && test "$1" = --runtime-only && case "$2" in --test-environment*) ;; *) exit 2 ;; esac\n',
        encoding="utf-8",
    )
    return root


def bash_env(store: Path) -> dict:
    env = child_env()
    home = store.parent / "home"
    home.mkdir(exist_ok=True)
    env.update(HOME=str(home), HERMES_HOME=str(home / "hermes"))
    for key in ("PYTHONHOME", "PYTHONPATH", "VIRTUAL_ENV", "BASH_ENV", "__HERMES_ACTIVATED"):
        env.pop(key, None)
    env["HERMES_RUNTIME_DIR"] = str(store)
    # Keep the real env out of the composed pm output so the canary export
    # is the only thing activate adds beyond the ambient environment.
    env.pop(CANARY, None)
    return env


def sync_checkout(tmp_path: Path):
    root = isolated_checkout(tmp_path)
    env = bash_env(tmp_path / "store")
    python = spawnable_python()
    # The setup seam publishes a bootstrap and a selected generation. PM's
    # freshness algorithms have their own tests; here a warm setup call is a
    # no-op and activation must still call it, rather than cache its own answer.
    (root / "sync.py").write_text(textwrap.dedent('''\
        import json, os, pathlib, shutil, sys
        from pm.environments import runtime_facts_path, site_packages
        root = pathlib.Path(__file__).parent
        record = {"argv": sys.argv[1:], "python_env": {
            key: os.environ.get(key) for key in ("PYTHONHOME", "PYTHONPATH", "VIRTUAL_ENV")}}
        with (root / "calls.jsonl").open("a", encoding="utf-8") as stream:
            stream.write(json.dumps(record) + "\\n")
        assert all(value is None for value in record["python_env"].values()), record
        assert sys.argv[1:] == ["runtime-only", "--trust-recorded", "--test-environment"], record
        print("setup progress")
        if (root / "fail").exists():
            sys.exit(42)
        bootstrap = root / ".venv"
        if not bootstrap.exists():
            shutil.copytree(root / "prepared-bootstrap", bootstrap)
        generation = (root / "input").read_text(encoding="utf-8").strip()
        facts = runtime_facts_path(root)
        selected = facts.parent / "environments" / generation / "venv"
        if not selected.exists():
            site_packages(selected).mkdir(parents=True)
            (selected / "pyvenv.cfg").write_text("home = fixture", encoding="utf-8")
            facts.write_text(json.dumps({"packages": {"venv": {"environment": str(selected)}}}), encoding="utf-8")
            with (root / "builds").open("a", encoding="utf-8") as stream:
                stream.write(generation + "\\n")
    '''), encoding="utf-8")
    (root / "input").write_text("first", encoding="utf-8")
    binary = root / "prepared-bootstrap" / "bin" / "python"
    binary.parent.mkdir(parents=True)
    binary.write_text(f"#!/bin/sh\nexec {shlex.quote(str(python))} \"$@\"\n", encoding="utf-8")
    binary.chmod(0o755)
    # Activation's contract with setup is the runtime-only switch; setup
    # itself maps that to PM's --trust-recorded install.
    (root / "setup-hermes.sh").write_text(
        'test "$#" = 2 && test "$1" = --runtime-only && test "$2" = --test-environment || exit 2\n'
        f'cd {shlex.quote(str(root))} || exit 3\n'
        f'exec {shlex.quote(str(python))} sync.py runtime-only --trust-recorded --test-environment\n', encoding="utf-8",
    )
    return root, env
