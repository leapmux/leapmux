import type { Accessor } from 'solid-js'
import type { NewTabTarget, NewWorkspacePayload } from '~/components/shell/AppShellDialogs'
import type { TabContext } from '~/components/shell/tabContext'
import type { useAgentOperations } from '~/components/shell/useAgentOperations'
import type { useTabOperations } from '~/components/shell/useTabOperations'
import type { useTerminalOperations } from '~/components/shell/useTerminalOperations'
import type { DialogState } from '~/hooks/createDialogState'
import type { UserKeybindingOverride } from '~/lib/shortcuts/types'
import type { createLayoutStore, SplitOrientation } from '~/stores/layout.store'
import type { Tab } from '~/stores/tab.types'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import { createEffect, onCleanup, onMount } from 'solid-js'
import { getRuntimeState, platformBridge } from '~/api/platformBridge'
import { openPreferences } from '~/components/shell/UserMenuState'
import { TAB_TYPE_WIRE_TOKEN } from '~/generated/contracts/tab-types'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { loadDetectedEditors, resolvePreferredEditor } from '~/lib/externalEditors'
import { refreshFileTree, toggleHiddenFiles } from '~/lib/fileTreeOps'
import { createLogger } from '~/lib/logger'
import { registerCommand } from '~/lib/shortcuts/commands'
import { registerLazyContext, setContext, unregisterLazyContext } from '~/lib/shortcuts/context'
import { WORKSPACE_KEYBINDINGS } from '~/lib/shortcuts/defaults'
import { activateBindings, mergeKeybindings, unbindAll } from '~/lib/shortcuts/keybindings'
import { syncMacMenuAccelerator } from '~/lib/shortcuts/tauriAccelerator'
import { isTypingContext } from '~/lib/textInputBehavior'
import { getFocusedChatSend } from '~/stores/focusedChatSend.store'
import { canCloseTab, tabKey } from '~/stores/tab.helpers'

interface UseShortcutsProps {
  view: TabView
  selection: TabSelectionStore
  getActiveWorkspaceId: () => string | null
  layoutStore: ReturnType<typeof createLayoutStore>
  tabOps: ReturnType<typeof useTabOperations>
  agentOps: ReturnType<typeof useAgentOperations>
  termOps: ReturnType<typeof useTerminalOperations>

  newAgentDialog: DialogState<NewTabTarget>
  newTerminalDialog: DialogState<NewTabTarget>
  newWorkspaceDialog: DialogState<NewWorkspacePayload>
  hasActiveWorkspace: Accessor<boolean>
  toggleFloatingTab: () => void
  toggleLeftSidebar: () => void
  toggleRightSidebar: () => void
  activeTabType: Accessor<TabType | null>
  resolveFocusedTab: () => Tab | null
  /**
   * Whether the active workspace is archived, for the close shortcut's own
   * guard. The BUTTONS ask `canCloseTab`; without this the keyboard would be
   * the one surface that still closes a tab the buttons refuse.
   */
  isActiveWorkspaceArchived: () => boolean
  splitFocusedTile: (direction: SplitOrientation) => void
  scrollFocusedTabPage: (direction: -1 | 1) => void
  writeToFocusedTerminal: (data: string) => void
  /**
   * Active tab's working context (worker, dir, home). Same getter shape
   * as the rest of the shell uses — so future shortcuts that need
   * workerId/homeDir can read them without expanding this prop list.
   */
  getCurrentTabContext: () => TabContext
  customKeybindings: Accessor<UserKeybindingOverride[]>
  /** The pinned external editor, read from the REACTIVE preference. */
  preferredEditorId: Accessor<string | undefined>
  /**
   * Persist the preferred external editor through the REACTIVE preference.
   * The open-in-editor shortcut can fall back to another editor when the
   * pinned one is gone, and that fallback must reach the editor menu and
   * the settings row — a raw storage write leaves both showing the old
   * pin for the life of the page.
   */
  setPreferredEditorId: (id: string | undefined) => void
}

/**
 * The token the keybinding context keys a tab kind by. GENERATED from
 * contracts/tab-types.json, so it is the same string the tab strip publishes and
 * the CLI accepts. UNSPECIFIED maps to '', which no binding matches.
 */
const TAB_TYPE_LABELS: Partial<Record<TabType, string>> = TAB_TYPE_WIRE_TOKEN

// FFI contract: must match SHOW_PREFERENCES_MENU_ID in desktop/rust/src/main.rs.
const SHOW_PREFERENCES_MENU_ID = 'show-preferences'

/**
 * Root keyboard shortcut hook. Call once in AppShell.
 *
 * Registers all commands, sets up context tracking, and binds keys via tinykeys.
 */
