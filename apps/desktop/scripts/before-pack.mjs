/**
 * before-pack.mjs — electron-builder beforePack hook.
 *
 * Two responsibilities:
 *
 * 1. Removes any stale unpacked app directory (`appOutDir`) before
 *    electron-builder stages the Electron binaries into it.
 *
 * WHY THIS EXISTS
 * ---------------
 * electron-builder's final packaging step copies the stock `electron`
 * binary into `release/<platform>-unpacked/` and then renames it to the
 * product name (`Hermes`). If a PREVIOUS `npm run pack` was interrupted
 * (Ctrl-C, OOM kill, crash, full disk) the unpacked directory is left in a
 * corrupted partial state: it keeps the already-renamed `LICENSE.electron.txt`
 * and the Chromium payload (.pak/.so/icudtl.dat/chrome-sandbox) but is MISSING
 * the `electron` binary itself.
 *
 * On the next run, electron-builder sees the destination directory already
 * populated, skips re-copying the binary it thinks is present, then tries to
 * rename a `electron` file that no longer exists. The build dies with:
 *
 *   ENOENT: no such file or directory, rename
 *   '.../release/linux-unpacked/electron' -> '.../release/linux-unpacked/Hermes'
 *
 * This is a hard failure with no obvious cause for the user — `hermes desktop`
 * just prints "Desktop GUI build failed" and the only fix is to manually
 * `rm -rf` the release directory, which a normal user has no way to know.
 *
 * The packaging step is not idempotent across an interrupted run, so we make
 * it idempotent ourselves: wipe the target unpacked directory up front so
 * electron-builder always stages into a clean tree. This is safe — the
 * directory is a pure build artifact that electron-builder fully recreates
 * on every pack; nothing else depends on its prior contents.
 *
 * Cross-platform: the same partial-state trap exists on macOS
 * (the mac-unpacked Hermes.app bundle), so we
 * clean whatever `appOutDir` electron-builder hands us regardless of platform.
 *
 * Best-effort: a cleanup failure must never mask the real build. We log and
 * resolve rather than throw — worst case electron-builder hits the original
 * ENOENT, which is no worse than not having this hook at all.
 *
 * 2. Copies the target's admitted native tree. Acquisition belongs to native
 * preparation before packaging, never this hook.
 *
 * electron-builder passes a context with:
 *   - appOutDir:            the unpacked app directory about to be staged
 *   - electronPlatformName: 'darwin' | 'linux'
 *   - arch:                 Arch enum (0=ia32, 1=x64, 2=armv7l, 3=arm64, 4=universal)
 */
import { existsSync, rmSync } from 'node:fs'
import path from 'node:path'
import { Arch } from 'electron-builder'
import { copyNativeInputs } from './prepared-native-deps.mjs'
import { removeDirSync } from './stage-native-deps.mjs'

/** @param {string | null | undefined} appOutDir @returns {boolean} */
export function cleanStaleAppOutDir(appOutDir) {
  if (!appOutDir || typeof appOutDir !== 'string') {
    return false
  }
  if (!existsSync(appOutDir)) {
    return false
  }
  // Recursive + force so a half-written tree (read-only bits, partial files)
  // can't block the wipe. retry/maxRetries rides out transient EBUSY.
  rmSync(appOutDir, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 })
  // Without this check a silent rmSync no-op would leave the stale tree and
  // the "removed" log below would lie. Fall back to the libuv-backed walk.
  if (existsSync(appOutDir)) {
    removeDirSync(appOutDir)
  }
  return true
}

/** @param {import("app-builder-lib").BeforePackContext} context @returns {Promise<void>} */
export default async function beforePack(context) {
  const appOutDir = context && context.appOutDir
  try {
    if (cleanStaleAppOutDir(appOutDir)) {
      console.log(`[before-pack] removed stale unpacked dir before staging: ${appOutDir}`)
    }
  } catch (err) {
    // Never fail the build over cleanup; surface why so a genuinely stuck
    // directory (permissions, mount) is still diagnosable.
    console.warn(`[before-pack] could not clean ${appOutDir} (${err.message}); continuing`)
  }

  const platform = context && context.electronPlatformName
  const arch = context && typeof context.arch === 'number' ? Arch[context.arch] : undefined
  if (!platform || !arch) return
  const app = context.packager.projectDir
  const source = path.resolve(app, '../..')
  const nativeDeps = process.env.HERMES_PREPARED_NATIVE_DEPS || path.join(app, 'build/native-deps')
  copyNativeInputs({ source, nativeDeps, out: path.join(app, 'dist/node_modules'), platform, arch })
}