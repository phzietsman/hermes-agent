import assert from 'node:assert/strict'
import type { SpawnOptions } from 'node:child_process'
import path from 'node:path'

import { test } from 'vitest'

import {
  collectRelaunchArgs,
  describeUpdaterHandoffFailure,
  observeUpdaterHandoff,
  resolvePosixScriptHandoff,
  sandboxFallbackFromEnv,
  spawnUpdaterProcess
} from './updater-process'

test('spawnUpdaterProcess preserves updater options', () => {
  let capturedOptions: SpawnOptions | undefined

  spawnUpdaterProcess(
    'hermes-setup',
    ['--update'],
    { detached: true, stdio: 'ignore' },
    {
      spawnProcess: (_command, _args, options) => {
        capturedOptions = options

        return { unref: () => {} }
      }
    }
  )

  assert.deepEqual(capturedOptions, { detached: true, stdio: 'ignore' })
})

test('resolvePosixScriptHandoff returns the bash recipe when the script exists', () => {
  const root = '/home/hermes/.hermes/hermes-agent'
  const expected = path.join(root, 'scripts', 'desktop-update', 'posix.sh')

  const handoff = resolvePosixScriptHandoff(root, {
    fileExists: candidate => candidate === expected
  })

  assert.ok(handoff)
  assert.equal(handoff.command, '/bin/bash')
  assert.deepEqual(handoff.args, [expected])
})

test('resolvePosixScriptHandoff is null when the checkout predates the script', () => {
  const handoff = resolvePosixScriptHandoff('/home/hermes/.hermes/hermes-agent', {
    fileExists: () => false
  })

  assert.equal(handoff, null)
})

test('collectRelaunchArgs drops Electron internals, keeps user/launcher args', () => {
  const argv = [
    '--type=renderer',
    '--user-data-dir=/tmp/x',
    '--enable-features=A,B',
    '--field-trial-handle=123',
    '--enable-logging',
    '--log-file=/tmp/log',
    '--lang=en-US',
    '--inspect=9229',
    '--remote-debugging-port=9222',
    '--no-sandbox',
    'hermes://open/session/abc',
    '--profile=work'
  ]

  assert.deepEqual(collectRelaunchArgs(argv), ['--no-sandbox', 'hermes://open/session/abc', '--profile=work'])
  assert.deepEqual(collectRelaunchArgs(undefined), [])
})

test('sandboxFallbackFromEnv: ELECTRON_DISABLE_SANDBOX / --no-sandbox opt out', () => {
  assert.equal(sandboxFallbackFromEnv({ ELECTRON_DISABLE_SANDBOX: '1' }, []), true)
  assert.equal(sandboxFallbackFromEnv({ ELECTRON_DISABLE_SANDBOX: 'true' }, []), true)
  assert.equal(sandboxFallbackFromEnv({}, ['--no-sandbox']), true)
  assert.equal(sandboxFallbackFromEnv({ ELECTRON_DISABLE_SANDBOX: '0' }, []), false)
  assert.equal(sandboxFallbackFromEnv({}, []), false)
})

// ── observeUpdaterHandoff (#66753) ──────────────────────────────────────────

class FakeChild {
  pid = 1234
  listeners = new Map<string, Array<(...args: unknown[]) => void>>()
  removed: string[] = []

  unref() {}

  once(event: string, listener: (...args: unknown[]) => void) {
    const arr = this.listeners.get(event) ?? []

    arr.push(listener)
    this.listeners.set(event, arr)

    return this
  }

  removeListener(event: string, _listener: (...args: unknown[]) => void) {
    this.removed.push(event)

    return this
  }

  emit(event: string, ...args: unknown[]) {
    for (const listener of this.listeners.get(event) ?? []) {
      listener(...args)
    }
  }
}

