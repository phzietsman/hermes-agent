import assert from 'node:assert/strict'
import os from 'node:os'
import path from 'node:path'

import { afterEach, test, vi } from 'vitest'

import { platformDefaultHermesHome, resolveDesktopHermesHome, resolveDesktopUserData } from './data-paths'
import { controlSocketPath } from './ssh-connection'

afterEach((): void => {
  vi.unstubAllEnvs()
})

test('local SSH sockets use the suffixed default root', (): void => {
  vi.stubEnv('HERMES_DATA_DIR_SUFFIX', 'magic-test')
  const socket: string = controlSocketPath('user', 'host', 22)

  assert.equal(path.dirname(socket), path.join(platformDefaultHermesHome(os.homedir()), 'desktop-ssh'))
})

test('default data roots append the suffix literally', (): void => {
  const home: string = '/home/test'
  const userData: string = path.posix.join(home, 'app-data', 'Hermes')
  const base: string = path.posix.join(home, '.hermes')

  for (const suffix of ['', '-asdfasdf', 'magic-test', ' spaced ']) {
    const env: NodeJS.ProcessEnv = { HERMES_DATA_DIR_SUFFIX: suffix }

    assert.equal(platformDefaultHermesHome(home, env), base + suffix)
    assert.equal(resolveDesktopUserData(userData, env), userData + suffix)
    assert.equal(resolveDesktopHermesHome({ home, env }), base + suffix)
  }
})

test('explicit homes and userData retain precedence', (): void => {
  const home: string = '/home/test'

  const env: NodeJS.ProcessEnv = {
    HERMES_DATA_DIR_SUFFIX: 'magic-test',
    HERMES_HOME: '/explicit/home',
    HERMES_DESKTOP_USER_DATA_DIR: '/explicit/electron'
  }

  assert.equal(resolveDesktopUserData('/default/electron', env), path.resolve(env.HERMES_DESKTOP_USER_DATA_DIR!))
  assert.equal(resolveDesktopHermesHome({ home, env }), env.HERMES_HOME)
  delete env.HERMES_HOME
  assert.equal(resolveDesktopHermesHome({ home, env }), '/explicit/electron/hermes-home')
})
