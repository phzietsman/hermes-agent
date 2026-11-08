"""Desktop-application resolver over an `AppDef` (parsed from an MCP manifest's `app:` block).

`locate` stats every declared location in order (fixed paths, PATH, the flatpak, snap and
Applications directories) and lists directories for a `*` path segment; the first present
one wins. `inspect` reads the version source in-process.
`probe` re-reads the vendor's runtime file on every call; the bearer token in it never
leaves this module.
"""

from __future__ import annotations

import glob
import json
import os
import plistlib
import re
import time
from dataclasses import dataclass
from typing import Callable, Literal
from urllib.parse import urlsplit

from hermes_platform.resolver import known_dirs
from hermes_platform.resolver.base import Effort, Inspection, Probe
from hermes_platform.resolver.core import (
    Candidate,
    CheckState,
    Kind,
    LookupContext,
    Observation,
    Resolution,
    locate_command,
)

PresenceKind = Literal["executable", "bundle"]
VersionKind = Literal["plist", "none"]
LivenessKind = Literal["server_json", "none"]

_LOOPBACK_HOSTS = frozenset({"127.0.0.1", "localhost", "::1", "[::1]"})


@dataclass(frozen=True)
class AppLocation:
    """One place to look. `kind` is `path` or a key of `LOCATION_KINDS`. `value` is the path, or the
    value of that kind's `key`."""

    kind: str
    value: str


@dataclass(frozen=True)
class AppDef:
    """One application on one OS. Paths use `$VAR` and `~`; expansion happens at lookup."""

    app_id: str
    os_family: str
    presence: PresenceKind
    locations: tuple[AppLocation, ...]
    version_kind: VersionKind = "none"
    liveness_kind: LivenessKind = "none"
    liveness_path: str = ""
    liveness_pid_key: str = "pid"
    liveness_url_key: str = "http"
    liveness_token_key: str = "token"
    endpoint_path: str = "/mcp"


def _expand(path: str) -> str:
    return os.path.expandvars(os.path.expanduser(path))


@dataclass(frozen=True)
class Endpoint:
    url: str
    token: str = ""

    def __repr__(self) -> str:
        return f"Endpoint(url={self.url!r}, token=<redacted>)"


@dataclass(frozen=True)
class AppResolver:
    definition: AppDef

    @property
    def name(self) -> str:
        return self.definition.app_id

    # ---- locate: stat only -------------------------------------------------------------

    def locate(self, ctx: LookupContext | None = None) -> Resolution:
        d = self.definition
        found = [hit for loc in d.locations for hit in _locator(loc.kind)(d, loc, ctx)]
        kind: Kind = next((k for c, k in found if c.present), "missing")
        return Resolution(kind, tuple(c for c, _ in found))

    # ---- inspect: bounded file reads and in-process OS APIs -----------------------------

    def inspect(self, res: Resolution, ctx: LookupContext | None = None) -> Inspection:
        if not res.found:
            return Inspection(Observation.not_checked(), Observation.not_checked())
        return Inspection(version=self._version(res.command[0]), signer=Observation.not_checked())

    def _version(self, path: str) -> Observation[str]:
        kind = self.definition.version_kind
        try:
            if kind == "none":
                return Observation.not_checked()
            if kind == "plist":
                return _plist_version(path)
        except Exception as exc:  # a vendor's plist is untrusted input; never abort the caller
            return Observation(CheckState.ERROR, detail=exc.__class__.__name__)
        return Observation(CheckState.UNAVAILABLE, detail=f"unknown version kind {kind}")

    # ---- probe: fresh, never cached ------------------------------------------------------

    def endpoint(self) -> Endpoint | None:
        """Read and validate the current runtime endpoint."""
        d = self.definition
        if d.liveness_kind != "server_json":
            return None
        session = _read_server_json(_expand(d.liveness_path), d)
        if session is None or _pid_alive(session.pid).value is not True:
            return None
        endpoint = _endpoint_observation(session.url, d.endpoint_path)
        if endpoint.state is not CheckState.PRESENT or not endpoint.value:
            return None
        return Endpoint(endpoint.value, session.token)

    def probe(self, res: Resolution, *, effort: Effort, deadline_s: float = 3.0) -> Probe:
        d = self.definition
        nc: Observation = Observation.not_checked()
        if d.liveness_kind != "server_json":
            return Probe(running=nc, answering=nc, endpoint=nc)
        session = _read_server_json(_expand(d.liveness_path), d)
        if session is None:
            absent = Observation(CheckState.ABSENT, False, "runtime file missing or unreadable")
            return Probe(running=absent, answering=nc, endpoint=Observation(CheckState.ABSENT))
        running = _pid_alive(session.pid)
        endpoint_obs = _endpoint_observation(session.url, d.endpoint_path)
        if effort is Effort.LOCAL or running.value is not True or endpoint_obs.state is not CheckState.PRESENT:
            return Probe(running=running, answering=nc, endpoint=endpoint_obs)
        answering = _mcp_initialize(session, endpoint_obs.value or "", deadline_s)
        return Probe(running=running, answering=answering, endpoint=endpoint_obs)


