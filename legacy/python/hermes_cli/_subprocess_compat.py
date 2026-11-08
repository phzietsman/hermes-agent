"""Subprocess helpers: hardened internal git environments, bounded probes, process-tree kill."""

from __future__ import annotations

import os
import re
import subprocess
from pathlib import Path
from typing import Mapping, NoReturn, Sequence

__all__ = [
    "restore_ambient_pythonpath",
    "bounded_git_probe",
    "bounded_probe_run",
    "selected_git_env",
    "noninteractive_git_env",
    "noninteractive_repo_git_env",
    "FILTER_DISCOVERY_FAILED",
    "NO_DRIVER_DIFF_FLAGS",
    "NO_LAZY_FETCH_ENV",
    "pid_is_hermes",
    "pid_exists_stdlib",
]

# Flags that neutralize *attribute-scoped* diff drivers on any diff-rendering git command. A
# malicious repo can name a driver in ``.gitattributes`` (``* diff=evil``) and point it at an
# arbitrary program via ``[diff "evil"] command=/textconv=`` in ``.git/config``; because the
# attacker chooses the name, ``GIT_CONFIG_KEY`` overrides in ``noninteractive_git_env`` cannot
# enumerate it — only these flags do. ``--no-ext-diff`` kills ``command=``; ``--no-textconv`` kills
# ``textconv=``; each alone leaves the other live. Repository-named clean/smudge/process filters
# are handled separately by ``noninteractive_repo_git_env`` at repo-scoped automatic call sites.
NO_DRIVER_DIFF_FLAGS = ("--no-ext-diff", "--no-textconv")

# Only these subcommands accept ``NO_DRIVER_DIFF_FLAGS`` — ``status`` and friends reject them
# (``unknown option``), so the helper gates on this set rather than blanket-prepending.
_DIFF_RENDERING_SUBCOMMANDS = frozenset({"diff", "show", "log", "blame"})

# Options that consume the FOLLOWING token, so that value is never mistaken for the subcommand
# (``-C diff`` is a path; ``-c diff=x`` is a config pair).
_GIT_VALUE_OPTS = {"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path"}


def run(cmd, **kwargs) -> NoReturn:
    # Shim to suppress old updater work until relaunch. Do not start its installer.
    from hermes_cli._old_updater import stop_for_relaunch

    stop_for_relaunch()


# Frozen old-updater surface (tests/compat/old_updater_surface.json): a running pre-PM updater
# lazy-imports these from the new tree mid-update. Windows support is gone; each keeps the
# POSIX behaviour it always had. Current code must not call them.
def split_command_line(line: str) -> list[str]:
    import shlex

    return shlex.split(line)


def windows_hide_flags() -> int:
    return 0


def windows_detach_flags_without_breakaway() -> int:
    return 0


def windows_detach_popen_kwargs() -> dict:
    return {"start_new_session": True}


def harden_git_argv(args: Sequence[str]) -> list[str]:
    """Copy of subcommand-first git *args* (no leading ``"git"``) with :data:`NO_DRIVER_DIFF_FLAGS`
    inserted right after a diff-rendering subcommand; other subcommands are returned unchanged.

    Pair with :func:`noninteractive_git_env`: the env layer disables fsmonitor/hooks/pager/editor/
    credential sinks, this closes the one class (attacker-named attribute drivers) env cannot reach.
    """
    out = list(args)
    i = 0
    while i < len(out):
        tok = out[i]
        if tok in _GIT_VALUE_OPTS:
            i += 2
            continue
        if tok.startswith("-"):
            i += 1
            continue
        if tok in _DIFF_RENDERING_SUBCOMMANDS:
            return out[: i + 1] + list(NO_DRIVER_DIFF_FLAGS) + out[i + 1 :]
        return out  # first non-option token is a non-diff subcommand
    return out


def restore_ambient_pythonpath(env: Mapping[str, str]) -> dict:
    """Re-add the ambient ``PYTHONPATH`` to a child environment that a
    ``build_subprocess_env``-style factory already built.

    No-boot-through-venv: the boot interpreter is the pm STORE python, whose
    imports arrive via ``PYTHONPATH=<repo>;<venv>/site-packages`` (it has no
    editable install). The subprocess-env factories strip Hermes-owned
    PYTHONPATH entries so agent-run children on DIFFERENT interpreter
    versions never load the backend's C extensions — but a child that
    re-execs THIS interpreter (``sys.executable -m hermes_cli.main``) runs
    on the same version and needs those entries back. Prepending keeps the
    launcher's repo-first ordering intact.
    """
    merged = dict(env)
    ambient = os.environ.get("PYTHONPATH")
    if ambient:
        existing = merged.get("PYTHONPATH", "")
        merged["PYTHONPATH"] = (
            ambient + os.pathsep + existing if existing else ambient
        )
    return merged


