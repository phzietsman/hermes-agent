/** Comparing two paths for identity or containment.
 *
 *  A cwd reaches us from the backend, a picker, a session row and a project
 *  record, and the four don't agree on trailing slashes or doubled separators.
 *  Compare through these rather than `===` / `startsWith`.
 */

/** One spelling: no surrounding whitespace, no trailing slash. */
export const cleanPath = (path: string): string => path.trim().replace(/\/+$/, '') || '/'

/** True when `child` IS `parent` or lives underneath it. */
export const isUnderPath = (parent: string, child: string): boolean => {
  const p = cleanPath(parent)
  const c = cleanPath(child)

  return c === p || c.startsWith(`${p === '/' ? '' : p}/`)
}