function manualTimer() {
  const pending: Array<() => void> = []

  return {
    deps: {
      setTimeoutFn: (callback: () => void, _ms: number) => {
        pending.push(callback)

        return 0
      },
      clearTimeoutFn: () => {}
    },
    fire: () => {
      for (const callback of pending.splice(0)) {
        callback()
      }
    }
  }
}

test('observeUpdaterHandoff reports a spawn error instead of settling ok', async () => {
  const child = new FakeChild()
  const timer = manualTimer()
  const outcomePromise = observeUpdaterHandoff(child, 2500, timer.deps)

  const err: Error & { code?: string } = new Error('spawn ENOENT')

  err.code = 'ENOENT'
  child.emit('error', err)

  const outcome = await outcomePromise

  assert.equal(outcome.ok, false)
  assert.equal(outcome.reason, 'spawn-error')
  assert.match(outcome.message ?? '', /ENOENT/)
})

test('observeUpdaterHandoff reports a non-zero early exit', async () => {
  const child = new FakeChild()
  const timer = manualTimer()
  const outcomePromise = observeUpdaterHandoff(child, 2500, timer.deps)

  child.emit('exit', 127, null)

  const outcome = await outcomePromise

  assert.equal(outcome.ok, false)
  assert.equal(outcome.reason, 'early-exit')
  assert.equal(outcome.code, 127)
})

test('describeUpdaterHandoffFailure leads with plain copy and confines the raw outcome to Details', () => {
  for (const raw of ['updater exited 127 before the settle window elapsed', 'updater spawn failed: ENOENT']) {
    const text = describeUpdaterHandoffFailure({ message: raw })
    const [lead, details] = text.split('\n\nDetails: ')

    assert.ok(lead && !lead.includes(raw))
    assert.equal(details, raw)
  }

  assert.doesNotMatch(describeUpdaterHandoffFailure({}), /Details:/)
})

test('observeUpdaterHandoff reports a signal death inside the window', async () => {
  const child = new FakeChild()
  const timer = manualTimer()
  const outcomePromise = observeUpdaterHandoff(child, 2500, timer.deps)

  child.emit('exit', null, 'SIGTERM')

  const outcome = await outcomePromise

  assert.equal(outcome.ok, false)
  assert.equal(outcome.reason, 'early-exit')
  assert.equal(outcome.signal, 'SIGTERM')
})

test('observeUpdaterHandoff accepts a clean exit 0 ', async () => {
  const child = new FakeChild()
  const timer = manualTimer()
  const outcomePromise = observeUpdaterHandoff(child, 2500, timer.deps)

  child.emit('exit', 0, null)

  const outcome = await outcomePromise

  assert.equal(outcome.ok, true)
  assert.equal(outcome.code, 0)
})

test('observeUpdaterHandoff settles ok when the child survives the window', async () => {
  const child = new FakeChild()
  const timer = manualTimer()
  const outcomePromise = observeUpdaterHandoff(child, 2500, timer.deps)

  timer.fire()

  const outcome = await outcomePromise

  assert.equal(outcome.ok, true)
  assert.equal(outcome.reason, undefined)
  // Listeners must be detached so a post-quit late exit can't fire them.
  assert.deepEqual(child.removed.sort(), ['error', 'exit'])
})

test('observeUpdaterHandoff ignores events after the first settle', async () => {
  const child = new FakeChild()
  const timer = manualTimer()
  const outcomePromise = observeUpdaterHandoff(child, 2500, timer.deps)

  child.emit('exit', 1, null)
  child.emit('error', new Error('late'))
  timer.fire()

  const outcome = await outcomePromise

  assert.equal(outcome.ok, false)
  assert.equal(outcome.reason, 'early-exit')
})

test('observeUpdaterHandoff settles ok for children without an event interface', async () => {
  const timer = manualTimer()
  const outcomePromise = observeUpdaterHandoff({ pid: 1, unref: () => {} }, 2500, timer.deps)

  timer.fire()

  const outcome = await outcomePromise

  assert.equal(outcome.ok, true)
})
