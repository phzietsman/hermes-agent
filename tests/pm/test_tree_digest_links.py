"""Realized digests bind link text without reading the linked directory."""
from pathlib import Path

from pm.store import tree_digest


def test_directory_links_bind_only_their_target_text(tmp_path):
    root = tmp_path / "tree"
    root.mkdir()
    (root / "payload").write_bytes(b"unchanged")
    left, right = tmp_path / "left", tmp_path / "right"
    for target in (left, right):
        target.mkdir()
        (target / "content").write_bytes(b"same bytes")
    plain = tree_digest(root)
    link = root / "directory"
    link.symlink_to(Path("..") / "left", target_is_directory=True)
    first = tree_digest(root)
    assert first != plain
    (left / "content").write_bytes(b"changed outside the tree")
    assert tree_digest(root) == first
    link.unlink()
    link.symlink_to(Path("..") / "right", target_is_directory=True)
    assert tree_digest(root) != first
    link.unlink()
    link.symlink_to(".", target_is_directory=True)
    cycle = tree_digest(root)
    assert tree_digest(root) == cycle
    assert cycle not in (plain, first)