def no_prompt_git_kwargs() -> dict:
    """``subprocess.run`` kwargs for the updater's network git calls.

    GitHub answers anonymous fetches with HTTP 401 during outages (and for
    unreachable repos); git then prompts ``Username for 'https://github.com':``
    on the inherited terminal and the update sits there forever. Disable the
    prompt so the fetch fails fast into ``_classify_fetch_failure``. Only the
    *prompt* is disabled — a configured credential helper / askpass still
    runs, so a private-fork origin keeps authenticating non-interactively.
    Lives here, dependency-free, because the update hand-off's bootstrap
    interpreter runs the treeless conversion before any dependency exists.
    """
    env = dict(os.environ)
    env["GIT_TERMINAL_PROMPT"] = "0"
    env["GCM_INTERACTIVE"] = "Never"
    return {"stdin": subprocess.DEVNULL, "env": env}

# Read-only probes must never lazy-fetch. In a partial (blobless/treeless) clone a missing object makes
# git spawn ``git fetch`` from the promisor remote, and a probe's timeout kills only its own git: the
# startup update check's ``merge-base --is-ancestor <fresh upstream tip>`` started a ~233k-object
# history download on every launch that ran on orphaned, piling up partial packs. With this set the
# probe fails fast on the missing object instead (git >= 2.44; older git ignores the variable).
NO_LAZY_FETCH_ENV = {"GIT_NO_LAZY_FETCH": "1"}


# GIT_CONFIG_KEY_n/VALUE_n overrides for internal git children: no credential/askpass prompts, no
# repo-configured fsmonitor/hooks/pager/editor/external-diff programs.
_GIT_CONFIG_INJECT_PREFIXES = ("GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_")
_GIT_CONFIG_OVERRIDES = {
    "credential.helper": "",
    "core.askPass": "",
    "core.fsmonitor": "false",
    "core.untrackedCache": "false",
    "core.hooksPath": os.devnull,
    "core.pager": "cat",
    "core.editor": "true",
    "sequence.editor": "true",
    "diff.external": "",
    # ssh itself bypasses stdin=DEVNULL/GIT_TERMINAL_PROMPT and opens /dev/tty directly — an
    # unknown host key (or password auth) prompts there and steals the caller's terminal (#104591).
    # BatchMode makes ssh fail instead of prompting; a working ssh-agent still succeeds. Injected
    # at the config layer so an explicit user GIT_SSH_COMMAND (env) still takes precedence.
    "core.sshCommand": "ssh -o BatchMode=yes",
}


def _safe_directory_cache_key(env: "Mapping[str, str]") -> tuple:
    """Everything that decides which files ``git config --system/--global`` reads, plus the
    global candidates' mtimes so an edit to ``~/.gitconfig`` is picked up without a restart."""
    home = env.get("HOME", "")
    xdg = env.get("XDG_CONFIG_HOME") or os.path.join(home, ".config")
    candidates = (
        env.get("GIT_CONFIG_SYSTEM") or "/etc/gitconfig",
        env.get("GIT_CONFIG_GLOBAL") or os.path.join(home, ".gitconfig"),
        os.path.join(xdg, "git", "config"),
    )
    stamps = []
    for path in candidates:
        try:
            stamps.append(os.stat(path).st_mtime_ns)
        except OSError:
            stamps.append(None)
    return (
        env.get("GIT_CONFIG_GLOBAL"), env.get("GIT_CONFIG_SYSTEM"), env.get("GIT_CONFIG_NOSYSTEM"),
        home, env.get("XDG_CONFIG_HOME"), env.get("PATH"), *stamps,
    )


_safe_directory_cache: dict[tuple, list[str]] = {}


