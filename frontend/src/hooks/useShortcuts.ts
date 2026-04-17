import type { Accessor } from 'solid-js'
import type { useAgentOperations } from '~/components/shell/useAgentOperations'
import type { useTabOperations } from '~/components/shell/useTabOperations'
import type { useTerminalOperations } from '~/components/shell/useTerminalOperations'
import type { Keybinding, UserKeybindingOverride } from '~/lib/shortcuts/types'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore, Tab } from '~/stores/tab.store'
import { createEffect, onCleanup, onMount } from 'solid-js'
import { isTauriApp, openWebInspector, quitApp, resetWebviewZoom, setMenuItemAccelerator, zoomInWebview, zoomOutWebview } from '~/api/platformBridge'
import { setShowPreferencesDialog } from '~/components/shell/UserMenuState'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { refreshFileTree, toggleHiddenFiles } from '~/lib/fileTreeOps'
import { registerCommand, resetCommands } from '~/lib/shortcuts/commands'
import { registerLazyContext, setContext, unregisterLazyContext } from '~/lib/shortcuts/context'
import { DEFAULT_KEYBINDINGS } from '~/lib/shortcuts/defaults'
import { activateBindings, mergeKeybindings, unbindAll } from '~/lib/shortcuts/keybindings'
import { getPlatform } from '~/lib/shortcuts/platform'
import { getPrimaryBindingForCommand, tinykeysToTauriAccelerator } from '~/lib/shortcuts/tauriAccelerator'
import { tabKey } from '~/stores/tab.store'

interface UseShortcutsProps {
  tabStore: ReturnType<typeof createTabStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  tabOps: ReturnType<typeof useTabOperations>
  agentOps: ReturnType<typeof useAgentOperations>
  termOps: ReturnType<typeof useTerminalOperations>

  setShowNewAgentDialog: (v: boolean) => void
  setShowNewTerminalDialog: (v: boolean) => void
  setShowNewWorkspace: (v: boolean) => void
  hasActiveWorkspace: Accessor<boolean>
  toggleFloatingTab: () => void
  toggleLeftSidebar: () => void
  toggleRightSidebar: () => void
  activeTabType: Accessor<TabType | null>
  resolveFocusedTab: () => Tab | null
  splitFocusedTile: (direction: 'horizontal' | 'vertical') => void
  scrollFocusedTabPage: (direction: -1 | 1) => void
  writeToFocusedTerminal: (data: string) => void
  customKeybindings: Accessor<UserKeybindingOverride[]>
}

const TAB_TYPE_LABELS: Partial<Record<TabType, string>> = {
  [TabType.AGENT]: 'agent',
  [TabType.TERMINAL]: 'terminal',
  [TabType.FILE]: 'file',
}

