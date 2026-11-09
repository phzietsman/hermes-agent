"""Host-OS gating for ``@pytest.mark.platforms(...)``, applied by the conftest collection hook."""
import sys

import pytest

# ---------------------------------------------------------------------------
# OS gating
#
# Hermes runs on Linux and macOS, and some of its behaviour genuinely differs
# per host: launchd vs systemd, Keychain vs libsecret, /proc vs sysctl.
#
# Faking the host (patching ``sys.platform`` inside a Linux CI job) gives a
# green test on a machine where the code under test could not actually run.
# So a test whose subject is genuinely OS-specific declares the OS it belongs
# to and runs there for real —
#
#   @pytest.mark.platforms("macos")     → only on macOS (``sys.platform == "darwin"``)
#   @pytest.mark.platforms("linux")     → only on Linux (``sys.platform.startswith("linux")``)
#
# Elsewhere the test is skipped, not faked. CI runs a dedicated macOS job
# (``-m platforms("macos")``) so those markers are actually exercised on
# their own host rather than quietly skipped everywhere.
#
# Pure functions that TAKE a platform as data stay host-independent: passing
# "darwin" as an argument is not faking the host. The line is: if the test
# needs the interpreter to BELIEVE it is on another OS in order to pass, it
# belongs on that OS.
# ---------------------------------------------------------------------------

_PLATFORM_ALIASES = {
    "linux": ("linux",),
    "macos": ("darwin", "macos"),
    "posix": ("linux", "darwin"),
    "any": (),
}


def _platform_machine() -> str:
    import platform as _platform

    machine = (_platform.machine() or "").lower()
    return {"aarch64": "arm64"}.get(machine, machine)


def _host_matches_platforms(conditions, arch=None, arch_negate=False):
    """Evaluate a platforms() marker payload against the running host.

    Returns ``(ok, skip_reason)``.
    """
    host = sys.platform.lower()
    machine = _platform_machine()
    specs = [str(c).strip().lower() for c in conditions if str(c).strip()]
    if not specs:
        return True, "platforms() with no specs matches every host"
    # An unknown spec is a collection error, never a skip: a typo like
    # platforms("linx") would otherwise drop the test on every host while
    # both lanes stay green — the exact failure the gate exists to catch.
    unknown = [spec for spec in specs if spec.removeprefix("not ").strip() not in _PLATFORM_ALIASES]
    if unknown:
        raise pytest.UsageError(
            f"platforms(): unknown spec(s) {', '.join(map(repr, unknown))} — valid: "
            f"{', '.join(sorted(_PLATFORM_ALIASES))}, each optionally prefixed with 'not '"
        )
    for spec in specs:
        negate = spec.startswith("not ")
        leaf = spec[4:].strip() if negate else spec
        wanted = _PLATFORM_ALIASES[leaf]
        matched = (not wanted) or host in wanted
        if negate:
            matched = not matched
        if matched:
            break
    else:
        return False, f"platforms({', '.join(specs)}); host is {sys.platform}"
    if arch is not None:
        arch_l = str(arch).lower()
        arch_hit = machine == arch_l or (
            arch_l in {"arm64", "aarch64"} and machine == "arm64"
        )
        if arch_negate:
            arch_hit = not arch_hit
        if not arch_hit:
            return False, (
                f"platforms(arch={'not ' if arch_negate else ''}{arch}); "
                f"host machine is {machine or 'unknown'}"
            )
    return True, ""


def _platforms_gate_reason(item):
    """Skip reason when the item's platforms() gating excludes this host."""
    for mark in item.iter_markers("platforms"):
        kwargs = dict(mark.kwargs)
        conds = list(mark.args)
        try:
            ok, reason = _host_matches_platforms(
                conds,
                arch=kwargs.pop("arch", None),
                arch_negate=kwargs.pop("arch_negate", False),
            )
        except pytest.UsageError as exc:
            raise pytest.UsageError(f"{item.nodeid}: {exc}") from None
        if kwargs:
            raise pytest.UsageError(
                f"{item.nodeid}: platforms() got unexpected keyword(s) "
                f"{sorted(kwargs)} — valid: arch, arch_negate"
            )
        if not ok:
            return reason
    return None


def _reject_contradictory_platform_marks(items):
    """Fail collection when one test carries two platforms() markers.

    Two markers are ANDed by the gate, so a stacked pair is not always wrong
    in principle — but the historic failure this guard exists for (a
    module-level gate stacking with a per-test gate so the test is skipped
    on every host while both lanes report green) is only diagnosable at
    collection time. A test that needs a compound condition writes ONE
    marker: platforms("linux", arch="arm64").
    """
    offenders = []
    retired = []
    for item in items:
        marks = list(item.iter_markers("platforms"))
        if len(marks) > 1:
            offenders.append(f"  {item.nodeid}: {len(marks)} platforms() marks")
        for legacy in ("linux_only", "macos_only"):
            if item.get_closest_marker(legacy):
                retired.append(f"  {item.nodeid}: {legacy}")
    if retired:
        # An unregistered mark is a warning, so a merge that resurrects the old
        # pair would make a host-gated test RUN on every host, unnoticed.
        raise pytest.UsageError(
            "linux_only/macos_only were replaced by platforms(...); rewrite:\n"
            + "\n".join(retired)
        )
    if offenders:
        raise pytest.UsageError(
            "a test may carry at most one platforms() marker — combine the "
            'specs into one call (platforms("linux", arch="arm64") instead '
            "of stacking two markers); these carry several:\n"
            + "\n".join(offenders)
        )
