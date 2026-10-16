/**
 * Desktop bundles ship precompiled renderer assets. Returning false here tells
 * electron-builder to skip the node_modules collector/install step, which
 * avoids workspace dependency graph explosions and keeps packaging
 * deterministic across environments.
 *
 * Payload assembly belongs to scripts/bundles. This hook never creates it.
 */
export default async function beforeBuild() {
  return false
}
