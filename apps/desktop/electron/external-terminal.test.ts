import assert from 'node:assert/strict'

import { test } from 'vitest'

import {
  buildTerminalScript,
  posixQuote,
  resolveTerminalLaunch,
  terminalScriptEnv,
  terminalScriptExtension,
  tuiResumeArgs
} from './external-terminal'

const never = () => null
const always = (command: string) => `/usr/bin/${command}`

test('tuiResumeArgs resumes the session in the TUI', () => {
  assert.deepEqual(tuiResumeArgs('20260814_101010_abc123'), ['--tui', '--resume', '20260814_101010_abc123'])
})

test('tuiResumeArgs pins the profile ahead of the mode flag', () => {
  assert.deepEqual(tuiResumeArgs('sess', 'work'), ['--profile', 'work', '--tui', '--resume', 'sess'])
})

test('posixQuote survives embedded single quotes', () => {
  assert.equal(posixQuote("/tmp/o'brien"), `'/tmp/o'\\''brien'`)
})

test('terminalScriptEnv drops PATH in any casing and keeps the rest', () => {
  const env = terminalScriptEnv(
    { Path: 'C:\\junk', PATH: '/junk', PYTHONPATH: '/repo', PYTHONUTF8: '1' },
    '/home/b/.hermes'
  )

  assert.deepEqual(env, { PYTHONPATH: '/repo', PYTHONUTF8: '1', HERMES_HOME: '/home/b/.hermes' })
})

test('terminalScriptEnv skips empty values and an absent home', () => {
  assert.deepEqual(terminalScriptEnv({ PYTHONPATH: '' }), {})
})

test('buildTerminalScript execs the resolved runtime with its env', () => {
  const script = buildTerminalScript({
    args: ['-m', 'hermes_cli.main', '--tui', '--resume', 'sess'],
    command: '/home/b/.hermes/hermes-agent/venv/bin/python',
    cwd: "/home/b/o'brien",
    env: { PYTHONPATH: '/home/b/.hermes/hermes-agent' }
  })

  assert.equal(
    script,
    [
      '#!/bin/sh',
      `cd '/home/b/o'\\''brien' || exit 1`,
      `export PYTHONPATH='/home/b/.hermes/hermes-agent'`,
      `exec '/home/b/.hermes/hermes-agent/venv/bin/python' '-m' 'hermes_cli.main' '--tui' '--resume' 'sess'`,
      ''
    ].join('\n')
  )
})

test('terminalScriptExtension matches what the platform binds to a terminal', () => {
  assert.equal(terminalScriptExtension('darwin'), '.command')
  assert.equal(terminalScriptExtension('linux'), '.sh')
})

test('macOS opens the script with no -a so LaunchServices picks the user handler', () => {
  assert.deepEqual(resolveTerminalLaunch({ findOnPath: never, platform: 'darwin', scriptPath: '/tmp/x.command' }), {
    command: 'open',
    args: ['/tmp/x.command']
  })
})

test("Linux leads with the user's x-terminal-emulator alternative", () => {
  assert.deepEqual(resolveTerminalLaunch({ findOnPath: always, platform: 'linux', scriptPath: '/tmp/x.sh' }), {
    command: '/usr/bin/x-terminal-emulator',
    args: ['-e', '/bin/sh', '/tmp/x.sh']
  })
})

test('Linux falls down the emulator ladder and omits a flagless terminal', () => {
  const onlyKitty = (command: string) => (command === 'kitty' ? '/usr/bin/kitty' : null)

  assert.deepEqual(resolveTerminalLaunch({ findOnPath: onlyKitty, platform: 'linux', scriptPath: '/tmp/x.sh' }), {
    command: '/usr/bin/kitty',
    args: ['/bin/sh', '/tmp/x.sh']
  })
})

test('Linux with no emulator installed reports no launch', () => {
  assert.equal(resolveTerminalLaunch({ findOnPath: never, platform: 'linux', scriptPath: '/tmp/x.sh' }), null)
})

test('tuiResumeArgs drops a profile value that is not a valid profile id', () => {
  // A non-slug (numeric roster id, display label) must never cross into the
  // TUI launch argv — the CLI used to str()-coerce it into profiles/0 (#88842).
  assert.deepEqual(tuiResumeArgs('sess', 0 as unknown as string), ['--tui', '--resume', 'sess'])
  assert.deepEqual(tuiResumeArgs('sess', ''), ['--tui', '--resume', 'sess'])
  assert.deepEqual(tuiResumeArgs('sess', 'Not A Slug!'), ['--tui', '--resume', 'sess'])
})

test('tuiResumeArgs normalizes a valid profile id like the CLI', () => {
  assert.deepEqual(tuiResumeArgs('sess', 'Work'), ['--profile', 'work', '--tui', '--resume', 'sess'])
})