def _user_safe_directories(base_env: "Mapping[str, str]") -> list[str]:
    """The user's configured ``safe.directory`` values, in git's own effective order.

    Read with ``git config -z --get-all`` under *base_env* (the caller's untouched environment) so
    an explicit ``GIT_CONFIG_GLOBAL``/``GIT_CONFIG_SYSTEM`` still points at the file the user means.
    Best-effort: any failure (git missing, malformed config, timeout) yields no entries and leaves
    the caller exactly as it behaved before. Memoised per process on the inputs that select the
    config files (and the global file's mtime): ``noninteractive_git_env()`` runs on every internal
    git call, including the startup banner probe, and two ``git config`` children per call is
    ~10 ms against ~0.2 ms for the rest of the function.

    ``safe.directory`` is an *ordered* multi-valued setting and an empty value resets every entry
    seen so far, so a user can revoke a system-wide ``safe.directory=*`` and then name only the
    repositories they actually trust. Order and empty resets are therefore policy, not formatting:
    scopes are read lowest-precedence first (system, then global) and every value is preserved
    verbatim -- no de-duplication (it is a sequence, not a set) and no dropping of the reset
    marker, either of which would resurrect a revoked wildcard and widen trust. ``-z`` keeps a
    value containing whitespace or a newline as the single entry git reads it as.
    """
    cache_key = _safe_directory_cache_key(base_env)
    cached = _safe_directory_cache.get(cache_key)
    if cached is not None:
        return list(cached)
    env = dict(base_env)
    # --get-all itself must not be derailed by ambient injection or an interactive prompt.
    for key in list(env):
        if key == "GIT_CONFIG_PARAMETERS" or key.startswith(_GIT_CONFIG_INJECT_PREFIXES):
            env.pop(key, None)
    env.pop("GIT_CONFIG_COUNT", None)
    env["GIT_TERMINAL_PROMPT"] = "0"
    values: list[str] = []
    for scope in ("--system", "--global"):
        try:
            proc = subprocess.run(
                ["git", "config", scope, "-z", "--get-all", "safe.directory"],
                capture_output=True, text=True, encoding="utf-8", errors="replace",
                timeout=5, stdin=subprocess.DEVNULL, env=env, check=False,
            )
        except (OSError, subprocess.SubprocessError):
            continue
        if proc.returncode != 0:
            continue
        # -z terminates every value with NUL, so the trailing split field is always empty and is
        # not a config entry; interior empty fields are real reset markers and must survive.
        records = proc.stdout.split("\0")
        if records and records[-1] == "":
            records.pop()
        values.extend(records)
    _safe_directory_cache[cache_key] = list(values)
    return values


def selected_git_env(base: Mapping[str, str] | None = None) -> dict[str, str]:
    """PM's full Git environment, or the original base for system-Git fallback.

    Keep lazy acquisition under PM's policy (not just installed-package lookup).
    Unsupported targets and failed acquisition must not disable a working system
    Git. Callers apply their own config/security isolation after selection.
    """
    env = dict(base if base is not None else os.environ)
    try:
        from pm import ensure

        return ensure("git", base_env=env).env
    except Exception:
        return env