# ---- locations: each yields (candidate, resolution kind) in probe order -----------------

_Hit = tuple[Candidate, Kind]
_Locator = Callable[[AppDef, AppLocation, LookupContext | None], list[_Hit]]


def _present(presence: PresenceKind, path: str) -> bool:
    if presence == "bundle":
        return os.path.isdir(path) and os.path.isfile(os.path.join(path, "Contents", "Info.plist"))
    return os.path.isfile(path)


def _hit(d: AppDef, source: str, path: str) -> _Hit:
    present = os.path.isabs(path) and _present(d.presence, path)
    return Candidate(path, source, present), "known_path"


def _version_order(path: str) -> list:
    return [(1, int(part)) if part.isdigit() else (0, part) for part in re.split(r"(\d+)", path)]


def _path_hits(d: AppDef, loc: AppLocation, ctx: LookupContext | None) -> list[_Hit]:
    target, source = _expand(loc.value), f"app:{d.app_id}"
    not_found: list[_Hit] = [(Candidate(target, source, False), "known_path")]
    if not os.path.isabs(target) or "%" in target or "$" in target:
        return not_found
    if "*" not in target:
        return [_hit(d, source, target)]
    # A `*` segment stands for a versioned folder (`Blender 5.2`); the highest version is tried first.
    pattern = "*".join(glob.escape(part) for part in target.split("*"))
    matches = sorted(glob.glob(pattern), key=_version_order, reverse=True)
    return [_hit(d, source, match) for match in matches] or not_found


def _command_hits(d: AppDef, loc: AppLocation, ctx: LookupContext | None) -> list[_Hit]:
    res = locate_command(loc.value, ctx)
    return [(Candidate(c.value, f"app:{d.app_id}:{loc.kind}", c.present), "path_executable") for c in res.candidates]


def _in_dirs(dirs: Callable[[], tuple[str, ...]]) -> _Locator:
    def hits(d: AppDef, loc: AppLocation, ctx: LookupContext | None) -> list[_Hit]:
        return [_hit(d, f"app:{d.app_id}:{loc.kind}", os.path.join(_expand(root), loc.value)) for root in dirs()]
    return hits


@dataclass(frozen=True)
class LocationSpec:
    """A location kind a declaration names in a mapping. `only_on` is the one OS family it is valid
    under (None: any), `key` names the mapping field holding what to find, `yields` is the presence
    it finds."""

    only_on: str | None
    key: str
    yields: PresenceKind
    locate: _Locator


LOCATION_KINDS: dict[str, LocationSpec] = {
    "command": LocationSpec(None, "name", "executable", _command_hits),
    "app_bundle": LocationSpec("darwin", "name", "bundle", _in_dirs(known_dirs.mac_application_dirs)),
    "flatpak": LocationSpec("linux", "app_id", "executable", _in_dirs(known_dirs.flatpak_export_dirs)),
    "snap": LocationSpec("linux", "name", "executable", _in_dirs(known_dirs.snap_bin_dirs)),
}


def _locator(kind: str) -> _Locator:
    """A plain string location is a `path`; every other kind comes from `LOCATION_KINDS`."""
    return _path_hits if kind == "path" else LOCATION_KINDS[kind].locate


