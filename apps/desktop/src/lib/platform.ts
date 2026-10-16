/**
 * Platform detection for the renderer.
 *
 * The renderer has no `process.platform`, and several surfaces need to know
 * which OS they're on — keybind glyphs, terminal shortcuts, and glass. One
 * definition so they can't disagree.
 *
 * The real glass gate is `hermesDesktop.glassSupported`, which main/preload
 * compute.
 */

export const isMacPlatform = (): boolean =>
  typeof navigator !== 'undefined' && /mac/i.test(navigator.platform || navigator.userAgent || '')

export const isLinuxPlatform = (): boolean =>
  typeof navigator !== 'undefined' && /linux/i.test(navigator.platform || navigator.userAgent || '')