def noninteractive_git_env(base: "Mapping[str, str] | None" = None) -> dict[str, str]:
    """Environment for *internal* git invocations that must never prompt.

    Copy of ``base`` (default ``os.environ``) with ``GIT_TERMINAL_PROMPT=0`` (fail instead of
    prompting), ``GCM_INTERACTIVE=Never`` (no Git Credential Manager dialog), and isolated git
    config: inherited ``GIT_CONFIG_*`` injection, global/system config, pagers, editors, fsmonitor,
    external diff and hooks are all disabled so a user's repo/global config cannot hang or mutate
    Hermes's plumbing calls. ``core.sshCommand`` is pinned to ``ssh -o BatchMode=yes`` so the ssh
    child of a fetch/ls-remote fails instead of prompting — ssh bypasses ``stdin=DEVNULL`` and
    opens ``/dev/tty`` directly (#104591); an agent-authenticated ssh still succeeds, and an
    explicit user ``GIT_SSH_COMMAND`` env var still takes precedence over this config-layer pin.
    ``GIT_ASKPASS``/``SSH_ASKPASS`` env vars are left alone, but OpenSSH BatchMode disables
    passphrase/password prompts, including SSH askpass. Usable keys and ssh-agent authentication
    still work; Git's own working askpass helper is unaffected. Pair with
    ``stdin=subprocess.DEVNULL``. Internal plumbing only — the agent-facing terminal tool has its
    own policy layer and visible PTY.

    Hermes shells out to git from many non-interactive contexts — MCP catalog installs, plugin
    install/update, profile distribution staging, worktree base fetches, desktop review-pane fetch/push.
    When the remote is private, misconfigured, or requires auth, git's default behavior is to prompt on the
    inherited terminal (or via an askpass helper), which silently hangs the operation until its timeout — or
    forever at call sites without one. Ported from openai/codex#34540 / #34612 ("detach non-interactive
    subprocesses from stdin"): a background tool invocation must fail fast with a readable error, not wait
    for input nobody can type.
    """
    env = dict(base if base is not None else os.environ)
    # Captured before the isolation below rewrites GIT_CONFIG_GLOBAL/SYSTEM to /dev/null --
    # reading after that point would resolve the user's config to an empty file.
    safe_directories = _user_safe_directories(base if base is not None else os.environ)
    env["GIT_TERMINAL_PROMPT"] = "0"
    env["GCM_INTERACTIVE"] = "Never"
    # Drop caller-supplied config injection; the GIT_CONFIG_COUNT block is rebuilt below so
    # ambient -c values cannot re-enable pagers, hooks, fsmonitor, editors or credential prompts.
    for key in list(env):
        if key == "GIT_CONFIG_PARAMETERS" or key.startswith(_GIT_CONFIG_INJECT_PREFIXES):
            env.pop(key, None)
    env.pop("GIT_CONFIG_COUNT", None)
    env["GIT_CONFIG_GLOBAL"] = os.devnull
    env["GIT_CONFIG_SYSTEM"] = os.devnull
    env["GIT_CONFIG_NOSYSTEM"] = "1"
    env["GIT_PAGER"] = "cat"
    env["PAGER"] = "cat"
    env["GIT_EDITOR"] = "true"
    overrides = list(_GIT_CONFIG_OVERRIDES.items())
    # safe.directory is honoured ONLY from global/system config (git rejects it from repo-level
    # config so a hostile repo cannot self-authorise), and both are blanked just above. Without
    # re-injection every internal git call fails "detected dubious ownership" on any repo whose
    # st_uid != geteuid() -- NFS/CIFS mounts without idmapping, shared checkouts, containers with
    # a remapped uid -- even though the user's own `git config --global --add safe.directory` is
    # correctly set and their interactive git works fine. Carried over the GIT_CONFIG_KEY_n
    # channel, which survives GIT_CONFIG_GLOBAL=/dev/null. Read-only and non-widening: the values
    # are replayed in git's own effective order, empty reset markers included (see
    # _user_safe_directories), so a global reset still revokes a system-wide wildcard exactly as it
    # does for the user's interactive git. Appended last, but the hardening overrides above are
    # distinct keys, so they are unaffected by ordering within safe.directory.
    overrides.extend(("safe.directory", value) for value in safe_directories)
    env["GIT_CONFIG_COUNT"] = str(len(overrides))
    for idx, (key, value) in enumerate(overrides):
        env[f"GIT_CONFIG_KEY_{idx}"] = key
        env[f"GIT_CONFIG_VALUE_{idx}"] = value
    return env


_FILTER_COMMAND_KEY = re.compile(r"^filter\..+\.(?:clean|smudge|process)$", re.IGNORECASE)
# ``includeIf`` is evaluated against the CURRENT checkout: ``onbranch:`` against the current branch,
# ``gitdir:`` against the current git dir (``.git/worktrees/<name>`` during ``worktree add``), so
# the spawned git can load an include this discovery's ``--includes`` skipped. Every include target
# is therefore read directly, whatever its condition, and its filter names are neutralized too.
# Global/system config is already /dev/null, so only repo-local includes reach this.
_INCLUDE_IF_KEY = re.compile(r"^includeif\..*\.path$", re.IGNORECASE)
# Any include (conditional or not) inside an include target: discovery does not walk it twice.
_INCLUDE_KEY = re.compile(r"^include(?:if\..*)?\.path$", re.IGNORECASE)
# `git config --get-regexp` pattern for the keys discovery reads (filter commands and includes).
_DISCOVERY_KEYS_REGEXP = r"^(filter\..*\.(clean|smudge|process)|include\.path|includeif\..*\.path)$"
# Each include target costs one bounded spawn on every hardened git call; refuse past this many.
_MAX_INCLUDE_TARGETS = 16
# Each discovered key costs two env entries; a repo with tens of thousands of filters would make
# every spawn fail with E2BIG ("Argument list too long"), so refuse past a generous cap.
_MAX_FILTER_KEYS = 256
# Stand-in stderr for a git call refused because filter discovery could not be trusted.
FILTER_DISCOVERY_FAILED = "git filter discovery failed"


