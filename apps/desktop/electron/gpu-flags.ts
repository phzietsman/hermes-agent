/**
 * `--disable-gpu` argv/env helpers shared by the Linux GPU recovery ladder
 * (see linux-gpu-fallback.ts). HERMES_DESKTOP_DISABLE_GPU forces GPU off (on
 * values) or keeps it on (off values).
 */

const GPU_OVERRIDE_ON = new Set(['1', 'true', 'yes', 'on'])
const GPU_OVERRIDE_OFF = new Set(['0', 'false', 'no', 'off'])
const DISABLE_GPU_SWITCH = '--disable-gpu'
const DISABLE_GPU_COMPOSITING_SWITCH = '--disable-gpu-compositing'

export function isHermesDesktopGpuOverrideOff(env: NodeJS.ProcessEnv = process.env): boolean {
  const override = String(env.HERMES_DESKTOP_DISABLE_GPU || '')
    .trim()
    .toLowerCase()

  return GPU_OVERRIDE_OFF.has(override)
}

/**
 * True when this process already launched with GPU off — Chromium argv
 * (`--disable-gpu`) or HERMES_DESKTOP_DISABLE_GPU on. Mirrors
 * `alreadyHasNoSandbox`: the crash-loop relaunch MUST pass these switches so
 * the next process is protected even if the sticky marker write failed.
 */
export function alreadyHasDisableGpu(argv: readonly string[] = [], env: NodeJS.ProcessEnv = process.env): boolean {
  if (Array.isArray(argv) && argv.some(arg => arg === DISABLE_GPU_SWITCH)) {
    return true
  }

  const override = String(env.HERMES_DESKTOP_DISABLE_GPU || '')
    .trim()
    .toLowerCase()

  return GPU_OVERRIDE_ON.has(override)
}

/** Relaunch argv with a single `--disable-gpu` + compositing switch. */
export function buildDisableGpuRelaunchArgs(argv: readonly string[]): string[] {
  const args = (Array.isArray(argv) ? argv : []).filter(
    arg => arg !== DISABLE_GPU_SWITCH && arg !== DISABLE_GPU_COMPOSITING_SWITCH
  )

  args.push(DISABLE_GPU_SWITCH, DISABLE_GPU_COMPOSITING_SWITCH)

  return args
}
