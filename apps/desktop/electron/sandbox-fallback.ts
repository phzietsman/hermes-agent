/**
 * Linux Chromium/Electron sandbox recovery (#121954).
 *
 * On hosts where the sandboxed GPU child cannot start (it dies pre-main on an
 * FD-ownership violation), Chromium prints "GPU process isn't usable.
 * Goodbye." and aborts: a 100% crash loop where only `--no-sandbox` reaches
 * the UI. `--no-sandbox` is enabled only on strong evidence: the GPU child
 * dying with SIGTERM (exit 143), or TWO consecutive mid-boot aborts. The
 * fallback is sticky per app version: after an update the sandbox is
 * re-probed once.
 *
 * Pure helpers stay injectable so tests never boot Electron.
 */

import fs from 'node:fs'
import path from 'node:path'

export const SANDBOX_MARKER_FILENAME = 'sandbox-fallback.json'

/**
 * Exit status Chromium uses to SIGTERM its GPU process when it never became
 * usable (#121954: sandbox-blocked GPU child dies pre-main on an
 * FD-ownership violation, browser prints "GPU process isn't usable.
 * Goodbye." and SIGTERMs it). Node reports signal deaths as `null` exitCode
 * + `signalName: 'SIGTERM'`; Electron serializes that as exitCode 143.
 */
export const GPU_CHILD_SANDBOX_SIGTERM_EXIT = 143

/** Consecutive mid-boot aborts required before enabling --no-sandbox. */
export const BOOT_ABORTS_BEFORE_FALLBACK = 2

export type SandboxMarkerState = 'booting' | 'fallback' | 'ok'

export type SandboxFallbackReason = 'gpu-breakpoint' | 'renderer-crash-loop' | 'boot-loop'

export interface SandboxMarker {
  state: SandboxMarkerState
  /** Why the fallback engaged (state === 'fallback'). */
  reason?: SandboxFallbackReason
  /** App version that entered fallback — a version change triggers a re-probe. */
  version?: string
  /** Consecutive aborted boots observed so far (state === 'booting'). */
  bootAborts?: number
  /** This boot is a sandbox re-probe after an app update; an abort returns
   *  straight to fallback instead of restarting the two-strike count. */
  reprobe?: boolean
}

export function sandboxMarkerPath(userDataDir: string): string {
  return path.join(String(userDataDir || ''), SANDBOX_MARKER_FILENAME)
}

export function alreadyHasNoSandbox(argv: readonly string[] = [], env: NodeJS.ProcessEnv = process.env): boolean {
  if (Array.isArray(argv) && argv.some(arg => arg === '--no-sandbox')) {
    return true
  }

  const disable = String(env.ELECTRON_DISABLE_SANDBOX || '')
    .trim()
    .toLowerCase()

  return disable === '1' || disable === 'true' || disable === 'yes' || disable === 'on'
}

const FALLBACK_REASONS: readonly string[] = ['gpu-breakpoint', 'renderer-crash-loop', 'boot-loop']

export function parseSandboxMarker(raw: unknown): SandboxMarker | null {
  if (!raw || typeof raw !== 'object') {
    return null
  }

  const record = raw as Record<string, unknown>
  const state = record.state

  if (state !== 'booting' && state !== 'fallback' && state !== 'ok') {
    return null
  }

  const marker: SandboxMarker = { state }

  if (typeof record.reason === 'string' && FALLBACK_REASONS.includes(record.reason)) {
    marker.reason = record.reason as SandboxFallbackReason
  }

  if (typeof record.version === 'string' && record.version) {
    marker.version = record.version
  }

  const aborts = Number(record.bootAborts)

  if (Number.isInteger(aborts) && aborts > 0) {
    marker.bootAborts = aborts
  }

  if (record.reprobe === true) {
    marker.reprobe = true
  }

  return marker
}

export function readSandboxMarker(userDataDir: string, { readFileSync = fs.readFileSync } = {}): SandboxMarker | null {
  try {
    const raw = JSON.parse(readFileSync(sandboxMarkerPath(userDataDir), 'utf8'))

    return parseSandboxMarker(raw)
  } catch {
    return null
  }
}

export function writeSandboxMarker(
  userDataDir: string,
  marker: SandboxMarker,
  {
    mkdirSync = fs.mkdirSync,
    writeFileSync = fs.writeFileSync
  }: {
    mkdirSync?: typeof fs.mkdirSync
    writeFileSync?: typeof fs.writeFileSync
  } = {}
): void {
  const dir = String(userDataDir || '')

  if (!dir) {
    return
  }

  mkdirSync(dir, { recursive: true })
  writeFileSync(sandboxMarkerPath(dir), `${JSON.stringify(marker)}\n`, 'utf8')
}

export interface SandboxLaunchDecision {
  enable: boolean
  reason: string | null
  /** Marker to persist immediately, before GPU/sandbox children start. */
  nextMarker: SandboxMarker
}