def noninteractive_repo_git_env(
    cwd: "str | os.PathLike[str]",
    base: "Mapping[str, str] | None" = None,
) -> "dict[str, str] | None":
    """Harden internal git for one repository, including named clean/smudge/process filters.

    The static environment can pin fixed config keys such as core.fsmonitor and
    core.hooksPath, but a repository chooses filter driver names through .gitattributes.
    Discover the effective filter command keys for this checkout and append empty command
    overrides plus required=false to the already-isolated config block. Discovery is
    bounded and fail-closed: if filter discovery cannot be trusted, callers skip the
    automatic git operation instead of running with only partial hardening.
    """
    env = noninteractive_git_env(base)
    # bounded_probe_run, not subprocess.run: the probe's descendants must die with it on timeout.
    # (Not bounded_git_probe: rc 1 = "no filters" is a verdict.)
    # One probe lists the filter keys and the include paths with their origin file: -z --show-origin
    # yields "file:<origin>", "<key>\n<value>" pairs.
    proc = bounded_probe_run(
        ["git", "-C", str(cwd), "config", "--includes", "--show-origin", "-z", "--get-regexp",
         _DISCOVERY_KEYS_REGEXP],
        timeout=2, env=env,
    )
    if proc is None or proc.returncode not in (0, 1):
        return None
    names: list[str] = []
    targets: set[Path] = set()
    toplevel: "Path | None" = None
    fields = proc.stdout.split("\0")
    for origin, entry in zip(fields[0::2], fields[1::2]):
        key, _, value = entry.partition("\n")
        if not _INCLUDE_IF_KEY.fullmatch(key):
            names.append(key)
            continue
        if not origin.startswith("file:"):
            return None
        origin_path = Path(origin[len("file:"):])
        if not origin_path.is_absolute():
            # git prints repo-local origins relative to the worktree top level, not to *cwd*; with a
            # caller-set GIT_DIR/GIT_WORK_TREE they are relative to something else, so refuse.
            if env.get("GIT_DIR") or env.get("GIT_WORK_TREE"):
                return None
            if toplevel is None:
                top = bounded_probe_run(["git", "-C", str(cwd), "rev-parse", "--show-toplevel"],
                                        timeout=2, env=env)
                if top is None or top.returncode != 0:
                    return None
                toplevel = Path(top.stdout.rstrip("\r\n"))  # a checkout path may end in a space
            origin_path = toplevel / origin_path
        # A relative include path resolves against the directory of the config file naming it.
        target = (origin_path.parent / os.path.expanduser(value)).resolve()
        if target in targets or not target.exists():
            continue  # already read, or missing (git skips a missing include file)
        if not target.is_file() or len(targets) >= _MAX_INCLUDE_TARGETS:
            return None  # a FIFO/device/directory, or too many targets to read on every call
        targets.add(target)
        probe = bounded_probe_run(
            ["git", "config", "--file", str(target), "--name-only", "-z", "--get-regexp", _DISCOVERY_KEYS_REGEXP],
            timeout=2, env=env,
        )
        if probe is None or probe.returncode not in (0, 1):
            return None
        found = probe.stdout.split("\0")
        # An include inside an include target would need the same walk again; refuse instead.
        if any(_INCLUDE_KEY.fullmatch(name) for name in found):
            return None
        names.extend(found)

    keys: list[str] = []
    required: list[str] = []
    seen: set[str] = set()
    # Dedup on the exact key name git prints: section and variable lowercased, subsection case kept,
    # and ``[filter "Evil"]`` is a different driver from ``[filter "evil"]``.
    for raw in names:
        key = raw.strip()
        if not key or key in seen or not _FILTER_COMMAND_KEY.fullmatch(key):
            continue
        seen.add(key)
        keys.append(key)
        if len(keys) > _MAX_FILTER_KEYS:
            return None
        required_key = key.rsplit(".", 1)[0] + ".required"
        if required_key not in seen:
            seen.add(required_key)
            required.append(required_key)

    start = int(env["GIT_CONFIG_COUNT"])  # always set by noninteractive_git_env
    overrides = [(key, "") for key in keys] + [(key, "false") for key in required]
    for offset, (key, value) in enumerate(overrides):
        env[f"GIT_CONFIG_KEY_{start + offset}"] = key
        env[f"GIT_CONFIG_VALUE_{start + offset}"] = value
    env["GIT_CONFIG_COUNT"] = str(start + len(overrides))
    return env


