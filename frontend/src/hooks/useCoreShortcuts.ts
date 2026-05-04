import { onCleanup } from 'solid-js'
import { isTauriApp, openWebInspector, quitApp, resetWebviewZoom, zoomInWebview, zoomOutWebview } from '~/api/platformBridge'
import { registerCommand } from '~/lib/shortcuts/commands'
import { setContext } from '~/lib/shortcuts/context'
import { CORE_KEYBINDINGS } from '~/lib/shortcuts/defaults'
import { activateBindings, unbindAll } from '~/lib/shortcuts/keybindings'
import { getPlatform } from '~/lib/shortcuts/platform'
import { syncMacMenuAccelerator } from '~/lib/shortcuts/tauriAccelerator'

// FFI contract: must match OPEN_WEB_INSPECTOR_MENU_ID in desktop/rust/src/main.rs.
const OPEN_WEB_INSPECTOR_MENU_ID = 'open-web-inspector'

/**
 * Mounted at the App root so quit / web inspector / webview zoom work on
 * every route — including launcher and auth/setup routes that don't mount
 * AppShell. Owns the 'core' binding slot, disjoint from the 'workspace'
 * slot bound by useShortcuts.
 */
export function useCoreShortcuts(): void {
  const cleanups: (() => void)[] = []
  const cmd = (id: string, title: string, handler: () => void | Promise<void>) => {
    cleanups.push(registerCommand({ id, title, handler, category: 'App' }))
  }
  cmd('app.quit', 'Quit Application', () => quitApp())
  cmd('app.openWebInspector', 'Open Web Inspector', () => openWebInspector())
  cmd('app.zoomOutWebview', 'Zoom Out', () => zoomOutWebview())
  cmd('app.zoomInWebview', 'Zoom In', () => zoomInWebview())
  cmd('app.resetWebviewZoom', 'Actual Size', () => resetWebviewZoom())

  // Owned here (not in useShortcuts) because this hook always mounts first
  // and is needed for CORE_KEYBINDINGS' isDesktop when-clauses to evaluate.
  setContext('isDesktop', isTauriApp())
  setContext('platform', getPlatform())

  activateBindings(CORE_KEYBINDINGS, 'core')
  syncMacMenuAccelerator(OPEN_WEB_INSPECTOR_MENU_ID, 'app.openWebInspector', CORE_KEYBINDINGS)

  onCleanup(() => {
    unbindAll('core')
    for (const cleanup of cleanups)
      cleanup()
  })
}
