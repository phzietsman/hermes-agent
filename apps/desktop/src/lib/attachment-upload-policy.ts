/** When a composer attachment must cross as BYTES rather than ride its path.
 *
 * Shared by the submit-time upload pipeline (`uploadComposerAttachment`) and
 * the drop-time routing decision (`partitionDroppedFiles`) so the two never
 * drift apart. Pure: takes the backend facts as arguments.
 */

// Terminal backends whose execution environment has its own filesystem
// (docker/ssh/singularity/modal/...) cannot see the desktop's host paths —
// they must be crossed as bytes, like remote attachments. Mirrors the
// container_backend set in tools/terminal_tool.py::_get_env_config.
export const CONTAINER_TERMINAL_BACKENDS = new Set([
  'docker',
  'ssh',
  'singularity',
  'modal',
  'daytona',
  'vercel_sandbox'
])

// `mode: local` means the gateway was launched locally, not necessarily that
// Electron and the gateway share a filesystem. Container terminal backends
// (docker, ssh, ...) always need bytes: the sandbox has its own filesystem and
// the host path would dangle inside it (#76577).
export function attachmentPathNeedsUpload(terminalBackend?: null | string): boolean {
  return CONTAINER_TERMINAL_BACKENDS.has((terminalBackend || '').trim().toLowerCase())
}