def posix_is_zombie(pid: int) -> bool:
    """Zombie via ``/proc/<pid>/stat`` field 3, or ``ps -o state=`` without /proc (macOS/BSD)."""
    try:
        with open(f"/proc/{pid}/stat", encoding="utf-8") as fh:
            stat_fields = fh.read().split()
        return len(stat_fields) > 2 and stat_fields[2] == "Z"
    except FileNotFoundError:
        try:
            r = subprocess.run(
                ["ps", "-o", "state=", "-p", str(pid)],
                capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=5,
            )
            return r.returncode == 0 and r.stdout.strip().startswith("Z")
        except Exception:
            pass
    except (IndexError, PermissionError, OSError):
        pass
    return False


def pid_exists_stdlib(pid: int) -> bool:
    """Stdlib-only "is this PID alive" check that never signals the target (zombies report dead).

    For code that must run without the dependency environment: the detached gateway restart
    watcher is started by whatever interpreter the updater runs on (the bare store Python after
    the package-manager handoff), so it cannot import ``gateway.status`` (``utils`` pulls in
    ``ruamel``). ``gateway.status._pid_exists`` prefers psutil and falls back to this.
    """
    pid = int(pid)
    if posix_is_zombie(pid):  # a zombie still answers os.kill(pid, 0)
        return False
    try:
        os.kill(pid, 0)
    except PermissionError:
        return True  # Exists but we can't signal it.
    except OSError:  # ProcessLookupError included
        return False
    return True


def _process_start_time(pid: int) -> int | None:
    """The repository's stable process-start fingerprint, if available."""
    try:
        from gateway.status import get_process_start_time

        return get_process_start_time(pid)
    except Exception:
        return None


def pid_is_hermes(pid: int, *, expected_start_time: int | None = None) -> bool:
    """Whether *pid* is a valid PID that, when the caller captured a start-time fingerprint before
    the destructive action, still carries the same ``(pid, start_time)`` identity. Any ambiguity
    fails closed.
    """
    if not isinstance(pid, int) or isinstance(pid, bool) or pid <= 0:
        return False
    if expected_start_time is None:
        return True
    try:
        return _process_start_time(pid) == expected_start_time
    except Exception:
        return False


def kill_process_tree(proc: "subprocess.Popen") -> None:
    """Best-effort terminate *proc* and its descendants; never raises.

    ``proc.kill()`` alone only terminates the direct child. This is cleanup on an already-failing
    path whose contract is to fail open, so every failure (access denied, already reaped) is
    swallowed rather than escaping the caller's ``except``.

    Killing the launcher leaves descendants (credential helpers, ``git-remote-https``, hook children)
    running and holding the pipe write ends, so the bounded drain that follows never reaches EOF. Callers spawn the child in
    its own process group (``process_group=0``, Python ≥3.11), so when — and only when — the child leads its
    own group (``pgid == pid``), the entire group is signalled with ``os.killpg``. The ownership check means
    a fallback spawn that shares our group can never cause us to kill unrelated processes. Ported from
    openai/codex#36793 ("Terminate timed-out Git process trees"); generalized for the shell-hook runner via
    openai/codex#37527 ("Terminate timed-out hook process trees").
    """
    try:
        from agent.deadline import kill_process_tree as _deadline_kill_tree

        _deadline_kill_tree(proc.pid)
    except Exception:
        _legacy_kill_process_tree(proc)
        return
    # Ensure Popen's own bookkeeping sees the exit so communicate()/wait() cannot hang.
    try:
        proc.kill()
    except OSError:
        pass


def _legacy_kill_process_tree(proc: "subprocess.Popen") -> None:
    """Local tree-kill fallback when agent.deadline is unavailable (partial install, cycle)."""
    # Verify the child leads its own process group before signalling, never a shared group.
    try:
        import signal as _signal

        pgid = os.getpgid(proc.pid)
        if pgid == proc.pid:
            os.killpg(pgid, _signal.SIGKILL)
    except Exception:
        pass
    try:
        proc.kill()
    except OSError:
        pass


