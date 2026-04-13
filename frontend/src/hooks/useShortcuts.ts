import type { Accessor } from 'solid-js'
import type { useTabOperations } from '~/components/shell/useTabOperations'
import type { UserKeybindingOverride } from '~/lib/shortcuts/types'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import { createEffect, onCleanup, onMount } from 'solid-js'
import { isTauriApp, quitApp } from '~/api/platformBridge'
import { setShowPreferencesDialog } from '~/components/shell/UserMenu'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { registerCommand, resetCommands } from '~/lib/shortcuts/commands'
import { registerLazyContext, setContext, unregisterLazyContext } from '~/lib/shortcuts/context'
import { DEFAULT_KEYBINDINGS } from '~/lib/shortcuts/defaults'
import { bindAll, mergeKeybindings, setActiveBindings, unbindAll } from '~/lib/shortcuts/keybindings'
import { getPlatform } from '~/lib/shortcuts/platform'

interface UseShortcutsProps {
  tabStore: ReturnType<typeof createTabStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  tabOps: ReturnType<typeof useTabOperations>

  // Dialog setters
  setShowNewAgentDialog: (v: boolean) => void
  setShowNewTerminalDialog: (v: boolean) => void
  setShowNewWorkspace: (v: boolean) => void

  // Sidebar toggle (desktop layout)
  toggleLeftSidebar: () => void

  // Active tab type (reactive)
  activeTabType: Accessor<TabType | null>

  // Custom keybinding overrides (from preferences)
  customKeybindings: Accessor<UserKeybindingOverride[]>
}

const TAB_TYPE_LABELS: Record<number, string> = {
  [TabType.AGENT]: 'agent',
  [TabType.TERMINAL]: 'terminal',
  [TabType.FILE]: 'file',
}

/**
 * Root keyboard shortcut hook. Call once in AppShell.
 *
 * Registers all commands, sets up context tracking, and binds keys via tinykeys.
 */
