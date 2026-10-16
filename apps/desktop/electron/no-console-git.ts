// Git spawning helpers: timed git commands run in their own POSIX process group
// so a promisor fetch cannot outlive the git process or the app.

import { type ChildProcess, spawn, type SpawnOptions } from 'node:child_process'

const PIPED_STDIO = ['ignore', 'pipe', 'pipe'] as const

export function hiddenGitSpawnSpec(gitBin: string, args: string[], options: SpawnOptions = {}) {
  return {
    command: gitBin || 'git',
    args,
    options: {
      ...options,
      env: (options.env as NodeJS.ProcessEnv | undefined) || process.env,
      stdio: options.stdio ?? [...PIPED_STDIO]
    } as SpawnOptions
  }
}

// Timed commands still running. On POSIX they lead their own process group,
// so nothing reaches them when the app exits unless we do it here (#125243:
// probes and their promisor fetches outliving a restart, reparented to PID 1).
const timedChildren = new Set<ChildProcess>()

// A POSIX group outlives its leader, so it is always signaled.
function hasTreeToKill(child: ChildProcess): child is ChildProcess & { pid: number } {
  return Boolean(child.pid)
}

function killGroup(child: ChildProcess & { pid: number }): void {
  try {
    process.kill(-child.pid, 'SIGKILL')
  } catch {
    child.kill('SIGKILL')
  }
}

/** Kill every timed git command still running, with its descendants. For app quit. */
export function killTimedGitChildren(): void {
  const live = [...timedChildren].filter(hasTreeToKill)
  timedChildren.clear()

  live.forEach(killGroup)
}

export function execGit(
  gitBin: string,
  args: string[],
  options: { cwd?: string; env?: NodeJS.ProcessEnv; timeoutMs?: number } = {}
): Promise<{ code: number | null; stdout: string; stderr: string }> {
  const spec = hiddenGitSpawnSpec(gitBin, args, {
    cwd: options.cwd,
    env: options.env,
    // Timed commands own a POSIX group so a promisor fetch cannot outlive
    // the git process.
    detached: Boolean(options.timeoutMs),
    stdio: ['ignore', 'pipe', 'pipe']
  })

  return new Promise((resolve, reject) => {
    const child = spawn(spec.command, spec.args, spec.options)
    let stdout = ''
    let stderr = ''
    let settled = false
    let timeoutError: (NodeJS.ErrnoException & { stderr?: string }) | undefined

    const finish = (error?: Error, code: number | null = child.exitCode) => {
      if (settled) {
        return
      }

      settled = true

      if (timer) {
        clearTimeout(timer)
      }

      if (error) {
        reject(error)

        return
      }

      resolve({ code, stdout, stderr })
    }

    const timer = options.timeoutMs
      ? setTimeout(() => {
          timeoutError = Object.assign(new Error('git timed out'), { code: 'ETIMEDOUT', stderr })

          const done = () => {
            // Pipes held by descendants must not keep Electron alive after a
            // failed tree kill. The timeout remains a failure, never exit 0.
            child.stdout?.destroy()
            child.stderr?.destroy()
            timedChildren.delete(child)
            finish(timeoutError)
          }

          if (!hasTreeToKill(child)) {
            done()
          } else {
            killGroup(child)
            done()
          }
        }, options.timeoutMs)
      : null

    child.stdout?.on('data', chunk => {
      stdout += chunk.toString()
    })
    child.stderr?.on('data', chunk => {
      stderr += chunk.toString()
    })

    if (timer) {
      timedChildren.add(child)
      child.once('close', () => timedChildren.delete(child))
    }

    child.once('error', error => {
      timedChildren.delete(child)
      finish(error)
    })
    child.once('close', code => {
      if (!timeoutError) {
        finish(undefined, code)
      }
    })
  })
}