export function useShortcuts(props: UseShortcutsProps): void {
  const {
    view,
    selection,
    getActiveWorkspaceId,
    layoutStore,
    tabOps,
    agentOps,
    termOps,
    newAgentDialog,
    newTerminalDialog,
    newWorkspaceDialog,
    hasActiveWorkspace,
    toggleFloatingTab,
    toggleLeftSidebar,
    toggleRightSidebar,
    activeTabType,
    resolveFocusedTab,
    splitFocusedTile,
    scrollFocusedTabPage,
    writeToFocusedTerminal,
    getCurrentTabContext,
    customKeybindings,
  } = props

  const log = createLogger('shortcuts')

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
      newWorkspaceDialog.open({})
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
      newWorkspaceDialog.open({})
      return
    }
    // An empty target: the shortcut is about the tab the user is looking at,
    // so the dialog follows the current tab context.
    newAgentDialog.open({})
  }, 'App')
  cmd('app.newTerminalDialog', 'New Terminal Dialog', () => {
    if (!hasActiveWorkspace())
      return
    newTerminalDialog.open({})
  }, 'App')
  cmd('app.newWorkspaceDialog', 'New Workspace Dialog', () => newWorkspaceDialog.open({}), 'App')
  cmd('app.refreshDirectoryTree', 'Refresh Directory Tree', () => refreshFileTree(), 'Files')
  cmd('app.toggleHiddenFiles', 'Toggle Hidden Files', () => toggleHiddenFiles(), 'Files')
  cmd('app.toggleFloatingTab', 'Toggle Floating Tab', toggleFloatingTab, 'Tab')
  cmd('app.closeActiveTab', 'Close Active Tab', () => {
    const tab = resolveFocusedTab()
    // The SAME predicate the tab strip and the sidebar tree draw their close
    // controls from. An archived workspace keeps its agent and terminal tabs, so
    // a shortcut that closed one would be the only surface that disagrees --
    // and the user would have no visible control to undo it with.
    if (tab && canCloseTab(props.isActiveWorkspaceArchived(), tab))
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
    openPreferences()
  }, 'App')

  cmd('app.openInExternalEditor', 'Open in External Editor', async () => {
    const dir = getCurrentTabContext().workingDir
    if (!dir)
      return
    // Solo-mode gate: in distributed mode the working dir lives on the worker
    // machine, not the local filesystem the app would hand to the editor process.
    const state = await getRuntimeState()
    if (!state.capabilities.localSolo)
      return
    // Persist any fallback pick through the reactive preference, not
    // straight to storage: the editor menu and the settings row both read
    // that signal, and a raw storage write leaves them showing the
    // previous editor for the life of the page.
    const target = resolvePreferredEditor(
      await loadDetectedEditors(),
      props.preferredEditorId(),
      props.setPreferredEditorId,
    )
    if (!target)
      return
    try {
      await platformBridge.openInEditor(target.id, dir)
    }
    catch (err) {
      log.warn('open_in_editor failed', { id: target.id, dir, err })
    }
  }, 'App')

  function getVisibleTabs() {
    const focusedTile = layoutStore.focusedTileId()
    return focusedTile ? view.forTile(focusedTile) : view.forWorkspace(getActiveWorkspaceId() ?? '')
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
      ? selection.activeKeyForTile(focusedTile)
      : selection.activeKeyForWorkspace(getActiveWorkspaceId() ?? '')
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

  cmd('chat.sendMessage', 'Send Message', () => {
    getFocusedChatSend()?.()
  }, 'Chat')

  // Terminal cursor navigation
  cmd('terminal.lineStart', 'Go to Line Start', () => writeToFocusedTerminal('\x01'), 'Terminal')
  cmd('terminal.lineEnd', 'Go to Line End', () => writeToFocusedTerminal('\x05'), 'Terminal')
  cmd('terminal.wordLeft', 'Go to Previous Word', () => writeToFocusedTerminal('\x1Bb'), 'Terminal')
  cmd('terminal.wordRight', 'Go to Next Word', () => writeToFocusedTerminal('\x1Bf'), 'Terminal')

  registerLazyContext('inputFocused', isTypingContext)

  registerLazyContext('editorFocused', () => {
    const el = document.activeElement
    return !!el?.closest('.ProseMirror')
  })

  registerLazyContext('chatInputFocused', () => {
    const el = document.activeElement
    return !!el?.closest('[data-chat-input]')
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
    const merged = mergeKeybindings(WORKSPACE_KEYBINDINGS, overrides)
    activateBindings(merged, 'workspace')
    syncMacMenuAccelerator(SHOW_PREFERENCES_MENU_ID, 'app.openPreferences', merged)
  })

  onCleanup(() => {
    unbindAll('workspace')
    // Per-command cleanups; resetCommands() would delete the core commands too.
    for (const cleanup of cleanups)
      cleanup()
    cancelAnimationFrame(dialogRafId)
    observer?.disconnect()
    unregisterLazyContext('inputFocused')
    unregisterLazyContext('editorFocused')
    unregisterLazyContext('chatInputFocused')
    unregisterLazyContext('terminalFocused')
  })
}
