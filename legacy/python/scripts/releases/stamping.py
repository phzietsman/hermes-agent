"""Stamp only release metadata consumed by external package builders.

Python and npm package versions are inert source placeholders. Runtime identity
comes from ``install-stamp.json``; this adapter exists only for builders that
require a native version in their own project metadata.
"""
from __future__ import annotations

import argparse
import re
from pathlib import Path


def _rewrite(path: Path, pattern: str, replacement: str, *, count: int = 0, flags: int = 0) -> bool:
    if not path.exists():
        return False
    raw = path.read_bytes()
    newline = "\r\n" if b"\r\n" in raw else "\n"
    text = raw.decode("utf-8-sig").replace("\r\n", "\n")
    updated = re.sub(pattern, replacement, text, count=count, flags=flags)
    path.write_bytes(updated.replace("\n", newline).encode("utf-8"))
    return True


def stamp(tree: Path, version: str) -> list[Path]:
    """Rewrite external builder version inputs under ``tree``."""
    written: list[Path] = []

    nix_package = tree / "nix" / "hermes-agent.nix"
    if _rewrite(
        nix_package,
        r'^  version \? "[^"]+",',
        f'  version ? "{version}",',
        count=1,
        flags=re.MULTILINE,
    ):
        written.append(nix_package)

    return written


def main(argv: list[str] | None = None) -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tree", type=Path, required=True)
    parser.add_argument("--version", required=True)
    args = parser.parse_args(argv)
    stamp(args.tree.resolve(), args.version)


if __name__ == "__main__":
    main()
