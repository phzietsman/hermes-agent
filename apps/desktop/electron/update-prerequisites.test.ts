import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

import { test } from 'vitest'

import { launcherTargetsInstallation, resolveInstallationLauncher } from './updater-process'

test('PM update prerequisites use the exact published launcher, not checkout venv files', (): void => {
  const root: string = fs.mkdtempSync(path.join(os.tmpdir(), 'update-launcher-'))

  try {
    fs.mkdirSync(path.join(root, 'pm'))
    fs.mkdirSync(path.join(root, '.hermes', 'bin'), { recursive: true })
    const launcher: string = path.join(root, '.hermes', 'bin', 'hermes')
    fs.writeFileSync(launcher, '#!/bin/sh\n')
    assert.equal(resolveInstallationLauncher(root), launcher)
    fs.unlinkSync(launcher)
    assert.equal(resolveInstallationLauncher(root), null)
  } finally {
    fs.rmSync(root, { recursive: true, force: true })
  }
})

test('earlier PM user-bin launchers are accepted only for the reported source tree', (): void => {
  const base: string = fs.mkdtempSync(path.join(os.tmpdir(), 'old-pm-launcher-'))

  try {
    const root: string = path.join(base, 'checkout')
    const other: string = path.join(base, 'other')
    const home: string = path.join(base, 'home')
    fs.mkdirSync(path.join(root, 'hermes_cli'), { recursive: true })
    fs.mkdirSync(path.join(root, 'pm'))
    fs.mkdirSync(other)
    fs.writeFileSync(path.join(root, 'hermes_cli', '_launchers.py'), '')
    fs.mkdirSync(path.join(home, 'bin'), { recursive: true })
    const launcher: string = path.join(home, 'bin', 'hermes')

    const body = (reported: string): string => `#!/bin/sh\nprintf '%s\\n' 'Install directory: ${reported}'\n`

    fs.writeFileSync(launcher, body(root), { mode: 0o755 })
    assert.equal(launcherTargetsInstallation(launcher, root), true)
    assert.equal(resolveInstallationLauncher(root, home), launcher)
    fs.writeFileSync(launcher, body(other), { mode: 0o755 })
    assert.equal(launcherTargetsInstallation(launcher, root), false)
    assert.equal(resolveInstallationLauncher(root, home), null)
  } finally {
    fs.rmSync(base, { recursive: true, force: true })
  }
})
