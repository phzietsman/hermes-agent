/**
 * desktop-uninstall.ts
 *
 * Pure, electron-free helpers for the desktop Chat GUI uninstaller. These map
 * the three user-facing uninstall modes to the `hermes uninstall` CLI flags,
 * resolve the running app bundle so a detached cleanup script can remove
 * it after the app quits, and build that cleanup script.
 *
 * Kept electron-free so Vitest can exercise the registered IPC handlers.
 * main.ts supplies the local install stamp and process callbacks.
 *
 * The three modes mirror the CLI's options exactly:
 *   - 'gui'  → remove ONLY the Chat GUI, keep the agent + all user data.
 *              `hermes uninstall --gui --yes`
 *   - 'lite' → remove the GUI + agent code, KEEP user data (config / sessions
 *              / .env) for a future reinstall. `hermes uninstall --yes`
 *   - 'full' → remove everything: GUI + agent + all user data.
 *              `hermes uninstall --full --yes`
 *
 * Why a detached cleanup script: 'lite'/'full' delete the very venv the
 * `hermes` command runs from, and every mode may need to delete the running
 * app bundle (locked on macOS while the process is alive). So we hand
 * the work to a detached child that waits for this app's PID to exit, runs the
 * Python uninstall, then removes the app bundle — then the app quits. Same
 * shape as the self-update swap-and-relaunch flow already in main.ts.
 */

import path from 'node:path'

import type { InstallStamp } from './install-stamp'

export interface UninstallSummaryDetails {
  hermes_home: string
  agent_installed: boolean
  gui_installed: boolean
  source_built_artifacts: string[]
  packaged_app_paths: string[]
  userdata_dir: string
  userdata_exists: boolean
  platform: string
  running_app_path?: string | null
  probe?: string
}

export interface DesktopUninstallSummary extends UninstallSummaryDetails {
  code_removal_allowed: boolean
  /** Set only when the OS or package manager owns removal; the renderer shows it verbatim. */
  native_removal_instructions: string | null
}

export interface DesktopUninstallResult {
  ok: boolean
  mode?: string
  willRemoveAppBundle?: boolean
  scriptPath?: string
  error?: string
  message?: string
}

export interface DesktopUninstallIpcDeps {
  ipcMain: {
    handle: (channel: string, handler: (event: unknown, payload?: unknown) => Promise<unknown>) => void
  }
  stamp: Readonly<Partial<Pick<InstallStamp, 'distribution' | 'source' | 'payload' | 'updateMechanism'>>> | null
  fallbackSummary: () => UninstallSummaryDetails
  probeSummary: () => Promise<UninstallSummaryDetails>
  runUninstall: (mode: string) => Promise<DesktopUninstallResult>
  removableAppPath: () => string | null
}

export function registerDesktopUninstallIpc({
  ipcMain,
  stamp,
  fallbackSummary,
  probeSummary,
  runUninstall,
  removableAppPath
}: DesktopUninstallIpcDeps): void {
  const kind: InstallKind = resolveInstallKind(stamp ?? {})
  const codeRemovalAllowed: boolean = installKindAllowsCodeRemoval(kind)

  ipcMain.handle('hermes:uninstall:summary', async (): Promise<DesktopUninstallSummary> => {
    const summary: UninstallSummaryDetails = codeRemovalAllowed ? await probeSummary() : fallbackSummary()

    // The local artifact owns this decision, not the Python summary. When
    // removal belongs to the OS, the main process also supplies the canonical
    // instructions so the renderer never guesses platform or install kind.
    return {
      ...summary,
      code_removal_allowed: codeRemovalAllowed,
      native_removal_instructions: codeRemovalAllowed
        ? null
        : nativeRemovalInstructions(kind, summary.platform, removableAppPath())
    }
  })
  ipcMain.handle(
    'hermes:uninstall:run',
    async (_event: unknown, payload?: unknown): Promise<DesktopUninstallResult> => {
      // Every cleanup mode can remove the bundle, including a hidden data request.
      if (!codeRemovalAllowed) {
        return {
          ok: false,
          error: 'externally-managed',
          message: 'This desktop install must be removed through its installer or package manager.'
        }
      }

      const mode: unknown = payload && typeof payload === 'object' && 'mode' in payload ? payload.mode : payload
      const requestedMode: string = String(mode || '')

      if (!allowedUninstallModes(kind).includes(requestedMode)) {
        return { ok: false, error: 'invalid-mode', message: `Unknown uninstall mode: ${requestedMode}` }
      }

      return runUninstall(requestedMode)
    }
  )
}