// FFI contract: these strings must match the `*_MENU_ID` constants in
// `desktop/rust/src/main.rs`. Keep in sync when adding/renaming menu items.
const SHOW_PREFERENCES_MENU_ID = 'show-preferences'
const OPEN_WEB_INSPECTOR_MENU_ID = 'open-web-inspector'

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
    agentOps,
    termOps,
    setShowNewAgentDialog,
    setShowNewTerminalDialog,
    setShowNewWorkspace,
    hasActiveWorkspace,
    toggleFloatingTab,
    toggleLeftSidebar,
    toggleRightSidebar,
    activeTabType,
    resolveFocusedTab,
    splitFocusedTile,
    scrollFocusedTabPage,
    writeToFocusedTerminal,
    customKeybindings,
  } = props

  const cleanups: (() => void)[] = []

  function cmd(id: string, title: string, handler: () => void | Promise<void>, category?: string) {
    cleanups.push(registerCommand({ id, title, handler, category }))
  }

  // Agent/terminal shortcuts require an active workspace. When none is active,
  // agent shortcuts fall through to the new-workspace dialog (so the user can
  // still make progress), while terminal shortcuts no-op (there is no natural
  // redirect — terminals live inside a workspace).
  cmd('app.newAgent', 'New Agent', () => {
    if (!hasActiveWorkspace()) {
      setShowNewWorkspace(true)
      return
    }
    agentOps.handleOpenAgent()
  }, 'App')
  cmd('app.newTerminal', 'New Terminal', () => {
    if (!hasActiveWorkspace())
      return
    termOps.handleOpenTerminal()
  }, 'App')
  cmd('app.newAgentDialog', 'New Agent Dialog', () => {
    if (!hasActiveWorkspace()) {
      setShowNewWorkspace(true)
      return
    }
    setShowNewAgentDialog(true)
  }, 'App')
  cmd('app.newTerminalDialog', 'New Terminal Dialog', () => {
    if (!hasActiveWorkspace())
      return
    setShowNewTerminalDialog(true)
  }, 'App')
  cmd('app.newWorkspaceDialog', 'New Workspace Dialog', () => setShowNewWorkspace(true), 'App')
  cmd('app.refreshDirectoryTree', 'Refresh Directory Tree', () => refreshFileTree(), 'Files')
  cmd('app.toggleHiddenFiles', 'Toggle Hidden Files', () => toggleHiddenFiles(), 'Files')
  cmd('app.toggleFloatingTab', 'Toggle Floating Tab', toggleFloatingTab, 'Tab')
  cmd('app.closeActiveTab', 'Close Active Tab', () => {
    const tab = resolveFocusedTab()
    if (tab)
      tabOps.handleTabClose(tab)
  }, 'Tab')
  cmd('app.toggleLeftSidebar', 'Toggle Left Sidebar', toggleLeftSidebar, 'Layout')
  cmd('app.toggleRightSidebar', 'Toggle Right Sidebar', toggleRightSidebar, 'Layout')
  function withFocusedTile(fn: (id: string) => void) {
    const id = layoutStore.focusedTileId()
    if (id)
      fn(id)
  }

  cmd('app.splitTileHorizontal', 'Split Tile Horizontally', () => withFocusedTile(() => splitFocusedTile('horizontal')), 'Layout')
  cmd('app.splitTileVertical', 'Split Tile Vertically', () => withFocusedTile(() => splitFocusedTile('vertical')), 'Layout')
  cmd('app.openPreferences', 'Open Preferences', () => {
    setShowPreferencesDialog(true)
  }, 'App')
  cmd('app.openWebInspector', 'Open Web Inspector', () => openWebInspector(), 'App')
  cmd('app.zoomOutWebview', 'Zoom Out', () => zoomOutWebview(), 'View')
  cmd('app.zoomInWebview', 'Zoom In', () => zoomInWebview(), 'View')
  cmd('app.resetWebviewZoom', 'Actual Size', () => resetWebviewZoom(), 'View')
  cmd('dialog.close', 'Close Dialog', () => {
    const dialogs = [...document.querySelectorAll('dialog[open]')]
    const last = dialogs.at(-1) as HTMLDialogElement | undefined
    if (last && !last.hasAttribute('data-busy'))
      last.close()
  }, 'App')
  cmd('app.quit', 'Quit Application', () => quitApp(), 'App')

  function getVisibleTabs() {
    const focusedTile = layoutStore.focusedTileId()
    return focusedTile ? tabStore.getTabsForTile(focusedTile) : tabStore.state.tabs
  }

  for (let i = 1; i <= 9; i++) {
    cmd(`app.switchToTab${i}`, `Switch to Tab ${i}`, () => {
      const target = getVisibleTabs()[i - 1]
      if (target)
        tabOps.handleTabSelect(target)
    }, 'Tab')
  }

  function navigateTab(direction: -1 | 1) {
    const tabs = getVisibleTabs()
    if (tabs.length < 2)
      return
    const focusedTile = layoutStore.focusedTileId()
    const activeKey = focusedTile
      ? tabStore.getActiveTabKeyForTile(focusedTile)
      : tabStore.state.activeTabKey
    const idx = tabs.findIndex(t => tabKey(t) === activeKey)
    const target = tabs[(idx + direction + tabs.length) % tabs.length]
    if (target)
      tabOps.handleTabSelect(target)
  }

  cmd('app.previousTab', 'Previous Tab', () => navigateTab(-1), 'Tab')
  cmd('app.nextTab', 'Next Tab', () => navigateTab(1), 'Tab')
  const scrollActiveTabPage = (direction: -1 | 1) => {
    const tabType = activeTabType()
    if (tabType === TabType.AGENT || tabType === TabType.TERMINAL)
      scrollFocusedTabPage(direction)
  }
  cmd('app.scrollActiveTabPageUp', 'Scroll Active Tab Up One Page', () => scrollActiveTabPage(-1), 'View')
  cmd('app.scrollActiveTabPageDown', 'Scroll Active Tab Down One Page', () => scrollActiveTabPage(1), 'View')

  // Terminal cursor navigation
  cmd('terminal.lineStart', 'Go to Line Start', () => writeToFocusedTerminal('\x01'), 'Terminal')
  cmd('terminal.lineEnd', 'Go to Line End', () => writeToFocusedTerminal('\x05'), 'Terminal')
  cmd('terminal.wordLeft', 'Go to Previous Word', () => writeToFocusedTerminal('\x1Bb'), 'Terminal')
  cmd('terminal.wordRight', 'Go to Next Word', () => writeToFocusedTerminal('\x1Bf'), 'Terminal')

  setContext('platform', getPlatform())
  setContext('isDesktop', isTauriApp())

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

  const updateDialogOpen = () => {
    setContext('dialogOpen', document.querySelector('dialog[open]') !== null)
  }
  let observer: MutationObserver | undefined
  let dialogRafId = 0
  onMount(() => {
    updateDialogOpen()
    observer = new MutationObserver(() => {
      cancelAnimationFrame(dialogRafId)
      dialogRafId = requestAnimationFrame(updateDialogOpen)
    })
    observer.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['open'] })
  })
  createEffect(() => {
    const type = activeTabType()
    setContext('activeTabType', type !== null ? (TAB_TYPE_LABELS[type] ?? '') : undefined)
  })

  const lastSentAccelerator = new Map<string, string | undefined>()
  const syncMenuAccelerator = (menuItemId: string, commandId: string, merged: readonly Keybinding[]) => {
    const binding = getPrimaryBindingForCommand(merged, commandId)
    const accelerator = binding ? tinykeysToTauriAccelerator(binding) : undefined
    if (lastSentAccelerator.has(menuItemId) && lastSentAccelerator.get(menuItemId) === accelerator)
      return
    lastSentAccelerator.set(menuItemId, accelerator)
    setMenuItemAccelerator(menuItemId, accelerator)
  }

  createEffect(() => {
    const overrides = customKeybindings()
    const merged = mergeKeybindings(DEFAULT_KEYBINDINGS, overrides)
    activateBindings(merged)

    if (isTauriApp() && getPlatform() === 'mac') {
      syncMenuAccelerator(SHOW_PREFERENCES_MENU_ID, 'app.openPreferences', merged)
      syncMenuAccelerator(OPEN_WEB_INSPECTOR_MENU_ID, 'app.openWebInspector', merged)
    }
  })

  onCleanup(() => {
    unbindAll()
    for (const cleanup of cleanups)
      cleanup()
    resetCommands()
    cancelAnimationFrame(dialogRafId)
    observer?.disconnect()
    unregisterLazyContext('inputFocused')
    unregisterLazyContext('editorFocused')
    unregisterLazyContext('terminalFocused')
  })
}
