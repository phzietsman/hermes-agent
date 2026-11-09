"""Startup recovery never writes to the stdout of the command it runs ahead of.

``update_pause_record.recover()`` resumes gateways an interrupted update paused before ANY ``hermes``
command (``--json`` ones included) and prints its own notices to stderr; the resume path it calls
must do the same. Real resume entry point; the seam is the tree gate's verdict (a torn checkout),
so the deferral notice is what gets printed.
"""

from __future__ import annotations

from hermes_cli import update_pause_record as pause_record
from hermes_cli.update_cmd_posix_pause import resume_paused_gateways_after_update


def test_a_recovering_resume_prints_its_notices_to_stderr(monkeypatch, capsys):
    monkeypatch.setattr(pause_record, "tree_is_whole", lambda token: (False, "the checkout is mid-pull"))
    token = {"resume_needed": True, "pause_id": "p", "recovery": True, "platform": "posix",
             "posix_units": [{"kind": "systemd", "scope": "user", "unit": "hermes-gateway.service"}]}

    resume_paused_gateways_after_update(token)
    out, err = capsys.readouterr()
    assert out == "", "recovery wrote to the unrelated command's stdout"
    assert "left stopped: the checkout is mid-pull" in err
    assert token["resume_deferred"] == "the checkout is mid-pull"
