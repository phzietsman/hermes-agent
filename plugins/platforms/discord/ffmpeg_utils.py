"""ffmpeg discovery for Discord voice: ``tools.transcription_audio`` owns the shared lookup
(PATH + Homebrew/local prefixes); this layers an explicit ``FFMPEG_PATH`` override on top."""

from __future__ import annotations

import os
import shutil


def _shared_find_ffmpeg():
    """Delegate to the repo-wide ffmpeg discovery helper when importable."""
    try:
        from tools.transcription_audio import _find_ffmpeg_binary
    except ImportError:  # standalone plugin import (tests / sandboxes)
        return shutil.which("ffmpeg")
    return _find_ffmpeg_binary()


def resolve_ffmpeg_executable() -> str:
    """Return an ffmpeg command, honouring an explicit ``FFMPEG_PATH``."""
    explicit = (os.getenv("FFMPEG_PATH") or "").strip()
    if explicit:
        return os.path.expandvars(os.path.expanduser(explicit))
    if discovered := _shared_find_ffmpeg():
        return discovered
    return "ffmpeg"