export function useShortcuts(props: UseShortcutsProps): void {
  const {
    tabStore,
    layoutStore,
    tabOps,
    setShowNewAgentDialog,
    setShowNewTerminalDialog,
    setShowNewWorkspace,
    toggleLeftSidebar,
    activeTabType,
    customKeybindings,
  } = props

  // -----------------------------------------------------------------------
  // Register commands
  // -----------------------------------------------------------------------

  const cleanups: (() => void)[] = []

  function cmd(id: string, title: string, handler: () => void | Promise<void>, category?: string) {
    cleanups.push(registerCommand({ id, title, handler, category }))
  }

  // App-level commands
  cmd('app.newAgent', 'New Agent', () => setShowNewAgentDialog(true), 'App')
  cmd('app.newTerminal', 'New Terminal', () => setShowNewTerminalDialog(true), 'App')
  cmd('app.newWorkspace', 'New Workspace', () => setShowNewWorkspace(true), 'App')
  cmd('app.closeActiveTab', 'Close Active Tab', () => {
    const tab = tabStore.activeTab()
    if (tab)
      tabOps.handleTabClose(tab)
  }, 'Tab')
  cmd('app.toggleSidebar', 'Toggle Sidebar', toggleLeftSidebar, 'Layout')
  cmd('app.splitTile', 'Split Tile', () => {
    const focusedId = layoutStore.focusedTileId()
    if (focusedId)
      layoutStore.splitTileHorizontal(focusedId)
  }, 'Layout')
  cmd('app.openPreferences', 'Open Preferences', () => {
    setShowPreferencesDialog(true)
  }, 'App')
  cmd('dialog.close', 'Close Dialog', () => {
    // Close the topmost open dialog
    const dialogs = [...document.querySelectorAll('dialog[open]')]
    const last = dialogs.at(-1) as HTMLDialogElement | undefined
    if (last) {
      // Dispatch Escape to let the dialog's own handler run
      last.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    }
  }, 'App')
  cmd('app.quit', 'Quit Application', () => quitApp(), 'App')

  // Tab switching by index
  for (let i = 1; i <= 9; i++) {
    cmd(`app.switchToTab${i}`, `Switch to Tab ${i}`, () => {
      const focusedTile = layoutStore.focusedTileId()
      const tabs = focusedTile ? tabStore.getTabsForTile(focusedTile) : tabStore.state.tabs
      const target = tabs[i - 1]
      if (target)
        tabOps.handleTabSelect(target)
    }, 'Tab')
  }

  // Tab navigation (previous/next via MRU within focused tile)
  cmd('app.previousTab', 'Previous Tab', () => {
    const focusedTile = layoutStore.focusedTileId()
    const tabs = focusedTile ? tabStore.getTabsForTile(focusedTile) : tabStore.state.tabs
    if (tabs.length < 2)
      return
    const activeKey = tabStore.state.activeTabKey
    const idx = tabs.findIndex(t => `${t.type}:${t.id}` === activeKey)
    const prev = tabs[(idx - 1 + tabs.length) % tabs.length]
    if (prev)
      tabOps.handleTabSelect(prev)
  }, 'Tab')

  cmd('app.nextTab', 'Next Tab', () => {
    const focusedTile = layoutStore.focusedTileId()
    const tabs = focusedTile ? tabStore.getTabsForTile(focusedTile) : tabStore.state.tabs
    if (tabs.length < 2)
      return
    const activeKey = tabStore.state.activeTabKey
    const idx = tabs.findIndex(t => `${t.type}:${t.id}` === activeKey)
    const next = tabs[(idx + 1) % tabs.length]
    if (next)
      tabOps.handleTabSelect(next)
  }, 'Tab')

  // -----------------------------------------------------------------------
  // Context tracking
  // -----------------------------------------------------------------------

  // Static context
  setContext('platform', getPlatform())
  setContext('isDesktop', isTauriApp())

  // Lazy context (evaluated at dispatch time)
  registerLazyContext('inputFocused', () => {
    const el = document.activeElement
    if (!el)
      return false
    const tag = el.tagName
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT')
      return true
    if (el.getAttribute('contenteditable') === 'true')
      return true
    return false
  })

  registerLazyContext('editorFocused', () => {
    const el = document.activeElement
    return !!el?.closest('.ProseMirror')
  })

  registerLazyContext('terminalFocused', () => {
    const el = document.activeElement
    return !!el?.closest('.xterm')
  })

  // Reactive context: dialogOpen (via MutationObserver)
  const updateDialogOpen = () => {
    setContext('dialogOpen', document.querySelector('dialog[open]') !== null)
  }
  let observer: MutationObserver | undefined
  onMount(() => {
    updateDialogOpen()
    observer = new MutationObserver(updateDialogOpen)
    observer.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['open'] })
  })

  // Reactive context: activeTabType
  createEffect(() => {
    const type = activeTabType()
    setContext('activeTabType', type !== null ? (TAB_TYPE_LABELS[type] ?? '') : undefined)
  })

  // -----------------------------------------------------------------------
  // Bind keys (react to custom keybinding changes)
  // -----------------------------------------------------------------------

  // Auto-tracks customKeybindings — runs on initial value and on every change.
  createEffect(() => {
    const overrides = customKeybindings()
    const merged = mergeKeybindings(DEFAULT_KEYBINDINGS, overrides)
    setActiveBindings(merged)
    bindAll(merged)
  })

  // -----------------------------------------------------------------------
  // Cleanup
  // -----------------------------------------------------------------------

  onCleanup(() => {
    unbindAll()
    for (const cleanup of cleanups)
      cleanup()
    resetCommands()
    observer?.disconnect()
    unregisterLazyContext('inputFocused')
    unregisterLazyContext('editorFocused')
    unregisterLazyContext('terminalFocused')
  })
}
