"""Commit rows bind a literal producer's receipt, not merely object names."""
import pytest

from tests.scripts.test_render_builds_publication import rbt

SHA = 'a' * 40
PACKAGE = 'HermesBundled-1.2.3-mac-x64.dmg'
OTHER = 'HermesBundled-1.2.4-mac-x64.dmg'
PREFIX = f'releases/commit/{SHA}/'


@pytest.mark.parametrize('listed,objects,state,diagnostic', [
    (None, [PACKAGE], 'receipt-missing', 'leg incomplete or upload interrupted'),
    (['metadata-macos-x64.json'], [PACKAGE], 'receipt-omits', 'artifact absent from receipt'),
    ([PACKAGE], [], 'object-missing', 'receipt present but object missing'),
    ([PACKAGE, OTHER], [PACKAGE, OTHER], 'ambiguous', 'ambiguous: multiple objects match'),
    ([PACKAGE], [PACKAGE, OTHER], 'built', '✅ Built'),
])
def test_commit_row_binding_faults(listed, objects, state, diagnostic):
    receipt = None if listed is None else {
        'schema': 2, 'commit': SHA, 'name': 'darwin-x64',
        'files': [{'path': path, 'size': 1, 'sha256': '0' * 64} for path in listed],
    }
    receipts = {'darwin-x64': receipt}
    keys = [PREFIX + path for path in objects]
    row = next(row for row in rbt.commit_expected_rows(keys, receipts)
               if row['label'] == 'macOS Intel (DMG)')
    assert row == {'label': 'macOS Intel (DMG)', 'leg': 'darwin-x64',
                   'state': state, 'key': PREFIX + PACKAGE if state == 'built' else None}
    summary = rbt.render_commit_summary(keys, 'https://cdn.example', SHA, receipts)
    page = rbt.render_commit_page(SHA, keys, 'https://cdn.example', receipts)
    for output in (summary, page):
        assert diagnostic in output
        assert ('https://cdn.example/' + PREFIX + PACKAGE in output) == (state == 'built')
    assert rbt.recorded_build(page) == SHA
    assert 'Bundle environment' not in page


@pytest.mark.parametrize('needs,expected', [
    (None, []), ('', []), ('not json', []),
    ('{"build-termux":{"result":"success"},"build-darwin":{"result":"failure"},'
     '"termux-deb":{"result":"skipped"},"build-linux":{"result":"failure"}}',
     ['build-darwin', 'build-linux']),
])
def test_failed_legs_from_release_needs(needs, expected):
    assert rbt.failed_legs_from_release_needs(needs) == expected


@pytest.mark.parametrize('state,status', [
    ('success', 'Passed'), ('failure', 'Failed'), ('cancelled', 'Cancelled'),
    ('skipped', 'Not run'), (None, 'Not run (no result)'),
])
def test_download_survives_smoke_failure_without_claiming_acceptance(state, status):
    receipt = {'schema': 2, 'commit': SHA, 'name': 'darwin-x64',
               'files': [{'path': PACKAGE, 'size': 1, 'sha256': '0' * 64}]}
    needs = {} if state is None else {'smoke-darwin-x64': {'result': state}}
    inputs = ([PREFIX + PACKAGE], 'https://cdn.example', SHA, {'darwin-x64': receipt})
    summary = rbt.render_commit_summary(*inputs, smoke_results=needs)
    page = rbt.render_commit_page(SHA, inputs[0], inputs[1], inputs[3], smoke_results=needs)
    assert f'| macOS DMG + ZIP (x64) | {status} |' in summary
    assert '| macOS DMG + ZIP (arm64) | Not run (no result) |' in summary
    assert f'<td>macOS DMG + ZIP (x64)</td><td>{status}</td>' in page
    for output in (summary, page):
        assert '✅ Built' in output and 'https://cdn.example/' + PREFIX + PACKAGE in output
        assert 'Linux bundles are disabled' in output
    missing = rbt.render_commit_summary([], 'https://cdn.example', SHA, {},
                                       failed_legs=['termux-deb'])
    assert 'failed: termux-deb' in missing
