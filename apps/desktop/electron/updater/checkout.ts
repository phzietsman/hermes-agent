// Checkout update policy and handoff execution. The shell supplies process and UI dependencies.

import { existsSync, readFileSync } from 'node:fs'
import * as path from 'node:path'

import {
  claimBridgeMarker,
  HANDOFF_CLAIM_TIMEOUT_MS,
  makeHandoffRunId,
  updateHandoffConflict,
  waitForHandoffClaim
} from '../update-marker'
import {
  collectRelaunchArgs,
  describeUpdaterHandoffFailure,
  killHandoffTree,
  observeUpdaterHandoff,
  resolveInstallationLauncher,
  resolvePosixScriptHandoff,
  sandboxFallbackFromEnv,
  spawnUpdaterProcess
} from '../updater-process'

import { type SourceUpdate, sourceUpdateEnvironment } from './checkout-source'
import { type MarkerHelperOp, type MarkerHelperVerdict, readHandoffProtocol, runMarkerHelper } from './marker-helper'

import type { UpdaterApplyResultWire, UpdaterMechanism, UpdaterStatusWire, UpdaterStrategy } from './index'

/**
 * Everything the checkout flow needs from the app shell. These are the
 * impure edges only — all update logic lives here.
 */
export interface CheckoutStrategyDeps {
  resolveUpdateRoot: () => string
  readSourceUpdate: (root: string, opts: { force?: boolean }) => Promise<SourceUpdate | null>
  hermesHome: string
  isMac: boolean
  defaultUpdateBranch: string
  updateHandoffDwellMs: number
  /** How long the hand-off script has to take the bridge marker (C2); default 20 s. */
  handoffClaimTimeoutMs?: number
  /**
   * True when one remote gateway serves this Desktop (app-global remote /
   * cloud / SSH). The hand-off then tells `hermes update` not to (re)start a
   * local messaging gateway: with the same channel credentials as the remote
   * host it would become a competing long-poll consumer (#117529).
   */
  remoteGatewayActive: () => boolean

  emitUpdateProgress: (payload: { stage: string; message: string; percent: number | null }) => void
  rememberLog: (chunk: unknown) => void
  startHermes: () => Promise<unknown>
  preflightStateDb: (hermesHome: string, rememberLog: (chunk: string) => void) => void | Promise<void>
  runningAppBundle: () => string | null
  markQuittingForHandoff: () => void
  quit: () => void
  /** The checkout script's marker helper (A7 rule 3); injectable for tests. */
  markerHelper?: typeof runMarkerHelper
}

/**
 * How one script hand-off is correlated (SPEC 4a/4b). Protocol 2 scripts adopt
 * the Desktop's bridge carrying `runId`; legacy scripts (no protocol line)
 * claim for themselves and echo `startedAt` on marker line 2.
 */
export interface HandoffPlan {
  protocol: number
  runId: string | null
  startedAt: number
}

const STILL_RUNNING_VERDICTS = new Set(['held', 'busy', 'live'])

/**
 * Withdraw answers after which no script can adopt this run any more: the
 * bridge was removed (`withdrawn`), is gone (`absent`), or is not this
 * Desktop's bridge for the run (`foreign`). `busy` / `error` / `unsupported`
 * leave the bridge in place.
 */
const WITHDRAW_SETTLED_VERDICTS = new Set(['withdrawn', 'absent', 'foreign'])

/**
 * Withdraw attempts while the sidecar lock is `busy` (usually brief). Any
 * other unsettled answer stands at once: an `error` re-asked would cost up to
 * MARKER_HELPER_TIMEOUT_MS per try with the user waiting.
 */
const WITHDRAW_ATTEMPTS = 3
const WITHDRAW_RETRY_MS = 250

/**
 * The manual command card for a checkout with no staged updater: the exact
 * `hermes update` line to run, branch-pinned to the checkout's current branch
 * for non-main (bare `hermes update` would silently switch the install
 * off-branch).
 */
export function buildManualUpdateCommand(currentBranch: string | null | undefined): string {
  return currentBranch && currentBranch !== 'HEAD' && currentBranch !== 'main'
    ? `hermes update --branch ${currentBranch}`
    : 'hermes update'
}

/**
 * The commit this checkout was installed at, from the install stamp the
 * updater already trusts (written by the CLI's completion tail next to the
 * checkout it attests). Read-only display data for `currentSha`; never a
 * git spawn and never an update input. Null when absent or malformed.
 */
export function readStampedCommit(root: string): string | null {
  try {
    const stamp = JSON.parse(readFileSync(path.join(root, 'install-stamp.json'), 'utf8')) as { commit?: unknown }

    return typeof stamp.commit === 'string' && stamp.commit ? stamp.commit : null
  } catch {
    return null
  }
}

