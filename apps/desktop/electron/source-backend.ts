import { existsSync } from 'node:fs'
import path from 'node:path'

import { buildDesktopBackendEnv } from './backend-env'
import { execProbe, PROBE_TIMEOUT_MS } from './backend-probes'
import { resolveInstallationLauncher } from './updater-process'

export interface SourceBackend {
  kind: 'command' | 'python'
  label: string
  command: string
  args: string[]
  env: NodeJS.ProcessEnv
  root: string
  bootstrap: false
  shell: boolean
  local: 'installed'
}

interface SourceOptions {
  env?: NodeJS.ProcessEnv
}

/** Keep the validated command. PM owns interpreter and generation selection. */
export async function resolveSourceInstallationBackend(
  root: string,
  args: string[],
  options: SourceOptions & { hermesHome?: string } = {}
): Promise<SourceBackend | null> {
  if (!existsSync(path.join(root, 'hermes_cli', 'main.py'))) {
    return null
  }

  const launcher: string | null = resolveInstallationLauncher(root, options.hermesHome)

  if (!launcher) {
    return null
  }

  const env: NodeJS.ProcessEnv = buildDesktopBackendEnv({ currentEnv: options.env ?? process.env })

  try {
    await execProbe(launcher, ['--version'], {
      cwd: root,
      env: { ...process.env, ...options.env, ...env },
      stdio: 'ignore',
      timeout: PROBE_TIMEOUT_MS
    })
  } catch {
    return null
  }

  return {
    kind: 'command',
    label: `Hermes at ${root}`,
    command: launcher,
    args: [...args],
    env,
    root,
    bootstrap: false,
    shell: false,
    local: 'installed'
  }
}

/** Developer overrides retain their interpreter, even outside the checkout. */
export function createSourcePythonBackend(
  root: string,
  python: string | null,
  args: string[],
  options: SourceOptions = {}
): SourceBackend | null {
  if (!python) {
    return null
  }

  return {
    kind: 'python',
    label: `Hermes source at ${root}`,
    command: python,
    args: ['-m', 'hermes_cli.main', ...args],
    // The backend runs in the user's workspace cwd, and the selected
    // interpreter need not have this checkout installed: name it explicitly.
    // (The scrubbed inherited value could point at another checkout.)
    env: { ...buildDesktopBackendEnv({ currentEnv: options.env ?? process.env }), PYTHONPATH: root },
    root,
    bootstrap: false,
    shell: false,
    local: 'installed'
  }
}
