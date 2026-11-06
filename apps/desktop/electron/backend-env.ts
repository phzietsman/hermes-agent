import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

import { resolveDesktopHermesHome } from './data-paths'

// macOS apps launched from Finder/Dock inherit only /usr/bin:/bin:/usr/sbin:/sbin,
// which misses Homebrew and user-installed CLI tools (codex, git credential
// helpers). Hermes' own managed tools need no PATH help — the backend composes
// their environment in-process via pm — but user tools on PATH do.
const POSIX_SANE_PATH_ENTRIES = Object.freeze([
  '/opt/homebrew/bin',
  '/opt/homebrew/sbin',
  '/usr/local/sbin',
  '/usr/local/bin',
  '/usr/sbin',
  '/usr/bin',
  '/sbin',
  '/bin'
])

const PATH_DELIMITER = ':'
const PATH_ENV_KEY = 'PATH'

function appendUniquePathEntries(entries, { delimiter = path.delimiter } = {}) {
  const seen = new Set()
  const ordered = []

  for (const entry of entries) {
    if (!entry) {
      continue
    }

    const parts = Array.isArray(entry) ? entry : String(entry).split(delimiter)

    for (const part of parts) {
      if (!part || seen.has(part)) {
        continue
      }

      seen.add(part)
      ordered.push(part)
    }
  }

  return ordered.join(delimiter)
}

function resolveHermesHomePath(hermesHome, { pathModule, homedir = os.homedir() }: any) {
  // fish (and any shell when the value is quoted) hands a literal `~` through; path.resolve()
  // would pin it under cwd and the Python backend inherits that absolute path via HERMES_HOME.
  let raw = String(hermesHome)

  if (raw === '~' || raw.startsWith('~/')) {
    raw = pathModule.join(homedir, raw.slice(1))
  }

  return pathModule.resolve(raw)
}

function isProfileHome(resolved, pathModule) {
  return pathModule.basename(pathModule.dirname(resolved)).toLowerCase() === 'profiles'
}

function normalizeHermesHomeRoot(hermesHome, { pathModule = path.posix, homedir = os.homedir() }: any = {}) {
  if (!hermesHome) {
    return hermesHome
  }

  const resolved = resolveHermesHomePath(hermesHome, { pathModule, homedir })

  return isProfileHome(resolved, pathModule) ? pathModule.dirname(pathModule.dirname(resolved)) : resolved
}

// OS/interpreter names a dotenv may redeclare that no child can run without.
const PROCESS_ENV_NAMES = new Set([
  'HERMES_HOME',
  'HOME',
  'LANG',
  'LC_ALL',
  'PATH',
  'PWD',
  'PYTHONPATH',
  'SHELL',
  'SSL_CERT_FILE',
  'TEMP',
  'TMP',
  'TMPDIR',
  'TZ',
  'USER',
  'VIRTUAL_ENV'
])

function dotenvKeyNames(contents = '') {
  return String(contents)
    .replace(/^\uFEFF/, '')
    .split(/\r?\n/)
    .flatMap(line => line.match(/^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=/)?.slice(1, 2) ?? [])
}

function readTextOrEmpty(fsModule, file) {
  try {
    return String(fsModule.readFileSync(file, 'utf8'))
  } catch {
    return ''
  }
}

/**
 * Parent env for a local `hermes serve` child of `profile` (#68367).
 *
 * `hermes desktop` loads its launch profile's `.env`/`.op.env` into os.environ
 * before exec'ing Electron, so `process.env` carries that profile's platform
 * credentials. A child for ANOTHER profile would inherit them ahead of its own
 * dotenv (`.op.env` is even skipped once OP_SERVICE_ACCOUNT_TOKEN is set) and,
 * e.g., connect the same Tlon ship as the default gateway. Drop every name the
 * launch profile's dotenv declares, as `_profile_action_environment` does for
 * dashboard actions; the child reloads its own scope. The launch profile's own
 * backend keeps the env unchanged, and shell exports the launch dotenv never
 * declared pass through everywhere.
 *
 * `profile` null/empty means no `--profile` flag: the child follows the sticky
 * `active_profile` like a bare `hermes serve` (`_apply_profile_override`).
 */
function profileBackendParentEnv({
  hermesHome,
  profile,
  currentEnv = process.env,
  fsModule = fs,
  pathModule = path.posix
}: any = {}) {
  const env = { ...(currentEnv || {}) }

  if (!hermesHome) {
    return env
  }

  const inheritedHome = currentEnv?.HERMES_HOME ? resolveHermesHomePath(currentEnv.HERMES_HOME, { pathModule }) : null
  const launchHome = inheritedHome && isProfileHome(inheritedHome, pathModule) ? inheritedHome : hermesHome
  const name = profile || readTextOrEmpty(fsModule, pathModule.join(hermesHome, 'active_profile')).trim()
  const targetHome = !name || name === 'default' ? hermesHome : pathModule.join(hermesHome, 'profiles', name)

  if (pathModule.resolve(launchHome) === pathModule.resolve(targetHome)) {
    return env
  }

  const launchOwned = new Set(
    ['.env', '.op.env']
      .flatMap(file => dotenvKeyNames(readTextOrEmpty(fsModule, pathModule.join(launchHome, file))))
      .filter(key => !PROCESS_ENV_NAMES.has(key.toUpperCase()))
  )

  for (const key of Object.keys(env)) {
    if (launchOwned.has(key)) {
      delete env[key]
    }
  }

  return env
}