/**
 * Single launch-time transition: decide whether this launch disables
 * the Chromium sandbox AND what the marker becomes for crash-detection on the
 * next launch.
 *
 * - `booting` left behind → the prior launch aborted mid-boot. One abort is
 *   tolerated (could be a kill/power loss); the SECOND consecutive abort — or
 *   a single abort during a post-update re-probe — engages the fallback.
 * - `fallback` is sticky within one app version. A version change re-probes
 *   the sandbox once so a fixed host (new Electron, installer ACL repair)
 *   returns to full sandboxing instead of degrading forever.
 * - A manual `--no-sandbox` / ELECTRON_DISABLE_SANDBOX launch is honored but
 *   NOT made sticky: the marker keeps its normal lifecycle so the flag's
 *   removal restores the sandbox.
 */
export function decideSandboxLaunch(
  options: {
    platform?: NodeJS.Platform | string
    argv?: readonly string[]
    env?: NodeJS.ProcessEnv
    marker?: SandboxMarker | null
    appVersion?: string
  } = {}
): SandboxLaunchDecision {
  const appVersion = String(options.appVersion || '')

  const launchPlatform = options.platform ?? process.platform

  if (launchPlatform !== 'linux') {
    return { enable: false, reason: null, nextMarker: { state: 'booting' } }
  }

  const argv = options.argv ?? process.argv
  const env = options.env ?? process.env
  const marker = options.marker ?? null

  if (alreadyHasNoSandbox(argv, env)) {
    // Honor the explicit flag; keep the marker lifecycle unchanged. When the
    // relaunch path set the flag, the fallback marker it wrote is preserved.
    const nextMarker: SandboxMarker = marker?.state === 'fallback' ? marker : { state: 'booting' }

    return { enable: true, reason: 'already-enabled', nextMarker }
  }

  if (marker?.state === 'fallback') {
    if (marker.version && appVersion && marker.version !== appVersion) {
      // App updated since the fallback engaged — re-probe the sandbox once.
      return {
        enable: false,
        reason: null,
        nextMarker: { state: 'booting', reprobe: true, bootAborts: 0 }
      }
    }

    return {
      enable: true,
      reason: 'sticky-fallback',
      nextMarker: { ...marker, version: marker.version || appVersion || undefined }
    }
  }

  if (marker?.state === 'booting') {
    const abortsObserved = (marker.bootAborts ?? 0) + 1

    if (marker.reprobe) {
      // The one post-update sandboxed re-probe aborted → back to fallback.
      return {
        enable: true,
        reason: 'reprobe-failed',
        nextMarker: fallbackMarker('boot-loop', appVersion)
      }
    }

    if (abortsObserved >= BOOT_ABORTS_BEFORE_FALLBACK) {
      return {
        enable: true,
        reason: 'boot-loop',
        nextMarker: fallbackMarker('boot-loop', appVersion)
      }
    }

    return {
      enable: false,
      reason: null,
      nextMarker: { state: 'booting', bootAborts: abortsObserved }
    }
  }

  // No marker, or a clean `ok` from the previous run.
  return { enable: false, reason: null, nextMarker: { state: 'booting' } }
}

export function fallbackMarker(reason: SandboxFallbackReason, appVersion?: string): SandboxMarker {
  const marker: SandboxMarker = { state: 'fallback', reason }

  if (appVersion) {
    marker.version = appVersion
  }

  return marker
}

/**
 * After the main window reaches ready-to-show: keep the sticky fallback when
 * we launched with `--no-sandbox`, otherwise mark a clean boot so future
 * launches trust the sandbox again.
 */
export function markerAfterSuccessfulBoot(options: {
  fallbackActive: boolean
  reason?: SandboxFallbackReason
  appVersion?: string
}): SandboxMarker {
  if (!options.fallbackActive) {
    return { state: 'ok' }
  }

  return fallbackMarker(options.reason ?? 'boot-loop', options.appVersion)
}

/**
 * True when a GPU child died with the #38216 breakpoint signature and we
 * should one-shot relaunch with `--no-sandbox` before Chromium FATAL-exits.
 */
export function shouldRelaunchForGpuSandboxCrash(options: {
  platform?: NodeJS.Platform | string
  details?: { type?: string; exitCode?: number | string; signalName?: string } | null
  alreadyNoSandbox?: boolean
  relaunchAttempted?: boolean
}): boolean {
  const platform = options.platform ?? process.platform

  if (options.alreadyNoSandbox || options.relaunchAttempted) {
    return false
  }

  if (String(options.details?.type || '').toLowerCase() !== 'gpu') {
    return false
  }

  if (platform === 'linux') {
    // #121954: the sandbox-blocked GPU child never comes up; Chromium
    // SIGTERMs it (exit 143) right before its own FATAL "Goodbye." abort.
    // Deliberately narrow: GPU-only, needs the explicit signature.
    return (
      options.details?.exitCode === GPU_CHILD_SANDBOX_SIGTERM_EXIT &&
      String(options.details?.signalName || '').toUpperCase() === 'SIGTERM'
    )
  }

  return false
}

export function buildNoSandboxRelaunchArgs(argv: readonly string[]): string[] {
  const args = (Array.isArray(argv) ? argv : []).filter(arg => arg !== '--no-sandbox')

  args.push('--no-sandbox')

  return args
}
