"""Release stamping changes only metadata consumed by external builders."""
from pathlib import Path


def _tree(root: Path) -> None:
    (root / "hermes_cli").mkdir()
    (root / "hermes_cli" / "__init__.py").write_text(
        '__release_date__ = "2026.1.1"\n', encoding="utf-8")
    (root / "pyproject.toml").write_text('version = "0.0.0"\n', encoding="utf-8")
    desktop = root / "apps" / "desktop"
    desktop.mkdir(parents=True)
    (desktop / "package.json").write_text('{"version": "0.0.0"}\n', encoding="utf-8")
    (root / "package-lock.json").write_text(
        '{"version": "0.0.0", "packages": {'
        '"apps/desktop": {"name": "hermes", "version": "0.0.0"}}}\n',
        encoding="utf-8")
    (root / "uv.lock").write_text(
        '[[package]]\nname = "hermes-agent"\nversion = "0.0.0"\n'
        '[[package]]\nname = "other"\nversion = "0.0.0"\n', encoding="utf-8")
    (root / "nix").mkdir()
    (root / "nix" / "hermes-agent.nix").write_text(
        '{\n  version ? "0.0.0",\n}: version\n', encoding="utf-8")


def test_stamping_only_writes_external_builder_inputs(tmp_path):
    from scripts.releases.stamping import stamp

    build = tmp_path / "build"
    build.mkdir()
    _tree(build)
    inert = [
        build / "hermes_cli" / "__init__.py",
        build / "pyproject.toml",
        build / "uv.lock",
        build / "apps" / "desktop" / "package.json",
        build / "package-lock.json",
    ]
    before = {path: path.read_bytes() for path in inert}

    written = stamp(build, "0.21.5")

    assert not (build / "hermes_cli" / "_version.py").exists()
    assert {path: path.read_bytes() for path in inert} == before
    assert 'version ? "0.21.5"' in (build / "nix" / "hermes-agent.nix").read_text()
    assert written == [build / "nix" / "hermes-agent.nix"]


def test_stamping_a_payload_snapshot_without_external_inputs_is_a_noop(tmp_path):
    import shutil

    from scripts.releases.stamping import stamp

    _tree(tmp_path)
    shutil.rmtree(tmp_path / "apps")

    shutil.rmtree(tmp_path / "nix")

    before = (tmp_path / "pyproject.toml").read_bytes()
    assert stamp(tmp_path, "0.21.5") == []
    assert (tmp_path / "pyproject.toml").read_bytes() == before
