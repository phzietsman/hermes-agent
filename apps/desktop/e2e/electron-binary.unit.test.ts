import * as path from 'node:path'

import { describe, expect, it } from 'vitest'

import { electronDistCandidates } from './electron-binary'

describe('electronDistCandidates', () => {
  const desktop = path.join('repo', 'apps', 'desktop')
  const repo = 'repo'

  it('probes the workspace-local install before the hoisted one', () => {
    // npm only hoists `electron` to the repo root when nothing conflicts, so
    // apps/desktop/node_modules is an ordinary outcome of `npm install`, not a
    // broken tree. Probing only the repo root is what makes the suite refuse to
    // start with "run npm install" on a tree that has electron installed.
    expect(electronDistCandidates([desktop, repo])).toEqual([
      path.join(desktop, 'node_modules', 'electron', 'dist', 'electron'),
      path.join(repo, 'node_modules', 'electron', 'dist', 'electron'),
    ])
  })
})
