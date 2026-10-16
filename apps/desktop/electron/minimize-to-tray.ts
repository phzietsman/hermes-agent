import fs from 'node:fs'
import path from 'node:path'

import { app, BrowserWindow, ipcMain, Menu, nativeImage, Tray } from 'electron'

export interface MinimizeToTrayStatus {
  enabled: boolean
  available: boolean
}

interface Options {
  preferencesPath: string
  getIconPath: () => string | undefined
  restoreMainWindow: () => void
  isQuittingForHandoff: () => boolean
  log: (message: string) => void
}

/** Device-local native preference; renderer windows only cache its status. */
export function createMinimizeToTray(options: Options) {
  let enabled = false
  let quitting = false
  let tray: Tray | null = null
  let stopWatchingHost: (() => void) | undefined
  let dockHidden = false
  let hostGeneration = 0
  let pending = Promise.resolve()
  const windows = new Set<BrowserWindow>()
  const hidden = new Set<BrowserWindow>()

  const status = (): MinimizeToTrayStatus => ({ enabled, available: !!tray && !tray.isDestroyed() })

  const broadcast = () => {
    for (const win of BrowserWindow.getAllWindows()) {
      if (!win.isDestroyed()) {
        win.webContents.send('hermes:minimize-to-tray:changed', status())
      }
    }
  }

  const showDock = () => {
    if (dockHidden) {
      dockHidden = false
      void app.dock?.show()
    }
  }

  const syncDock = () => {
    if (process.platform !== 'darwin') {
      return
    }

    // A hidden primary must not remove a visible peer from Cmd-Tab/the Dock.
    const foreground = [...windows].some(win => !win.isDestroyed() && win.isVisible() && !win.isMinimized())

    if (status().available && hidden.size > 0 && !foreground && !quitting) {
      dockHidden = true
      app.dock?.hide()
    } else {
      showDock()
    }
  }

  const released = (win: BrowserWindow): boolean => {
    if (!hidden.delete(win)) {
      return false
    }

    showDock()

    return true
  }

  const restoreHidden = () => {
    showDock()

    for (const win of [...hidden]) {
      if (win.isDestroyed()) {
        hidden.delete(win)

        continue
      }

      released(win)

      if (win.isMinimized()) {
        win.restore()
      }

      win.showInactive()
    }
  }

  const restore = () => {
    restoreHidden()
    options.restoreMainWindow()
  }

  const destroyTray = (force = false) => {
    stopWatchingHost?.()
    stopWatchingHost = undefined

    // Linux StatusNotifierItem stays exported after Tray.destroy(), so a later
    // `new Tray()` in this process cannot re-export and the panel icon dies
    // (#126353). Park the instance until the app actually quits or the host
    // disappears.
    if (process.platform === 'linux' && !quitting && !force) {
      return
    }

    tray?.destroy()
    tray = null
  }

  const hostLost = () => {
    // Losing the shell/tray must never strand an invisible app.
    hostGeneration += 1
    restoreHidden()
    destroyTray(true)
    broadcast()
  }

  const apply = async (on: boolean) => {
    enabled = on

    if (!on) {
      restoreHidden()
      destroyTray()
    } else if (!quitting && status().available && process.platform === 'linux' && !stopWatchingHost) {
      try {
        const { watchLinuxTrayHost } = await import('./tray-host')
        const generation = hostGeneration
        stopWatchingHost = await watchLinuxTrayHost(hostLost)

        if (generation !== hostGeneration) {
          throw new Error('System tray host disappeared')
        }
      } catch (error) {
        restoreHidden()
        destroyTray(true)
        options.log(`[tray] unavailable; ordinary window behavior retained: ${error}`)
      }
    } else if (!status().available && !quitting) {
      try {
        if (process.platform === 'linux') {
          const { watchLinuxTrayHost } = await import('./tray-host')
          const generation = hostGeneration
          stopWatchingHost = await watchLinuxTrayHost(hostLost)

          if (generation !== hostGeneration) {
            throw new Error('System tray host disappeared')
          }
        }

        if (quitting) {
          destroyTray()

          return status()
        }

        const iconPath = options.getIconPath()
        const icon = iconPath ? nativeImage.createFromPath(iconPath) : nativeImage.createEmpty()

        if (icon.isEmpty()) {
          throw new Error('No usable tray icon')
        }

        tray = new Tray(
          icon.resize({
            width: process.platform === 'darwin' ? 18 : 24,
            height: process.platform === 'darwin' ? 18 : 24
          })
        )
        tray.setToolTip('Hermes')
        tray.setContextMenu(
          Menu.buildFromTemplate([
            { label: 'Show Hermes', click: restore },
            { type: 'separator' },
            // Do not bypass the ordinary active-work confirmation or teardown.
            { label: 'Quit Hermes', click: () => app.quit() }
          ])
        )

        // macOS single-click opens the native menu, not the window behind it.
        if (process.platform !== 'darwin') {
          tray.on('click', restore)
        }

        tray.on('double-click', restore)
      } catch (error) {
        restoreHidden()
        destroyTray()
        options.log(`[tray] unavailable; ordinary window behavior retained: ${error}`)
      }
    }

    broadcast()

    return status()
  }

  function registerWindow(win: BrowserWindow, { closeToTray = false } = {}) {
    windows.add(win)

    const hide = () => {
      if (win.isDestroyed()) {
        return false
      }

      if (!enabled || !status().available || quitting || options.isQuittingForHandoff()) {
        return false
      }

      hidden.add(win)

      win.hide()
      syncDock()

      return true
    }

    // Hide past the native minimize dispatch, not inside it: hiding
    // synchronously here re-enters window-state changes mid-flight.
    // Guards are re-evaluated at fire time inside hide(); the close handler
    // below keeps its synchronous hide so preventDefault still works.
    win.on('minimize', () => {
      setImmediate(() => {
        // The user may have restored the window in the meantime (taskbar or
        // shortcut); a stale hide must not snatch it back.
        if (!win.isDestroyed() && !win.isMinimized() && win.isVisible()) {
          return
        }

        hide()
      })
    })

    if (closeToTray) {
      win.on('close', event => {
        if (hide()) {
          event.preventDefault()
        }
      })
    }

    const release = () => {
      released(win)
      syncDock()
    }

    win.on('show', release)
    win.on('restore', release)
    win.on('closed', () => {
      windows.delete(win)
      hidden.delete(win)
      syncDock()
    })
  }

  async function start() {
    let on = false

    try {
      on = JSON.parse(fs.readFileSync(options.preferencesPath, 'utf8')).enabled === true
    } catch {
      // Missing or malformed preference preserves ordinary minimize/close.
    }

    const operation = apply(on)
    pending = operation.then(
      () => undefined,
      () => undefined
    )

    return operation
  }

  function setEnabled(on: boolean): Promise<MinimizeToTrayStatus> {
    // Serialize writes from peer windows so an older native apply cannot win.
    const operation = pending.then(async () => {
      fs.mkdirSync(path.dirname(options.preferencesPath), { recursive: true })
      fs.writeFileSync(`${options.preferencesPath}.tmp`, JSON.stringify({ enabled: on === true }), 'utf8')
      fs.renameSync(`${options.preferencesPath}.tmp`, options.preferencesPath)

      return apply(on === true)
    })

    pending = operation.then(
      () => undefined,
      () => undefined
    )

    return operation
  }

  ipcMain.handle('hermes:minimize-to-tray:get', status)
  ipcMain.handle('hermes:minimize-to-tray:set', (_event, on) => setEnabled(on === true))
  app.on('will-quit', () => {
    quitting = true
    destroyTray()
  })

  return {
    start,
    status,
    setEnabled,
    registerWindow,
    restore,
    // Call only AFTER the active-work guard accepts the quit. Cancelling the
    // prompt must leave hiding and its recovery affordance intact.
    beginQuit: () => {
      quitting = true
    }
  }
}