# ---- version sources -------------------------------------------------------------------


def _plist_version(bundle: str) -> Observation[str]:
    with open(os.path.join(bundle, "Contents", "Info.plist"), "rb") as fh:
        info = plistlib.load(fh)
    value = info.get("CFBundleShortVersionString") or info.get("CFBundleVersion")
    if not value:
        return Observation(CheckState.UNAVAILABLE, detail="no version key in Info.plist")
    return Observation(CheckState.PRESENT, str(value))


# ---- liveness: the token stays inside this section -------------------------------------


@dataclass(frozen=True)
class _Session:
    pid: int | None
    url: str
    token: str

    def __repr__(self) -> str:
        return f"_Session(pid={self.pid}, url={self.url!r}, token=<redacted>)"


def _read_server_json(path: str, d: AppDef) -> _Session | None:
    try:
        with open(path, encoding="utf-8") as fh:
            data = json.load(fh)
    except (OSError, ValueError):
        return None
    if not isinstance(data, dict):
        return None
    pid = data.get(d.liveness_pid_key)
    url = data.get(d.liveness_url_key)
    token = data.get(d.liveness_token_key)
    return _Session(
        pid=pid if isinstance(pid, int) else None,
        url=url if isinstance(url, str) else "",
        token=token if isinstance(token, str) else "",
    )


def _pid_alive(pid: int | None) -> Observation[bool]:
    if pid is None or pid <= 0:
        return Observation(CheckState.UNAVAILABLE, detail="no pid in runtime file")
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return Observation(CheckState.ABSENT, False)
    except PermissionError:
        return Observation(CheckState.PRESENT, True)
    return Observation(CheckState.PRESENT, True)


def _endpoint_observation(raw_url: str, fixed_path: str) -> Observation[str]:
    """Accept only a loopback http URL with a numeric port and no userinfo; the path is ours."""
    if not raw_url:
        return Observation(CheckState.UNAVAILABLE, detail="no url in runtime file")
    try:
        parts = urlsplit(raw_url)
        hostname, port, username, password = parts.hostname, parts.port, parts.username, parts.password
    except ValueError:
        return Observation(CheckState.UNAVAILABLE, detail="malformed endpoint")
    if parts.scheme != "http" or username or password:
        return Observation(CheckState.UNAVAILABLE, detail="endpoint must be plain http without userinfo")
    if hostname not in _LOOPBACK_HOSTS:
        return Observation(CheckState.UNAVAILABLE, detail="endpoint must be loopback")
    if port is None or not (1 <= port <= 65535):
        return Observation(CheckState.UNAVAILABLE, detail="endpoint needs a numeric port")
    return Observation(CheckState.PRESENT, f"http://{hostname}:{port}{fixed_path}")


def _mcp_initialize(session: _Session, endpoint: str, deadline_s: float) -> Observation[bool]:
    """One MCP `initialize` POST under one absolute deadline covering connect, headers, and body."""
    import http.client

    parts = urlsplit(endpoint)
    body = json.dumps({
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {"protocolVersion": "2025-06-18", "capabilities": {},
                   "clientInfo": {"name": "hermes", "version": "probe"}},
    }).encode()
    headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
    if session.token:
        headers["Authorization"] = f"Bearer {session.token}"
    deadline = time.monotonic() + max(0.1, deadline_s)

    def remaining() -> float:
        left = deadline - time.monotonic()
        if left <= 0:
            raise TimeoutError
        return left

    conn = http.client.HTTPConnection(parts.hostname or "127.0.0.1", parts.port or 80, timeout=remaining())
    try:
        conn.request("POST", parts.path or "/", body=body, headers=headers)
        conn.sock.settimeout(remaining())
        status = conn.getresponse().status
    except TimeoutError:
        return Observation(CheckState.ABSENT, False, f"no answer within {deadline_s:g} s")
    except (OSError, http.client.HTTPException):
        return Observation(CheckState.ABSENT, False, "connection refused or timed out")
    finally:
        conn.close()
    if status in (200, 401, 403):
        return Observation(CheckState.PRESENT, True, f"http {status}")
    return Observation(CheckState.ABSENT, False, f"http {status}")