def bounded_probe_run(
    argv: Sequence[str], *, timeout: float, errors: str = "replace",
    env: "Mapping[str, str] | None" = None, cwd: "str | os.PathLike[str] | None" = None,
    raise_on_spawn_failure: bool = False, input: "str | None" = None,
) -> "subprocess.CompletedProcess[str] | None":
    """Deadlock-safe ``subprocess.run(argv, capture_output=True, timeout=…)`` for fail-open probes.

    ``input`` is written to the child's stdin (closed afterwards); without it stdin is ``DEVNULL``.

    Returns a ``CompletedProcess`` when the child finished within *timeout* (any exit code), or
    ``None`` on spawn failure or timeout. With ``raise_on_spawn_failure=True`` the ``Popen``
    exception propagates instead, so callers that treat a *timeout* as a verdict can still tell
    "our own probe never started" apart from "the child hung".

    Why not ``subprocess.run``: its post-timeout cleanup calls an *unbounded* ``communicate()`` after
    killing only the direct child; a surviving descendant holding the captured pipes keeps them from
    reaching EOF and the reader-thread join blocks forever (#87134, #68609 / #66037). The probe runs
    in its own process group so the timeout path takes the whole tree down.
    """
    try:
        proc = subprocess.Popen(
            list(argv), stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            stdin=subprocess.DEVNULL if input is None else subprocess.PIPE,
            text=True, encoding="utf-8", errors=errors,
            env=dict(env) if env is not None else None, cwd=cwd, process_group=0)
    except Exception:
        if raise_on_spawn_failure:
            raise
        return None
    try:
        stdout, stderr = proc.communicate(input=input, timeout=timeout)
    except Exception:
        # Timeout OR any other communicate() failure (torn-down pipe, decode error): tree-kill and
        # drain bounded — leaving it running would leak the suspended-descendant class this guards.
        kill_and_drain(proc, 1)
        return None
    return subprocess.CompletedProcess(list(argv), proc.returncode, stdout, stderr)


def kill_and_drain(proc: "subprocess.Popen", seconds: float) -> "tuple | None":
    """Tree-kill *proc* (:func:`kill_process_tree`), then read what its pipes still hold for at
    most *seconds*: ``(stdout, stderr)``, or ``None`` when a descendant the kill missed still
    holds them. Those pipes are left to ``communicate()``'s reader threads, never closed
    (closing a pipe a thread is reading blocks; a ``with Popen`` exit would close them)."""
    kill_process_tree(proc)
    try:
        return proc.communicate(timeout=seconds)
    except (OSError, ValueError, subprocess.SubprocessError):
        proc.stdin = proc.stdout = proc.stderr = None
        return None


def bounded_git_probe(argv: Sequence[str], *, timeout: float, env: "Mapping[str, str] | None" = None) -> str:
    """Run a short ``git`` probe and return stripped stdout, or ``""`` on ANY failure.

    ``run()``'s post-timeout cleanup calls an unbounded ``communicate()``; a descendant holding the
    pipe handles then blocks forever. Here: bounded ``communicate``, tree-kill plus a 1s drain, then
    abandon the pipes; the probe gets its own process group so cleanup also takes down
    credential/remote helpers.

    Security (GHSA-7x36-8jrh-v4pw): these probes run automatically against whatever directory the
    session sits in, before any tool call or trust prompt, and an index refresh executes the
    repo-configured ``core.fsmonitor`` program. Every probe therefore runs under
    :func:`noninteractive_git_env`; diff-rendering callers additionally pass
    :data:`NO_DRIVER_DIFF_FLAGS` (attribute-scoped drivers can't be disabled via env).

    On the Desktop agent-build path (``_start_agent_build → _session_info → branch() → run_git``) a
    hung probe turned an optional branch label into ``agent initialization timed out`` (#68609 /
    #66037). ``process_group=0`` only changes which group the child belongs to; it does not detach
    the terminal or alter the fast path (see :func:`kill_process_tree`; port of openai/codex#36793).
    """
    result = bounded_probe_run(argv, timeout=timeout, env={**(env or noninteractive_git_env()), **NO_LAZY_FETCH_ENV})
    if result is None or result.returncode != 0:
        return ""
    return (result.stdout or "").strip()
