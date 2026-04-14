import type { Accessor } from 'solid-js'
import type { useAgentOperations } from '~/components/shell/useAgentOperations'
import type { useTabOperations } from '~/components/shell/useTabOperations'
import type { useTerminalOperations } from '~/components/shell/useTerminalOperations'
import type { UserKeybindingOverride } from '~/lib/shortcuts/types'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import { createEffect, onCleanup, onMount } from 'solid-js'
import { isTauriApp, openWebInspector, quitApp, resetWebviewZoom, zoomInWebview, zoomOutWebview } from '~/api/platformBridge'
import { setShowPreferencesDialog } from '~/components/shell/UserMenu'
import { scrollActiveChatPage } from '~/components/chat/ChatView'
import { scrollActiveTerminalPage, writeToActiveTerminal } from '~/components/terminal/TerminalView'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { registerCommand, resetCommands } from '~/lib/shortcuts/commands'
import { registerLazyContext, setContext, unregisterLazyContext } from '~/lib/shortcuts/context'
import { DEFAULT_KEYBINDINGS } from '~/lib/shortcuts/defaults'
import { activateBindings, mergeKeybindings, unbindAll } from '~/lib/shortcuts/keybindings'
import { getPlatform } from '~/lib/shortcuts/platform'
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
  toggleFloatingTab: () => void
  toggleLeftSidebar: () => void
  toggleRightSidebar: () => void
  activeTabType: Accessor<TabType | null>
  customKeybindings: Accessor<UserKeybindingOverride[]>
}

const TAB_TYPE_LABELS: Partial<Record<TabType, string>> = {
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
    agentOps,
    termOps,
    setShowNewAgentDialog,
    setShowNewTerminalDialog,
    setShowNewWorkspace,
    toggleFloatingTab,
    toggleLeftSidebar,
    toggleRightSidebar,
    activeTabType,
    customKeybindings,
  } = props

  const cleanups: (() => void)[] = []

  function isVisibleElement(el: Element): el is HTMLElement {
    return el instanceof HTMLElement && el.offsetParent !== null
  }

  function clickShortcutButton(testId: string): boolean {
    const dialogs = [...document.querySelectorAll('dialog[open]')]
    const topmostDialog = dialogs.at(-1) as HTMLDialogElement | undefined
    const dialogButton = topmostDialog?.querySelector(`[data-testid="${testId}"]`)
    if (dialogButton && isVisibleElement(dialogButton)) {
      dialogButton.click()
      return true
    }

    const button = [...document.querySelectorAll(`[data-testid="${testId}"]`)]
      .find(isVisibleElement)
    if (button) {
      button.click()
      return true
    }
    return false
  }

  function cmd(id: string, title: string, handler: () => void | Promise<void>, category?: string) {
    cleanups.push(registerCommand({ id, title, handler, category }))
  }

  cmd('app.newAgent', 'New Agent', () => agentOps.handleOpenAgent(), 'App')
  cmd('app.newTerminal', 'New Terminal', () => termOps.handleOpenTerminal(), 'App')
  cmd('app.newAgentDialog', 'New Agent Dialog', () => setShowNewAgentDialog(true), 'App')
  cmd('app.newTerminalDialog', 'New Terminal Dialog', () => setShowNewTerminalDialog(true), 'App')
  cmd('app.newWorkspaceDialog', 'New Workspace Dialog', () => setShowNewWorkspace(true), 'App')
  cmd('app.refreshDirectoryTree', 'Refresh Directory Tree', () => {
    clickShortcutButton('directory-selector-refresh')
      || clickShortcutButton('files-refresh')
  }, 'Files')
  cmd('app.toggleHiddenFiles', 'Toggle Hidden Files', () => {
    clickShortcutButton('directory-selector-show-hidden-toggle')
      || clickShortcutButton('files-show-hidden-toggle')
  }, 'Files')
  cmd('app.toggleFloatingTab', 'Toggle Floating Tab', toggleFloatingTab, 'Tab')
  cmd('app.closeActiveTab', 'Close Active Tab', () => {
    const tab = tabStore.activeTab()
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

  cmd('app.splitTileHorizontal', 'Split Tile Horizontally', () => withFocusedTile(id => layoutStore.splitTileHorizontal(id)), 'Layout')
  cmd('app.splitTileVertical', 'Split Tile Vertically', () => withFocusedTile(id => layoutStore.splitTileVertical(id)), 'Layout')
  cmd('app.openPreferences', 'Open Preferences', () => {
    setShowPreferencesDialog(true)
  }, 'App')
  cmd('app.openWebInspector', 'Open Web Inspector', () => openWebInspector(), 'App')
  cmd('app.zoomOutWebview', 'Zoom Out', () => zoomOutWebview(), 'View')
  cmd('app.zoomInWebview', 'Zoom In', () => zoomInWebview(), 'View')
  cmd('app.resetWebviewZoom', 'Actual Size', () => resetWebviewZoom(), 'View')
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
  cmd('app.scrollActiveTabPageUp', 'Scroll Active Tab Up One Page', () => {
    const tabType = activeTabType()
    if (tabType === TabType.AGENT)
      scrollActiveChatPage(-1)
    else if (tabType === TabType.TERMINAL)
      scrollActiveTerminalPage(-1)
  }, 'View')
  cmd('app.scrollActiveTabPageDown', 'Scroll Active Tab Down One Page', () => {
    const tabType = activeTabType()
    if (tabType === TabType.AGENT)
      scrollActiveChatPage(1)
    else if (tabType === TabType.TERMINAL)
      scrollActiveTerminalPage(1)
  }, 'View')

  // Terminal cursor navigation
  cmd('terminal.lineStart', 'Go to Line Start', () => writeToActiveTerminal('\x01'), 'Terminal')
  cmd('terminal.lineEnd', 'Go to Line End', () => writeToActiveTerminal('\x05'), 'Terminal')
  cmd('terminal.wordLeft', 'Go to Previous Word', () => writeToActiveTerminal('\x1Bb'), 'Terminal')
  cmd('terminal.wordRight', 'Go to Next Word', () => writeToActiveTerminal('\x1Bf'), 'Terminal')

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

  createEffect(() => {
    const overrides = customKeybindings()
    const merged = mergeKeybindings(DEFAULT_KEYBINDINGS, overrides)
    activateBindings(merged)
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
