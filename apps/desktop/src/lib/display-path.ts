/**
 * One place to format filesystem paths for DISPLAY.
 *
 * Industry standard (shells, VS Code `tildify`, Finder path bar): collapse the
 * user's home directory to `~`, keep forward slashes, leave everything else
 * alone. Copy/reveal/IPC still use the real absolute path — this is paint only.
 *
 * When `home` is unknown (renderer has no `os.homedir()` and remote cwd may
 * not match the local machine), a conservative heuristic still collapses the
 * common `/Users/<name>` and `/home/<name>` prefixes so a
 * long absolute path never paints raw in chrome.
 */

export interface DisplayPathOptions {
  /** Explicit home directory to collapse (local machine home, remote $HOME). */
  home?: null | string
}

/** Collapse repeated slashes and drop a trailing slash (except root). */
export function normalizeDisplayPath(raw: string): string {
  let path = (raw || '').trim()

  if (!path) {
    return ''
  }

  path = path.replace(/\/{2,}/g, '/')

  // Drop trailing slash except bare `/`.
  if (path.length > 1 && path.endsWith('/')) {
    path = path.replace(/\/+$/, '')
  }

  return path
}

function normalizeHome(home: string): string {
  const normalized = normalizeDisplayPath(home)

  if (!normalized) {
    return ''
  }

  // Home itself should not keep a trailing slash for prefix checks.
  return normalized.replace(/\/+$/, '')
}

function startsWithHome(path: string, home: string, caseInsensitive: boolean): boolean {
  if (!home) {
    return false
  }

  if (path === home) {
    return true
  }

  const prefix = `${home}/`

  return caseInsensitive
    ? path.toLowerCase().startsWith(prefix.toLowerCase()) || path.toLowerCase() === home.toLowerCase()
    : path.startsWith(prefix) || path === home
}

/**
 * Best-effort home prefix when callers don't pass one. Matches the usual
 * single-user layouts; never collapses `/Users` or `/home` alone.
 */
function inferredHomePrefix(path: string): string {
  // macOS: /Users/name[/...]
  let match = path.match(/^(\/Users\/[^/]+)(?:\/|$)/)

  if (match) {
    return match[1]
  }

  // Linux (and most UNIX): /home/name[/...]
  match = path.match(/^(\/home\/[^/]+)(?:\/|$)/)

  if (match) {
    return match[1]
  }

  return ''
}

/**
 * Format a filesystem path for UI chrome.
 *
 *   /Users/brooklyn/www/hermes-agent  →  ~/www/hermes-agent
 *   /Users/brooklyn                   →  ~
 *   /var/log                          →  /var/log
 *   already/relative                  →  already/relative
 */
export function displayPath(raw: null | string | undefined, options: DisplayPathOptions = {}): string {
  const path = normalizeDisplayPath(raw || '')

  if (!path) {
    return ''
  }

  // Already tildified — normalize only.
  if (path === '~' || path.startsWith('~/')) {
    return path
  }

  const explicitHome = options.home ? normalizeHome(options.home) : ''
  // POSIX paths with an explicit home keep case-sensitive matching (Linux).
  // Heuristic homes and macOS /Users homes ignore case.
  const home = explicitHome || inferredHomePrefix(path)

  if (!home) {
    return path
  }

  const caseInsensitive = !explicitHome || home.startsWith('/Users/')

  if (!startsWithHome(path, home, caseInsensitive)) {
    return path
  }

  if (path.length === home.length) {
    return '~'
  }

  return `~${path.slice(home.length)}`
}

/** Last path segment for compact labels (statusbar leaf, settings rows). */
export function pathLeaf(raw: null | string | undefined): string {
  const path = normalizeDisplayPath(raw || '')

  if (!path || path === '/' || path === '~') {
    return path
  }

  const leaf = path.split('/').filter(Boolean).pop()

  return leaf || path
}
