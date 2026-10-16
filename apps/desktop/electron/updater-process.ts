import { spawn, type SpawnOptions, spawnSync, type SpawnSyncReturns } from 'node:child_process'
import { existsSync, realpathSync, statSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'

/** Historical layouts still matter when uninstalling or migrating a source install. */
export function resolveVenvDir(updateRoot: string): string {
  for (const name of ['venv', '.venv']) {
    const candidate: string = path.join(updateRoot, name)

    try {
      if (statSync(candidate).isDirectory()) {
        return candidate
      }
    } catch {
      // Try the other supported layout, then retain the legacy diagnostic path.
    }
  }

  return path.join(updateRoot, 'venv')
}

import { platformDefaultHermesHome } from './data-paths'

/** Exact installation identity; PATH may refer to another checkout. */
export function resolveInstallationLauncher(
  updateRoot: string,
  hermesHome: string = process.env.HERMES_HOME ?? ''
): string | null {
  const managed: string = path.join(updateRoot, '.hermes', 'bin', 'hermes')

  if (stagedFileExists(managed)) {
    return managed
  }

  // Earlier PM installers published only to user-bin. Trust that historical
  // launcher only after its existing version surface proves exact source identity.
  if (stagedFileExists(path.join(updateRoot, 'hermes_cli', '_launchers.py'))) {
    for (const candidate of userBinLaunchers(hermesHome)) {
      if (launcherTargetsInstallation(candidate, updateRoot)) {
        return candidate
      }
    }
  }

  // An old shim is a migration rung, never a damaged PM install fallback.
  if (!existsSync(path.join(updateRoot, 'pm'))) {
    const legacy: string = path.join(updateRoot, 'venv', 'bin', 'hermes')

    if (stagedFileExists(legacy)) {
      return legacy
    }
  }

  return null
}

/** Published user-bin launchers, at fixed locations: a GUI launch's PATH may omit them. */
function userBinLaunchers(hermesHome: string): string[] {
  const defaultHome: string = platformDefaultHermesHome(os.homedir(), process.env)

  const dirs: string[] = [path.join(os.homedir(), '.local', 'bin'), path.join(hermesHome || defaultHome, 'bin')]

  return [...new Set(dirs)].map((dir: string): string => path.join(dir, 'hermes')).filter(stagedFileExists)
}

/**
 * A source install outside the canonical root (install.sh --dir, a
 * setup-hermes.sh clone) is reachable only through the user-bin launcher it
 * published. Return the install directory that launcher reports, when it is a
 * Hermes source tree; the caller still resolves and probes it by root.
 */
export function userLauncherInstallRoot(
  hermesHome: string = process.env.HERMES_HOME ?? ''
): { launcher: string; root: string } | null {
  for (const launcher of userBinLaunchers(hermesHome)) {
    const root: string | null = launcherInstallDirectory(launcher)

    if (root && existsSync(path.join(root, 'hermes_cli', 'main.py'))) {
      return { launcher, root }
    }
  }

  return null
}

export function launcherTargetsInstallation(launcher: string, root: string): boolean {
  try {
    const reported: string | null = launcherInstallDirectory(launcher, root)

    return reported !== null && reported === realpathSync(root)
  } catch {
    return false
  }
}

/** The real install directory a launcher's `--version` reports, or null. */
function launcherInstallDirectory(launcher: string, root?: string): string | null {
  try {
    const probe: SpawnSyncReturns<string> = spawnSync(launcher, ['--version'], {
      cwd: root,
      encoding: 'utf8',
      timeout: 15000,
      env: root ? { ...process.env, HERMES_INSTALL_ROOT: root } : process.env
    })

    if (probe.error || probe.status !== 0) {
      return null
    }

    const reported: string | undefined = /^Install directory: (.+)$/m.exec(probe.stdout)?.[1]?.trim()

    return reported ? realpathSync(reported) : null
  } catch {
    return null
  }
}

export interface UpdaterChild {
  pid?: number
  unref: () => void
}

export interface ResolveUpdateScriptHandoffDeps {
  fileExists?: (candidate: string) => boolean
}

export interface UpdateScriptHandoff {
  command: string
  args: string[]
  scriptPath: string
}

/**
 * Repo-owned POSIX update hand-off.
 *
 * Replaces the in-app posix updater: the Desktop spawns the script detached
 * and QUITS, the script waits it out, runs `hermes update`, swaps/relaunches
 * the app, and writes .hermes-update-result.json. With the app gone before
 * the update starts, the HERMES_DESKTOP_CHILD_PID reaper-exclusion dance is
 * unnecessary — there are no live desktop backends to spare.
 *
 * Null when the checkout predates the script (caller surfaces the manual
 * `hermes update` card — old checkouts pull the script on their next update).
 */
export function resolvePosixScriptHandoff(
  updateRoot: string,
  deps: ResolveUpdateScriptHandoffDeps = {}
): UpdateScriptHandoff | null {
  const scriptPath = path.join(updateRoot, 'scripts', 'desktop-update', 'posix.sh')
  const exists = deps.fileExists ?? stagedFileExists

  if (!exists(scriptPath)) {
    return null
  }

  return {
    command: '/bin/bash',
    args: [scriptPath],
    scriptPath
  }
}

/**
 * Electron/Chromium internal switches that must NOT be replayed on re-exec:
 * runtime artifacts of THIS launch, not user intent (ported from the deleted
 * update-relaunch.ts; #45205). `--no-sandbox` is deliberately kept — it is
 * the user's sandbox opt-out and the signal that makes a relaunch safe when
 * chrome-sandbox isn't setuid.
 */
export const INTERNAL_ARG_PREFIXES = [
  '--type=',
  '--user-data-dir=',
  '--enable-features=',
  '--disable-features=',
  '--field-trial-handle=',
  '--enable-logging',
  '--log-file=',
  '--disable-gpu-sandbox',
  '--lang=',
  '--inspect',
  '--remote-debugging-port='
]

/** Filter Electron internals from process.argv.slice(1) so the relaunched
 * app replays only user/launcher intent (deep links, app flags). */
export function collectRelaunchArgs(argv: unknown): string[] {
  if (!Array.isArray(argv)) {
    return []
  }

  return argv.filter((arg): arg is string => {
    if (typeof arg !== 'string' || arg.length === 0) {
      return false
    }

    return !INTERNAL_ARG_PREFIXES.some(prefix =>
      prefix.endsWith('=') ? arg.startsWith(prefix) : arg === prefix || arg.startsWith(prefix + '=')
    )
  })
}

/** True when the user has opted out of the SUID sandbox — the relaunch is
 * safe even if chrome-sandbox fails preflight (ported from update-relaunch.ts). */
export function sandboxFallbackFromEnv(env: Record<string, string | undefined>, launchArgs: string[]): boolean {
  const disable = String(env?.ELECTRON_DISABLE_SANDBOX || '').trim()

  if (disable === '1' || disable.toLowerCase() === 'true') {
    return true
  }

  return Array.isArray(launchArgs) && launchArgs.includes('--no-sandbox')
}

function stagedFileExists(candidate: string): boolean {
  try {
    return statSync(candidate).isFile()
  } catch {
    return false
  }
}

export interface SpawnUpdaterProcessDeps {
  spawnProcess?: (command: string, args: string[], options: SpawnOptions) => UpdaterChild
}

/**
 * Spawn the detached installer used for update and bootstrap-recovery handoffs.
 * The helper owns unref semantics so every
 * updater handoff follows the same behavior and can be tested without Electron.
 */
export function spawnUpdaterProcess(
  updater: string,
  updaterArgs: string[],
  options: SpawnOptions,
  deps: SpawnUpdaterProcessDeps = {}
): UpdaterChild {
  const child = deps.spawnProcess
    ? deps.spawnProcess(updater, updaterArgs, options)
    : spawn(updater, updaterArgs, options)

  child.unref()

  return child
}

/**
 * Stop a hand-off the Desktop has given up on (C2 timeout), so a script that
 * starts late cannot run an update the UI already reported as "did not start".
 * The detached launcher leads its own process group: kill the group. Best
 * effort; the script is adopt-only besides (A4).
 */
export function killHandoffTree(
  child: UpdaterChild,
  { kill = process.kill.bind(process) }: { kill?: typeof process.kill } = {}
): void {
  const pid = child.pid

  if (!Number.isInteger(pid) || !pid || pid <= 0) {
    return
  }

  try {
    kill(-pid, 'SIGKILL')
  } catch {
    try {
      kill(pid, 'SIGKILL')
    } catch {
      // Already gone.
    }
  }
}

export interface UpdaterHandoffOutcome {
  ok: boolean
  /** Set when ok is false. */
  reason?: 'spawn-error' | 'early-exit'
  /** Human-readable detail for logs (never contains argv secrets). */
  message?: string
  /** Exit code when the child exited inside the settle window. */
  code?: number | null
  /** Signal when the child was killed inside the settle window. */
  signal?: string | null
}

export interface ObserveUpdaterHandoffDeps {
  setTimeoutFn?: (callback: () => void, ms: number) => unknown
  clearTimeoutFn?: (timer: unknown) => void
}

/**
 * User-facing copy for a hand-off that did not take (spawn error or early exit).
 * The lead sentence is plain: nothing changed and Hermes keeps running. The raw
 * outcome message (exit code / signal / spawn error) stays on a trailing
 * "Details:" line for logs and support.
 */
export function describeUpdaterHandoffFailure(outcome: Pick<UpdaterHandoffOutcome, 'message'>): string {
  const lead =
    "The updater couldn't start, so nothing was changed and Hermes keeps running as before. " +
    'Try again; if it keeps failing, open the logs and send them to support.'

  return outcome.message ? `${lead}\n\nDetails: ${outcome.message}` : lead
}

/**
 * Watch a just-spawned detached updater for the duration of the quit dwell
 * and report whether the hand-off actually became viable (#66753).
 *
 * Before this, the Desktop called `unref()` and quit after a fixed dwell
 * without ever observing the child's async `error` event (ENOENT/EACCES —
 * Node reports exec failures asynchronously) or an early `exit`. A failed
 * spawn therefore looked identical to a successful one: the app vanished, no
 * updater appeared, and nothing relaunched. Worse, an unhandled `'error'`
 * event on the detached child would crash the Electron main process outright.
 *
 * Success is: no `error` event AND either the child survives the settle
 * window or it exits 0 inside it. Failure is a
 * spawn `error`, a non-zero exit, or a signal death inside the window.
 *
 * Children that expose no event interface (bare test doubles) settle as ok
 * after the window — the observation is a best-effort hardening, never a new
 * way to wedge an update.
 */
export function observeUpdaterHandoff(
  child: UpdaterChild,
  settleMs: number,
  deps: ObserveUpdaterHandoffDeps = {}
): Promise<UpdaterHandoffOutcome> {
  const setTimeoutFn = deps.setTimeoutFn ?? setTimeout

  const clearTimeoutFn =
    deps.clearTimeoutFn ?? ((timer: unknown) => clearTimeout(timer as ReturnType<typeof setTimeout>))

  const observable = child as UpdaterChild & {
    once?: (event: string, listener: (...args: unknown[]) => void) => unknown
    removeListener?: (event: string, listener: (...args: unknown[]) => void) => unknown
  }

  if (typeof observable.once !== 'function') {
    return new Promise(resolve => {
      setTimeoutFn(() => resolve({ ok: true }), settleMs)
    })
  }

  return new Promise(resolve => {
    let settled = false

    const finish = (outcome: UpdaterHandoffOutcome) => {
      if (settled) {
        return
      }

      settled = true
      clearTimeoutFn(timer)
      observable.removeListener?.('error', onError)
      observable.removeListener?.('exit', onExit)
      resolve(outcome)
    }

    const onError = (...args: unknown[]) => {
      const error = args[0] as (Error & { code?: string }) | undefined

      finish({
        ok: false,
        reason: 'spawn-error',
        message: `updater spawn failed: ${error?.code || error?.message || 'unknown error'}`
      })
    }

    const onExit = (...args: unknown[]) => {
      const code = args[0] as number | null
      const signal = args[1] as string | null

      if (signal || (typeof code === 'number' && code !== 0)) {
        finish({
          ok: false,
          reason: 'early-exit',
          message: signal
            ? `updater died from signal ${signal} before the settle window elapsed`
            : `updater exited ${code} before the settle window elapsed`,
          code: code ?? null,
          signal: signal ?? null
        })

        return
      }

      // Clean exit 0 inside the window is expected for wrapper shapes.
      finish({ ok: true, code: code ?? 0, signal: null })
    }

    const timer = setTimeoutFn(() => finish({ ok: true }), settleMs)

    observable.once('error', onError)
    observable.once('exit', onExit)
  })
}