const UNINSTALL_MODES: string[] = ['gui', 'lite', 'full', 'data']

// The baked install stamp determines who owns removal:
//   'nix'      — a Nix build (stamp distribution 'nix'). The store is
//                immutable and the install is owned by Nix tooling, so the
//                app must not remove any code, its own bundle included.
//   'bundled'  — a bundled or light artifact. The OS owns app removal.
//   'external' — another package manager owns updates and removal.
//   'standard' — everything else: the git-clone install the desktop
//                installer bootstraps, or a `hermes desktop` source build.
//                The classic script flow (venv python + rm the bundle) works.
//
// Only 'standard' installs may use the desktop cleanup script.
const INSTALL_KINDS = ['nix', 'bundled', 'external', 'standard'] as const
type InstallKind = (typeof INSTALL_KINDS)[number]

/**
 * Classify the install from the stamp. Pure so it can be unit-tested:
 * callers pass the baked stamp, never the connected backend's install facts.
 * `distribution` is authoritative; `source` is the schema-1 fallback.
 * The 'bundled' and 'light' artifact kinds both classify as the managed
 * 'bundled' flow — neither has agent code the app may remove, and the OS
 * owns app removal.
 */
function resolveInstallKind({
  distribution,
  source,
  payload = 'bootstrap',
  updateMechanism
}: NonNullable<DesktopUninstallIpcDeps['stamp']> = {}): InstallKind {
  if (distribution === 'nix' || source === 'nix') {
    return 'nix'
  }

  if (payload === 'bundled' || payload === 'light') {
    return 'bundled'
  }

  if (updateMechanism === 'external') {
    return 'external'
  }

  return 'standard'
}

/** True when this install kind lets the app remove code (agent / bundle). */
function installKindAllowsCodeRemoval(kind: InstallKind): boolean {
  return kind === 'standard'
}

/**
 * Desktop cleanup always removes the bundle. No mode is safe for an install
 * owned by another installer, including the CLI's data-only mode.
 */
function allowedUninstallModes(kind: InstallKind): string[] {
  return installKindAllowsCodeRemoval(kind) ? ['gui', 'lite', 'full'] : []
}

/**
 * Human instructions for removing the app itself the native way. Used when
 * the install kind forbids code removal: macOS owns the bundled app
 * through the Trash, a Linux AppImage is a
 * single file the user placed somewhere, and a Nix install belongs to the
 * flake / profile that made it. `appPath` is the resolveRemovableAppPath()
 * result (the .app bundle on macOS, the AppImage path on Linux), used only to
 * name the exact bundle or file.
 */
function nativeRemovalInstructions(kind, platform, appPath = null) {
  if (kind === 'nix') {
    return (
      'This Hermes desktop app was installed by Nix. Uninstall it the same way you installed it: ' +
      'remove hermes-agent from your flake or profile, then rebuild.'
    )
  }

  if (kind === 'external') {
    // Deleting a package manager's files by hand leaves the package registered with missing files.
    return (
      'Another package manager installed this Hermes desktop app. ' +
      'Uninstall it with that package manager, the same way you installed it.'
    )
  }

  if (platform === 'darwin') {
    // Name the running bundle: channel builds ship under different .app names.
    const bundle = appPath ? path.posix.basename(String(appPath)) : 'the Hermes app'
    const folder = appPath ? path.posix.dirname(String(appPath)) : 'Applications'

    return `Quit the app and drag ${bundle} from ${folder} to the Trash.`
  }

  if (appPath && /\.appimage$/i.test(String(appPath))) {
    return `Delete the AppImage file at ${appPath}.`
  }

  if (appPath) {
    return `Delete the app directory at ${appPath}.`
  }

  return 'Delete the Hermes AppImage (or app directory) from wherever you saved it.'
}

/**
 * Map an uninstall mode to the `python -m hermes_cli.uninstall` argv (after the
 * python executable). Uses the dedicated lightweight module entrypoint (not
 * `hermes_cli.main`) so it can run under a system Python OUTSIDE the venv that
 * lite/full delete, so it is not deleted out from under itself.
 * Throws on an unknown mode so a typo can't silently become a full wipe.
 */
function uninstallArgsForMode(mode: string) {
  if (!UNINSTALL_MODES.includes(mode)) {
    throw new Error(`Unknown uninstall mode: ${mode}`)
  }

  return ['-m', 'hermes_cli.uninstall', '--mode', mode]
}

