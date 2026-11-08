# Confirmed historical upgrade limitations

These failures cannot be repaired by changing the update target: the failing code is already loaded from the starting release. The workflow still executes each original update path. Only a match on the exact starting commit, method pair, failed assertion, and fresh log signatures produces a non-red known-failure receipt. Other errors still fail. The results table shows matched cases as `known [n]`, with the explanation and evidence in a footnote at the bottom. These cases are counted separately from passed upgrades.

The machine-readable rules in `e2e-assets/known-failures.json` own the matcher and report footnote text. This document explains their historical evidence. Logs are rotated before each attempt so an earlier failure cannot classify a later one.

No historical limitation is currently classified.

## Not classified as unfixable

Onboarding click failures, zoom drift, native permission dialogs, stale update markers, autostash conflicts, network failures, and generic timeouts remain actionable or unclassified until diagnosed. They must not inherit a historical label because they occurred on an old release.
