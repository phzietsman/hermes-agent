import fs from 'node:fs';
import path from 'node:path';

/**
 * Resolve the installation-owned launcher used to finish a source update.
 *
 * @param {string} root
 * @returns {{launcher: string, command: string, args: string[]}}
 */
export function sourceRuntimeSettleCommand(root) {
  const candidates = [path.join(root, '.hermes', 'bin', 'hermes'), path.join(root, 'venv', 'bin', 'hermes')];
  const launcher = candidates.find(candidate => fs.existsSync(candidate));
  if (!launcher) throw new Error(`No source launcher available to settle ${root}`);
  return { launcher, command: launcher, args: ['status'] };
}
