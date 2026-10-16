/**
 * Terminal clearing with scrollback support (ESC[3J).
 */

import { CURSOR_HOME, ERASE_SCREEN, ERASE_SCROLLBACK } from './termio/csi.js'

/**
 * Returns the ANSI escape sequence to clear the terminal including scrollback.
 */
export function getClearTerminalSequence(): string {
  return ERASE_SCREEN + ERASE_SCROLLBACK + CURSOR_HOME
}

/**
 * Clears the terminal screen, including scrollback.
 */
export const clearTerminal = getClearTerminalSequence()
