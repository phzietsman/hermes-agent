import fs from 'node:fs'
import { connect } from 'node:net'
import os from 'node:os'
import path from 'node:path'

import { expect, test, vi } from 'vitest'

import { execGit, killTimedGitChildren } from './no-console-git'

// A node "git" that spawns a grandchild owning a TCP listener, then hangs:
// the listener only goes away if the whole tree is killed.
function hangingTreeFixture() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'git-timeout-'))
  const pids = path.join(dir, 'pids.json')
  const fixture = path.join(dir, 'hang.cjs')

  fs.writeFileSync(
    fixture,
    `
    const { spawn } = require('node:child_process');
    const worker = String.raw\`const fs = require('node:fs');
      const server = require('node:net').createServer(socket => socket.end());
      server.listen(0, '127.0.0.1', () => {
        fs.writeFileSync(process.argv[1] + '.tmp',
          JSON.stringify({ pids: [Number(process.argv[2]), process.pid], port: server.address().port }));
        fs.renameSync(process.argv[1] + '.tmp', process.argv[1]);
      });\`;
    spawn(process.execPath, ['-e', worker, process.argv[2], String(process.pid)], { stdio: 'inherit' });
    setInterval(() => {}, 1000);
  `
  )

  const listening = (port: number): Promise<boolean> =>
    new Promise(resolve => {
      const socket = connect(port, '127.0.0.1')

      const done = (alive: boolean) => {
        socket.destroy()
        resolve(alive)
      }

      socket.once('connect', () => done(true))
      socket.once('error', () => done(false))
      socket.setTimeout(2000, () => done(true))
    })

  const port = async (): Promise<number> => {
    await vi.waitFor(() => expect(fs.existsSync(pids)).toBe(true), { timeout: 5000 })

    return JSON.parse(fs.readFileSync(pids, 'utf8')).port
  }

  const cleanup = () => {
    if (fs.existsSync(pids)) {
      for (const pid of JSON.parse(fs.readFileSync(pids, 'utf8')).pids) {
        try {
          process.kill(pid, 'SIGKILL')
        } catch {
          /* already reaped */
        }
      }
    }

    vi.unstubAllEnvs()
    fs.rmSync(dir, { recursive: true, force: true })
  }

  return { args: [fixture, pids], cleanup, listening, port }
}

test('a timed-out git command reaps descendants', async () => {
  const tree = hangingTreeFixture()

  try {
    const success = await execGit(process.execPath, ['-e', 'process.stdout.write("ok")'], { timeoutMs: 5000 })
    expect(success).toMatchObject({ code: 0, stdout: 'ok' })

    const error = await execGit(process.execPath, tree.args, { timeoutMs: 5000 }).catch(error => error)
    const port = await tree.port()
    await vi.waitFor(async () => expect(await tree.listening(port)).toBe(false), { timeout: 3000 })
    expect(error).toMatchObject({ code: 'ETIMEDOUT' })
  } finally {
    tree.cleanup()
  }
}, 20_000)

test('app quit kills a timed git command still running, descendants included', async () => {
  const tree = hangingTreeFixture()

  try {
    const pending = execGit(process.execPath, tree.args, { timeoutMs: 60_000 }).catch(error => error)
    const port = await tree.port()
    expect(await tree.listening(port)).toBe(true)

    killTimedGitChildren()

    await vi.waitFor(async () => expect(await tree.listening(port)).toBe(false), { timeout: 3000 })
    await pending
  } finally {
    tree.cleanup()
  }
}, 20_000)