/**
 * PATH with the entries under the PM store (HERMES_RUNTIME_DIR, else
 * <hermes home>/tools, as pm.environments.store_root resolves it) moved to the
 * front, every other entry kept in order. Hermes's own children must run the
 * store's uv/node/npm, but shell-path.ts puts the user's login-shell entries
 * (nvm, Homebrew, ~/.local/bin) ahead of the inherited PATH, which is where
 * `hermes desktop` put the store dirs.
 */
function storeFirstPath(pathValue: string, { currentEnv = process.env, homedir = os.homedir() }: any = {}) {
  const pathModule = path.posix
  const delimiter = PATH_DELIMITER
  const hermesHome = resolveDesktopHermesHome({ home: homedir, env: currentEnv })
  const roots = [currentEnv?.HERMES_RUNTIME_DIR, pathModule.join(hermesHome, 'tools')].filter(Boolean)

  const owned = (entry: string) =>
    roots.some(root => {
      const relative = pathModule.relative(pathModule.resolve(root), pathModule.resolve(entry))

      return !relative.startsWith('..') && !pathModule.isAbsolute(relative)
    })

  const entries = String(pathValue || '').split(delimiter)

  return appendUniquePathEntries([entries.filter(entry => entry && owned(entry)), entries], { delimiter })
}

/**
 * The environment for the spawned Python backend. Electron knows ONE thing:
 * where the interpreter is (by convention). Everything else — managed tool
 * PATHs, browser paths, node — is composed in-process by pm when the backend
 * spawns tools. PYTHONPATH/PYTHONHOME are scrubbed so an inherited value
 * can't make the backend import modules from another checkout. Store dirs
 * already on the inherited PATH stay first (storeFirstPath).
 */
function buildDesktopBackendEnv({ currentEnv = process.env, homedir = os.homedir() }: any = {}) {
  const delimiter = PATH_DELIMITER
  const key = PATH_ENV_KEY
  const saneEntries = POSIX_SANE_PATH_ENTRIES
  const inherited = storeFirstPath(currentEnv?.[key] || '', { currentEnv, homedir })

  return {
    PYTHONPATH: '',
    PYTHONHOME: '',
    // Force PEP 540 UTF-8 mode in the spawned Python backend so its stdio and
    // subprocess defaults are UTF-8 even on non-UTF-8 locales. hermes_bootstrap sets this inside the child too, but only
    // after import — anything emitted earlier (interpreter startup errors,
    // pre-bootstrap tracebacks) still decodes with the locale default without
    // this. User's explicit setting wins. Re-port of PR #56499 (echoriver89).
    PYTHONUTF8: currentEnv?.PYTHONUTF8 ?? '1',
    [key]: appendUniquePathEntries([inherited, saneEntries], { delimiter })
  }
}

/**
 * Spawn env for a POOLED per-profile backend (`spawnPoolBackend`).
 *
 * TERMINAL_CWD is the LAUNCH profile's resolved workspace. A pooled child
 * serves ANOTHER profile (`--profile X`): stamping the app-global cwd makes
 * that profile's placeholder/unset `terminal.cwd` sessions inherit the launch
 * (or another) profile's workspace (#87584). Drop TERMINAL_CWD from the
 * inherited env AND from any runtime-provided mapping; the `--profile` child re-resolves its own cwd from its profile
 * config, the same way a standalone `hermes -p X serve` would. The launch
 * profile's own (primary) backend keeps the pin in main.ts.
 */
function pooledProfileBackendEnv({
  hermesHome,
  profile,
  currentEnv = process.env,
  backendEnv = {},
  fsModule = fs,
  pathModule = path.posix
}: any = {}) {
  const parent = profileBackendParentEnv({ hermesHome, profile, currentEnv, fsModule, pathModule })
  const isTerminalCwd = (key: string) => key === 'TERMINAL_CWD'

  const env = { ...parent }

  for (const key of Object.keys(env)) {
    if (isTerminalCwd(key)) {
      delete env[key]
    }
  }

  // The resolved root, not process.env's: the --profile child resolves its home under it.
  env.HERMES_HOME = hermesHome

  for (const [key, value] of Object.entries(backendEnv || {})) {
    if (isTerminalCwd(key)) {
      continue
    }

    env[key] = value
  }

  return env
}

export {
  appendUniquePathEntries,
  buildDesktopBackendEnv,
  normalizeHermesHomeRoot,
  PATH_DELIMITER,
  PATH_ENV_KEY,
  pooledProfileBackendEnv,
  POSIX_SANE_PATH_ENTRIES,
  profileBackendParentEnv,
  storeFirstPath
}
