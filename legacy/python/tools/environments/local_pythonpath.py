"""Hermes-owned PYTHONPATH stripping for child processes. Launchers prepend the repo
root and the Hermes venv's site-packages so the backend can ``import tools``; leaked
into a child Python of a DIFFERENT version they load the backend's C extensions and
crash. Only entries proven Hermes-owned by *path provenance* are removed — never by a
cross-version heuristic. Module state (``_hermes_repo_root_aliases``, ``_in_venv``,
``_hermes_site_packages``) lives in ``tools.environments.local`` (via :func:`_state`)
so tests monkeypatching it there keep working."""

import logging
import os
import sys
from pathlib import Path

from tools.environments.local_env_policy import _ACTIVE_VENV_MARKER_VARS

logger = logging.getLogger("tools.environments.local")


def _state():
    """Return the ``tools.environments.local`` module (owner of the caches)."""
    from tools.environments import local

    return local


def _same_path(left: Path, right: Path) -> bool:
    """Compare path spellings with host filesystem case semantics."""
    return [os.path.normcase(p) for p in left.parts] == [os.path.normcase(p) for p in right.parts]


def _build_hermes_repo_root_aliases(
    resolved_root: Path, lexical_root: Path, configured_home: Path,
) -> tuple[Path, ...]:
    """Exact repo-root spellings emitted by Hermes launchers: the physical path under
    the resolved HERMES_HOME maps back to the configured spelling, so a symlink-backed
    install matches without treating arbitrary HERMES_HOME descendants as Hermes-owned.
    A repo-level symlink is accepted only when a strict resolve proves
    <root>/<repo dirname> is the physical root (fail-closed)."""
    candidates = [resolved_root, lexical_root]
    # Profile re-home: with --profile the configured home is <root>/profiles/<name>
    # and the repo lives beside the profiles dir (as get_default_hermes_root() does).
    home_candidates = [configured_home]
    if configured_home.parent.name == "profiles":
        home_candidates.append(configured_home.parent.parent)
    for home in home_candidates:
        try:
            resolved_home = home.resolve()
            home_key = os.path.normcase(str(resolved_home))
            if os.path.commonpath([home_key, os.path.normcase(str(resolved_root))]) == home_key:
                candidates.append(home / os.path.relpath(str(resolved_root), str(resolved_home)))
        except (OSError, ValueError):
            pass
    # Repo-level symlink recovery: a repo linked in from outside the home is not
    # expressible by the home-relative mapping above.
    for home in home_candidates:
        repo_candidate = home / resolved_root.name
        try:
            if repo_candidate.resolve(strict=True) == resolved_root.resolve(strict=True):
                candidates.append(repo_candidate)
        except OSError:
            pass
    aliases: list[Path] = []
    for candidate in candidates:
        if not any(_same_path(candidate, existing) for existing in aliases):
            aliases.append(candidate)
    return tuple(aliases)


def _validated_runtime_venv() -> Path | None:
    """The PM-selected runtime venv when this checkout records runtime facts, else None."""
    from pm.environments import runtime_facts_path, selected_venv

    root = Path(__file__).resolve().parents[2]
    if runtime_facts_path(root).is_file():
        return selected_venv(root)
    return None


def _get_hermes_site_packages() -> list[Path]:
    """Exact site-packages dirs owned by the Hermes runtime (cached):
    ``site.getsitepackages()`` with a ``sys.prefix`` fallback, plus the PM-selected
    runtime venv's site-packages."""
    local = _state()
    if local._hermes_site_packages is None:
        result: list[Path] = []
        if local._in_venv:
            try:
                import site
                result.extend(Path(sp) for sp in site.getsitepackages())
            except Exception:
                pass
            if not result:
                pyver = f"python{sys.version_info[0]}.{sys.version_info[1]}"
                result.append(Path(sys.prefix) / "lib" / pyver / "site-packages")
        local._hermes_site_packages = list(result)
    result = list(local._hermes_site_packages)

    runtime_venv = _validated_runtime_venv()
    if runtime_venv is not None:
        from pm.environments import site_packages
        runtime_site_packages = site_packages(runtime_venv)
        if not any(_same_path(runtime_site_packages, existing) for existing in result):
            result.append(runtime_site_packages)
    return result


def _strip_hermes_owned_pythonpath_and_runtime_markers(env: dict) -> None:
    """Strip Hermes-owned PYTHONPATH entries, then the runtime marker vars."""
    _strip_hermes_owned_pythonpath(env)
    for _marker in _ACTIVE_VENV_MARKER_VARS:
        env.pop(_marker, None)


def _strip_hermes_owned_pythonpath(env: dict) -> None:
    """Remove Hermes-owned PYTHONPATH entries: only exact matches of the repo root
    (any launcher spelling) and runtime site-packages — never descendants, which are
    user paths. Empty components (= cwd) and everything else are preserved.

    Everything else -- user libs, Nix plugin paths, a pythonX.Y/site-packages entry meant for a DIFFERENT
    child version -- is preserved byte-for-byte: ownership is decided by path provenance, never by a
    cross-version heuristic (#74817 follow-up).
    """
    pp = env.get("PYTHONPATH")
    if not pp:
        return
    owned_paths = [*_get_hermes_site_packages(), *_state()._hermes_repo_root_aliases]
    entries = pp.split(os.pathsep)
    stripped = [e for e in entries if e and any(_same_path(Path(e), p) for p in owned_paths)]
    kept = [e for e in entries if e not in stripped]
    if kept:
        env["PYTHONPATH"] = os.pathsep.join(kept)
    else:
        env.pop("PYTHONPATH", None)
    if stripped:
        logger.debug("Stripped Hermes-owned entries from PYTHONPATH: %s", stripped)
