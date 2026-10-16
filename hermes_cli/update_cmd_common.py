"""Shared leaf helpers for the ``hermes update`` modules (no module-level Hermes imports; no cycle)."""

import logging
import shlex
import sys
from contextlib import contextmanager

# Log-record parity with the origin module.
logger = logging.getLogger("hermes_cli.update_cmd")


def _record_stop(reason: str, *, without_receipt: "str | None" = None) -> None:
    """Name the exit about to stop this update as one closed token (see update_receipt.record_stop_reason).

    ``without_receipt`` (``refused``/``failed``) is for an exit that fires before the receipt
    opens: a metrics row only. The receipt module is read from ``sys.modules``, never imported:
    an older updater can load this tree after the swap while its own, older ``update_receipt``
    (without these calls) stays loaded.
    """
    receipt = sys.modules.get("hermes_cli.update_receipt")
    if without_receipt is None:
        record = getattr(receipt, "record_stop_reason", None)
        if record is not None:
            record(reason)
    elif (record := getattr(receipt, "record_stop_without_receipt", None)) is not None:
        record(reason, without_receipt)


@contextmanager
def _best_effort(message: str):
    """Run a non-critical update step; swallow ``Exception`` and log it at debug.

    The updater must never die on bookkeeping (receipt, notices, cache seeds):
    ``message`` is the ``%s``-style debug line the inline ``try/except`` used.
    """
    try:
        yield
    except Exception as exc:
        logger.debug(message, exc)


_HOLDER_VALUE_FLAGS_FALLBACK = frozenset({
    "--profile", "-p", "--config", "--model", "-m", "--provider", "--reasoning", "--toolsets", "-t",
    "--skills", "-s", "--continue", "-c", "--resume", "-r", "--oneshot", "-z", "--in", "--usage-file",
})
_holder_value_flags_cache: frozenset | None = None


def _holder_value_flags() -> frozenset:
    """Top-level CLI flags that consume a value, from the REAL parser (nargs != 0); cached per process.

    Derived so the holder classifier can't drift from argparse (a handwritten subset misparsed ``--reasoning high
    serve`` as subcommand ``high`` and ``-m dashboard serve`` as ``dashboard``, #91869). Pre-argparse profile
    selectors are added explicitly (stripped before argparse sees argv). Falls back to a static snapshot when the
    parser can't import — the updater must classify holders even on a broken tree.
    """
    global _holder_value_flags_cache
    if _holder_value_flags_cache is not None:
        return _holder_value_flags_cache
    flags: set[str] = {"--profile", "-p", "--config"}
    try:
        from hermes_cli._parser import build_top_level_parser
        for action in build_top_level_parser()[0]._actions:
            if action.option_strings and action.nargs != 0:
                flags.update(action.option_strings)
        _holder_value_flags_cache = frozenset(flags)
    except Exception:
        _holder_value_flags_cache = _HOLDER_VALUE_FLAGS_FALLBACK
    return _holder_value_flags_cache


def _hermes_holder_subcommand(cmdline: str) -> str | None:
    """The actual Hermes SUBCOMMAND a process argv runs, or None (callers must NOT guess a label).

    Token-based, never substring (``kanban --preserve-cache`` contains "serve"): find the ``hermes_cli.main`` /
    ``hermes`` entry token, return the first following token that isn't a flag or a flag's value.

    Profile selectors (``--profile X``, ``-p X``) are skipped like the canonical gateway matcher does. See
    #90778.
    """
    try:
        tokens = shlex.split(cmdline, posix=False)
    except Exception:
        tokens = cmdline.split()
    # ``python -c <src> … -m hermes_cli.main <subcommand>``: the entry token belongs to the argv the
    # inline source carries for a LATER spawn, not to this holder (#107002) -- unless the source is a
    # Hermes bootstrap running the entry point in this process (#124318).
    from gateway.status_inline_source import command_line_runs_inline_source, inline_bootstrap_argv
    normalized = [t.strip("\"'") for t in tokens]
    if command_line_runs_inline_source(normalized):
        tokens = inline_bootstrap_argv(normalized)
        if tokens is None:
            return None

    def _is_entry(i: int, token: str) -> bool:
        low = token.lower().strip("\"'")
        return (low.endswith("hermes_cli.main") and i > 0 and tokens[i - 1] == "-m") or (
            low.rsplit("/", 1)[-1] == "hermes") or low.endswith("hermes_cli/main.py")

    entry_idx = next((i for i, token in enumerate(tokens) if _is_entry(i, token)), None)
    if entry_idx is None:
        return None
    value_flags = _holder_value_flags()
    i = entry_idx + 1
    while i < len(tokens):
        token = tokens[i]
        if token in value_flags or token.split("=", 1)[0] in value_flags:
            i += 1 if "=" in token else 2  # --flag value consumes two tokens; --flag=value one.
        elif token.startswith("-"):
            i += 1
        else:
            return token.lower()
    return None