/**
 * The checkout strategy: posix-handoff.
 * The bodies are the production update flow; the mechanism stamp rides on
 * every result the way the wire contract expects.
 */
export function createCheckoutStrategy(deps: CheckoutStrategyDeps): UpdaterStrategy {
  function markerHelper(op: MarkerHelperOp, runId: string | null): Promise<MarkerHelperVerdict> {
    return (deps.markerHelper ?? runMarkerHelper)(op, {
      updateRoot: deps.resolveUpdateRoot(),
      hermesHome: deps.hermesHome,
      desktopPid: process.pid,
      runId
    })
  }

  /** `withdraw`, re-asked only while the lock is `busy`; every other answer ends it at once. */
  async function withdrawBridge(runId: string): Promise<MarkerHelperVerdict> {
    let verdict: MarkerHelperVerdict = { kind: 'error' }

    for (let attempt = 0; attempt < WITHDRAW_ATTEMPTS; attempt++) {
      if (attempt > 0) {
        await new Promise(resolve => setTimeout(resolve, WITHDRAW_RETRY_MS))
      }

      verdict = await markerHelper('withdraw', runId)

      if (verdict.kind !== 'busy') {
        break
      }
    }

    return verdict
  }

  function refuse(message: string, error: string): { refusal: UpdaterApplyResultWire } {
    deps.rememberLog(`[updates] refusing hand-off: ${message}`)
    deps.emitUpdateProgress({ stage: 'error', message, percent: null })

    return { refusal: { ok: false, error, message } }
  }

  /**
   * Plan the hand-off for this script (SPEC 4). Protocol 2: exclusive-create
   * the bridge in this Desktop's own name with a fresh run id, routing an
   * existing dead marker (or a stale bridge of ours) through the script
   * helper, then retrying the create ONCE. Legacy scripts get no bridge at all
   * (they overwrite unconditionally and know no run id). Never deletes or
   * rewrites the marker itself (A7 rule 3).
   */
  async function planHandoff(
    scriptPath: string,
    updateStartedAt: number
  ): Promise<{ plan: HandoffPlan } | { refusal: UpdaterApplyResultWire }> {
    const protocol = readHandoffProtocol(scriptPath) ?? 1

    if (protocol < 2) {
      deps.rememberLog('[updates] hand-off script predates protocol 2; legacy acknowledgment (live owner + started_at)')

      return { plan: { protocol, runId: null, startedAt: updateStartedAt } }
    }

    const runId = makeHandoffRunId()

    for (let attempt = 0; attempt < 2; attempt++) {
      const bridge = await claimBridgeMarker(deps.hermesHome, { startedAt: updateStartedAt, runId })

      if (bridge.ok) {
        return { plan: { protocol, runId, startedAt: updateStartedAt } }
      }

      if (bridge.conflict) {
        return refuse(bridge.conflict.message, 'update-already-running')
      }

      if (attempt > 0) {
        break
      }

      if (bridge.existing) {
        const op: MarkerHelperOp = bridge.existing.state === 'ours' ? 'withdraw' : 'reclaim'
        const verdict = await markerHelper(op, op === 'withdraw' ? bridge.existing.run : null)
        deps.rememberLog(`[updates] ${bridge.existing.state} update marker: script helper ${op} => ${verdict.kind}`)

        if (STILL_RUNNING_VERDICTS.has(verdict.kind)) {
          return refuse(
            'Another update still holds this installation. Wait for it to finish, then try again.',
            'update-already-running'
          )
        }
      } else {
        deps.rememberLog(`[updates] could not write the bridge marker (${bridge.error}); retrying once`)
      }
    }

    return refuse(
      'The updater could not take the update lock, so nothing was changed. Hermes keeps running on the previous version — try the update again.',
      'updater-spawn-failed'
    )
  }

  /**
   * C2 + R5: the hand-off started only when a LIVE, correlated script process
   * holds the marker — not when a wrapper exited 0 (V7), and not when a pid
   * claimed and died. Returns an error message when it did not; the caller
   * stays up.
   */
  async function confirmScriptHandoff(child, plan: HandoffPlan, startedAtMs: number): Promise<string | null> {
    const outcome = await observeUpdaterHandoff(child, deps.updateHandoffDwellMs)
    let failure: string | null = outcome.ok ? null : describeUpdaterHandoffFailure(outcome)

    if (!failure) {
      const claim = await waitForHandoffClaim(deps.hermesHome, process.pid, {
        timeoutMs: Math.max(0, (deps.handoffClaimTimeoutMs ?? HANDOFF_CLAIM_TIMEOUT_MS) - (Date.now() - startedAtMs)),
        ...(plan.runId ? { runId: plan.runId } : { startedAt: plan.startedAt })
      })

      if (claim.taken) {
        deps.rememberLog(`[updates] hand-off script took the update marker (pid ${claim.pid})`)

        return null
      }

      failure =
        'The updater did not start, so nothing was changed. Hermes keeps running on the previous version — try the update again.\n\n' +
        `Details: the hand-off script never took the update lock within ${Math.round((deps.handoffClaimTimeoutMs ?? HANDOFF_CLAIM_TIMEOUT_MS) / 1000)} s.`
    }

    deps.rememberLog(`[updates] hand-off not viable, aborting quit: ${failure}`)

    // Withdraw the bridge through the script helper (under its lock): an
    // adopt-only script that starts late then finds nothing to adopt (A4).
    // `taken` means a live script adopted our run at the deadline — the
    // update IS running.
    if (plan.runId) {
      const verdict = await withdrawBridge(plan.runId)

      if (verdict.kind === 'taken') {
        deps.rememberLog(`[updates] hand-off script took the update marker at the deadline (pid ${verdict.pid})`)

        return null
      }

      // Not withdrawn means the bridge may still be there, and the daemon
      // (setsid, out of killHandoffTree's reach) adopts it whenever it starts.
      // Reporting "did not start" and restarting the backend would then run
      // `hermes update` beside it. Quit instead: the next boot's update gate
      // judges the bridge or the script's claim under the script's lock.
      if (!WITHDRAW_SETTLED_VERDICTS.has(verdict.kind)) {
        deps.rememberLog(
          `[updates] bridge withdraw stayed ${verdict.kind}: a late hand-off script can still adopt run ${plan.runId}, ` +
            'so this Desktop quits instead of restarting its backend beside it'
        )

        return null
      }

      deps.rememberLog(`[updates] bridge withdraw: ${verdict.kind}`)
    }

    // MINOR-3: and stop the script tree we spawned, so nothing the UI just
    // reported as "did not start" can go on to run.
    killHandoffTree(child)

    return failure
  }

  const mechanism: UpdaterMechanism = 'posix-handoff'

  async function check(opts: { force?: boolean } = {}): Promise<UpdaterStatusWire> {
    const root: string = deps.resolveUpdateRoot()

    // A checkout without the source probe predates source channels, so it can
    // only be on the git line: move it to main. Its update pulls the probe in.
    const status: UpdaterStatusWire = (await deps.readSourceUpdate(root, opts)) ?? {
      supported: true,
      updateAvailable: true,
      behind: null,
      branch: deps.defaultUpdateBranch,
      hermesRoot: root
    }

    status.mechanism = mechanism

    // Display data only (#122727): the statusbar and command palette read
    // currentSha, but a probe-less checkout never supplied it. The install
    // stamp names the checkout's commit without a git spawn; never let it
    // override the probe and never let it influence the update decision.
    if (!status.currentSha) {
      const stamped: string | null = readStampedCommit(root)

      if (stamped) {
        status.currentSha = stamped
      }
    }

    return status
  }

  async function apply(): Promise<UpdaterApplyResultWire> {
    const result: UpdaterApplyResultWire = await applyBody()
    result.mechanism = mechanism

    return result
  }

  return { mechanism, check, apply }

  async function applyBody(): Promise<UpdaterApplyResultWire> {
    const status: UpdaterStatusWire = await check({ force: true })

    if (!status.supported || status.error) {
      return { ok: false, error: status.error ?? status.reason, message: status.message }
    }

    const branch: string = status.branch ?? deps.defaultUpdateBranch
    const targetArgs: string[] = status.channel ? ['--channel', status.channel] : ['--branch', branch]
    const targetLabel: string = status.channel ?? branch

    const manualCommand: string = status.channel
      ? `hermes update --channel ${status.channel}`
      : buildManualUpdateCommand(branch)

    const root: string = deps.resolveUpdateRoot()

    // Earlier PM scripts still demand checkout/venv. Do not invoke that known
    // incompatible handoff: one exact-install CLI update obtains the new scripts.
    if (existsSync(path.join(root, 'pm')) && !existsSync(path.join(root, 'scripts', 'desktop-update', 'posix.sh'))) {
      const launcher: string | null = resolveInstallationLauncher(root, deps.hermesHome)

      if (!launcher) {
        return { ok: false, error: 'installation-launcher-missing' }
      }

      const quote = (value: string): string => `'${value.replace(/'/g, "'\\''")}'`

      const command: string = `${quote(launcher)} update ${targetArgs.map(quote).join(' ')}`

      return { ok: true, manual: true, command, hermesRoot: root }
    }

    // macOS/Linux: hand off to the repo-owned posix script (quit → detached
    // orchestrator → `hermes update` → relaunch). The script owns
    // swap/relaunch, and the app is DEAD during the update so there is
    // nothing to reap around. Checkouts that predate the script get the
    // manual `hermes update` card once; their next update pulls it.
    return await applyPosixHandoff(targetArgs, targetLabel, manualCommand)
  }

  async function applyPosixHandoff(
    targetArgs: string[],
    targetLabel: string,
    manualCommand: string
  ): Promise<UpdaterApplyResultWire> {
    const updateRoot = deps.resolveUpdateRoot()
    const handoff = resolvePosixScriptHandoff(updateRoot)

    if (!handoff) {
      deps.emitUpdateProgress({ stage: 'manual', message: manualCommand, percent: null })

      return { ok: true, manual: true, command: manualCommand, hermesRoot: updateRoot }
    }

    const handoffConflict = await updateHandoffConflict(deps.hermesHome)

    if (handoffConflict) {
      // A live foreign updater (#75778)
      // already owns the marker — refuse rather than double-mutate the tree.
      deps.rememberLog(`[updates] refusing posix hand-off: ${handoffConflict.message}`)
      deps.emitUpdateProgress({ stage: 'error', message: handoffConflict.message, percent: null })

      return { ok: false, error: 'update-already-running', message: handoffConflict.message }
    }

    // ── Pre-flight state.db integrity guard (#68474) ──
    await deps.preflightStateDb(deps.hermesHome, deps.rememberLog)

    const args: string[] = [
      ...handoff.args,
      '--install-root',
      updateRoot,
      ...targetArgs,
      '--desktop-pid',
      String(process.pid)
    ]

    // A remote-served Desktop owns no local messaging gateway: `hermes update
    // --gateway` would (re)start one here anyway, and with the same channel
    // credentials as the remote host it becomes a competing long-poll consumer
    // (#117529). Keep --gateway for the local-ownership default.
    if (deps.remoteGatewayActive()) {
      args.push('--no-gateway')
    }

    const handoffStartedAt = Date.now()
    const updateStartedAt = Math.floor(handoffStartedAt / 1000)
    const planned = await planHandoff(handoff.scriptPath, updateStartedAt)

    if ('refusal' in planned) {
      return planned.refusal
    }

    const { plan } = planned

    if (plan.runId) {
      args.push('--handoff-run', plan.runId)
    }

    // Relaunch target: the running .app bundle on mac (script swaps the
    // rebuilt bundle over it), the running binary elsewhere. The script's gate
    // (an exact port of update-relaunch.ts's decideRelaunchOutcome) relaunches
    // only a binary the rebuild replaced with a launchable sandbox helper —
    // replaying the original launch context (filtered args, cwd, sandbox
    // opt-out) so a deep-link or --no-sandbox launch survives the update.
    const targetApp = deps.isMac ? deps.runningAppBundle() : process.execPath

    if (targetApp) {
      args.push('--relaunch-target', targetApp)
    }

    const relaunchArgs = collectRelaunchArgs(process.argv.slice(1))

    if (!deps.isMac) {
      args.push('--relaunch-cwd', process.cwd())

      if (sandboxFallbackFromEnv(process.env, relaunchArgs)) {
        args.push('--sandbox-fallback')
      }

      if (relaunchArgs.length) {
        args.push('--', ...relaunchArgs)
      }
    }

    const child = spawnUpdaterProcess(handoff.command, args, {
      cwd: deps.hermesHome,
      env: {
        ...sourceUpdateEnvironment(updateRoot, deps.hermesHome),
        HERMES_UPDATE_STARTED_AT: String(updateStartedAt)
      },
      detached: true,
      stdio: 'ignore'
    })

    deps.rememberLog(`[updates] launched posix hand-off: ${handoff.scriptPath} (${targetLabel}); quitting to hand off`)
    deps.emitUpdateProgress({
      stage: 'restart',
      message:
        'Updating Hermes — this window will close. Don’t reopen Hermes yourself; it restarts automatically when the update finishes.',
      percent: 100
    })

    // Settle window (#66753): the reported macOS failure mode is exactly this
    // path — the app quits, bash/posix.sh dies early (or was never spawnable),
    // and the user is left with no app, no updater, and no relaunch. Watch the
    // child through the dwell; on spawn error or early death, stay alive and
    // surface the failure instead of quitting into nothing.
    // The launcher always exits 0 after daemonizing, so only the daemon taking
    // the bridge marker proves an update is running (C2, V7).
    const dwellStartedAt = Date.now()
    const failure = await confirmScriptHandoff(child, plan, handoffStartedAt)

    if (failure) {
      deps.emitUpdateProgress({ stage: 'error', message: failure, percent: null })

      return { ok: false, error: 'updater-spawn-failed', message: failure }
    }

    deps.markQuittingForHandoff()
    setTimeout(
      () => {
        deps.quit()
      },
      Math.max(0, deps.updateHandoffDwellMs - (Date.now() - dwellStartedAt))
    )

    return { ok: true, handedOff: true, updater: handoff.scriptPath }
  }
}