/** True when `mode` removes the agent code (lite/full), false otherwise. */
function modeRemovesAgent(mode: string) {
  return mode === 'lite' || mode === 'full'
}

/** True when `mode` removes user data (full and data). */
function modeRemovesUserData(mode: string) {
  return mode === 'full' || mode === 'data'
}

/**
 * Resolve the on-disk app bundle/dir to remove for the running desktop app,
 * given the path to the running executable (`process.execPath`) and platform.
 *
 *   macOS:   …/Hermes.app/Contents/MacOS/Hermes  → …/Hermes.app
 *   Linux:   AppImage → the APPIMAGE env path; unpacked → the *-unpacked dir
 *
 * Returns null when we can't confidently identify a removable bundle (e.g.
 * running from a dev checkout, or a system-package install we must not rmtree).
 */
function resolveRemovableAppPath(execPath, platform, env: any = {}) {
  const exe = String(execPath || '')

  if (!exe) {
    return null
  }

  const p = path.posix

  if (platform === 'darwin') {
    // …/Hermes.app/Contents/MacOS/Hermes → strip 3 segments to the .app
    const macOsDir = p.dirname(exe) // …/Contents/MacOS
    const contents = p.dirname(macOsDir) // …/Contents
    const appBundle = p.dirname(contents) // …/Hermes.app

    if (appBundle.endsWith('.app')) {
      return appBundle
    }

    return null
  }

  // Linux: an AppImage exposes its own path via the APPIMAGE env var.
  if (env.APPIMAGE) {
    return env.APPIMAGE
  }

  // Unpacked electron-builder tree: …/linux-unpacked/hermes
  const dir = p.dirname(exe)

  if (/-unpacked$/.test(dir)) {
    return dir
  }

  return null
}

/**
 * Should we even try to remove the running app bundle from a cleanup script?
 * Only when packaged AND we resolved a concrete removable path. Dev runs
 * (electron from node_modules) and system-package installs return null above
 * and are left to the OS package manager.
 */
function shouldRemoveAppBundle(isPackaged, appPath) {
  return Boolean(isPackaged) && Boolean(appPath)
}

/**
 * Build a POSIX cleanup shell script (macOS / Linux). It:
 *   1. waits (bounded ~30s) for the desktop PID to exit (venv/bundle unlock),
 *   2. runs the Python uninstall module with the mode,
 *   3. removes the app bundle if one was resolved.
 *
 * `pythonExe` should be a Python OUTSIDE the venv for lite/full (the venv is
 * being deleted); `pythonPath` is prepended to PYTHONPATH so `import hermes_cli`
 * resolves from the agent source. `q()` single-quote-escapes for the shell
 * (closes-escapes-reopens any embedded apostrophe), defending against spaces.
 */
function buildPosixCleanupScript({ desktopPid, pythonExe, pythonPath, agentRoot, uninstallArgs, appPath, hermesHome }) {
  const q = s => `'${String(s).replace(/'/g, `'\\''`)}'`

  const lines = [
    '#!/usr/bin/env bash',
    'set -u',
    '# Wait (up to ~30s) for the desktop process to exit so the venv python',
    '# and the app bundle are no longer in use.',
    `pid=${Number(desktopPid) || 0}`,
    'if [ "$pid" -gt 0 ]; then',
    '  for _ in $(seq 1 60); do',
    '    kill -0 "$pid" 2>/dev/null || break',
    '    sleep 0.5',
    '  done',
    'fi',
    `export HERMES_HOME=${q(hermesHome)}`
  ]

  if (pythonPath) {
    lines.push(`export PYTHONPATH=${q(pythonPath)}\${PYTHONPATH:+:$PYTHONPATH}`)
  }

  lines.push(`cd ${q(agentRoot)} 2>/dev/null || true`, `${q(pythonExe)} ${uninstallArgs.map(q).join(' ')} || true`)

  if (appPath) {
    lines.push(`rm -rf ${q(appPath)} || true`)
  }

  // Self-delete the script.
  lines.push('rm -f "$0" 2>/dev/null || true')
  lines.push('')

  return lines.join('\n')
}

export {
  allowedUninstallModes,
  buildPosixCleanupScript,
  INSTALL_KINDS,
  installKindAllowsCodeRemoval,
  modeRemovesAgent,
  modeRemovesUserData,
  nativeRemovalInstructions,
  resolveInstallKind,
  resolveRemovableAppPath,
  shouldRemoveAppBundle,
  UNINSTALL_MODES,
  uninstallArgsForMode
}
