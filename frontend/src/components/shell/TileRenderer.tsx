import type { Component, JSX } from 'solid-js'
import type { CloseFlow } from './closeFlow'
import type { TabContext } from './tabContext'
import type { TileActions, TilePopAction } from './TileActionsMenu'
import type { useAgentOperations } from './useAgentOperations'
import type { useTerminalOperations } from './useTerminalOperations'
import type { FileAttachment } from '~/components/chat/attachments'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { createLoadingSignal } from '~/hooks/createLoadingSignal'
import type { ImperativeRef } from '~/lib/imperativeRef'
import type { createAgentSessionStore } from '~/stores/agentSession.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createControlStore } from '~/stores/control.store'
import type { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import type { createGitFileStatusStore } from '~/stores/gitFileStatus.store'
import type { createLayoutStore, SplitOrientation, TilePredicates } from '~/stores/layout.store'
import type { LayoutOwner } from '~/stores/layoutOwner'
import type { createTabStore } from '~/stores/tab.store'
import type { AgentTab, FileTab, Tab, TerminalTab } from '~/stores/tab.types'
import { create } from '@bufbuild/protobuf'
import { createEffect, createMemo, For, mapArray, onCleanup, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { AgentEditorPanel } from '~/components/chat/AgentEditorPanel'
import { ChatView } from '~/components/chat/ChatView'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { showWarnToast } from '~/components/common/Toast'
import { FileViewer } from '~/components/fileviewer/FileViewer'
import { TerminalView } from '~/components/terminal/TerminalView'
import { AgentChatMessageSchema, AgentStatus, ContentCompression, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { uint8ArrayToBase64 } from '~/lib/base64'
import { randomUUID } from '~/lib/idGenerator'
import { createImperativeRef } from '~/lib/imperativeRef'
import { relativizePath } from '~/lib/paths'
import { pluralize } from '~/lib/plural'
import { formatFileMention, formatFileQuote } from '~/lib/quoteUtils'
import { MAX_LOADED_CHAT_MESSAGES } from '~/stores/chat.store'
import { appendText, insertIntoMruAgentEditor } from '~/stores/editorRef.store'
import { buildTilePredicateMap, CLOSE_MODE_NONE } from '~/stores/layout.store'
import { agentTabToInfo } from '~/stores/tab.helpers'
import { shouldShowThinkingIndicator } from '~/utils/agentState'
import * as styles from './AppShell.css'
import { closePlanWithDispose, createCloseFlow } from './closeFlow'
import { EmptyTilePlaceholder } from './EmptyTilePlaceholder'
import { TabBar } from './TabBar'
import { Tile } from './Tile'
import { cleanupAfterWindowDisposal, focusTile as focusTileShared } from './tileLifecycle'

/**
 * Options for {@link createTileRenderer}. Grouped by concern so a single
 * factory call is readable; flat aliases at the top of the function body
 * keep the implementation working against familiar names.
 */
interface TileRendererOpts {
  /** Reactive shell stores; stable for the renderer's lifetime. */
  stores: {
    tabStore: ReturnType<typeof createTabStore>
    chatStore: ReturnType<typeof createChatStore>
    controlStore: ReturnType<typeof createControlStore>
    layoutStore: ReturnType<typeof createLayoutStore>
    agentSessionStore: ReturnType<typeof createAgentSessionStore>
    gitFileStatusStore?: ReturnType<typeof createGitFileStatusStore>
    workerInfoStore: { getHomeDir: (workerId: string) => string }
  }
  /** Tab/agent/terminal lifecycle hooks. */
  ops: {
    agentOps: ReturnType<typeof useAgentOperations>
    termOps: ReturnType<typeof useTerminalOperations>
  }
  /** Active-workspace state and tab-context accessors. */
  workspace: {
    isActiveWorkspaceMutatable: () => boolean
    isActiveWorkspaceArchived: () => boolean
    activeWorkspace: () => { id: string } | null
    getCurrentTabContext: () => TabContext
    getMruAgentContext: () => Pick<TabContext, 'workingDir' | 'homeDir'>
  }
  /** Per-tab handlers + in-flight close set. */
  tab: {
    handleTabSelect: (tab: Tab) => void
    handleTabClose: (tab: Tab) => Promise<boolean>
    setIsTabEditing: (fn: () => boolean) => void
    closingTabKeys: () => Set<string>
  }
  /** New-tab loading flags + dialog setters. */
  newTab: {
    newAgentLoadingProvider: () => AgentProvider | null
    newTerminalLoading: () => boolean
    newShellLoading: () => boolean
    setShowNewAgentDialog: (v: boolean) => void
    setShowNewTerminalDialog: (v: boolean) => void
  }
  /** Shell chrome state and sidebar toggles. */
  chrome: {
    isMobileLayout: () => boolean
    toggleLeftSidebar: () => void
    toggleRightSidebar: () => void
  }
  /** Cross-component refs the renderer threads to its tab content. */
  refs: {
    focusEditorRef: ImperativeRef<() => void>
    getScrollStateRef: ImperativeRef<() => { distFromBottom: number, atBottom: boolean } | undefined>
    forceScrollToBottomRef: ImperativeRef<() => void>
  }
  /** Floating-window plumbing. Omit to disable detach/attach support. */
  floatingWindow?: {
    store: ReturnType<typeof createFloatingWindowStore>
    onDetachTab?: (tab: Tab) => void
    onAttachTab?: (tab: Tab) => void
  }
  /** Settings-loading signal used by the empty-tile placeholder. */
  settingsLoading: ReturnType<typeof createLoadingSignal>
}

export function createTileRenderer(opts: TileRendererOpts) {
  const {
    tabStore,
    chatStore,
    controlStore,
    layoutStore,
    agentSessionStore,
    gitFileStatusStore,
    workerInfoStore,
  } = opts.stores
  const { agentOps, termOps } = opts.ops
  const {
    isActiveWorkspaceMutatable,
    isActiveWorkspaceArchived,
    activeWorkspace,
    getCurrentTabContext,
    getMruAgentContext,
  } = opts.workspace
  const { handleTabSelect, handleTabClose, setIsTabEditing, closingTabKeys } = opts.tab
  const {
    newAgentLoadingProvider,
    newTerminalLoading,
    newShellLoading,
    setShowNewAgentDialog,
    setShowNewTerminalDialog,
  } = opts.newTab
  const { isMobileLayout, toggleLeftSidebar, toggleRightSidebar } = opts.chrome
  const { focusEditorRef, getScrollStateRef, forceScrollToBottomRef } = opts.refs
  const { settingsLoading } = opts
  const floatingWindowStore = opts.floatingWindow?.store
  const onDetachTab = opts.floatingWindow?.onDetachTab
  const onAttachTab = opts.floatingWindow?.onAttachTab

  const chatHandlers = new Map<string, { pageScroll: (direction: -1 | 1) => void }>()
  const terminalHandlers = new Map<string, { pageScroll: (direction: -1 | 1) => void, write: (data: string) => void }>()

  const getActiveTabForTile = (tileId: string): Tab | null =>
    tabStore.getActiveTabForTile(tileId)

  const getWindowIdForTile = (tileId: string) => floatingWindowStore?.getWindowForTile(tileId) ?? null

  const focusTile = (tileId: string) => focusTileShared(layoutStore, floatingWindowStore, tileId)

  // Main-layout strategy: close the tile, scrub its tab-store records.
  const removeTileFromMain = (tileId: string) => {
    layoutStore.closeTile(tileId)
    tabStore.cleanupTile(tileId)
  }

  // Floating-window strategy: closeTile may dispose the entire window when
  // its last tile is removed; scrub every disposed tile and migrate focus
  // back to the main layout if needed.
  const removeTileFromWindow = (tileId: string, windowId: string) => {
    const fws = floatingWindowStore
    if (!fws)
      return
    // No-op when the window has already been auto-disposed (e.g. by
    // `useTabOperations.removeEmptyFloatingWindow` during a close-all
    // loop). The tile-store records are already cleaned in that case.
    if (!fws.getWindow(windowId)) {
      return
    }

    const focusedTileId = layoutStore.focusedTileId()
    const result = fws.closeTile(windowId, tileId)

    if (result.kind === 'disposed') {
      // `tileId` was definitely in the window before drop (otherwise
      // `closeTile` would return `noop`), so it's already in `tileIds`;
      // the helper covers both the "focus was on the closed tile" and
      // "focus was on a sibling that got swept up by the disposal" cases.
      cleanupAfterWindowDisposal(layoutStore, tabStore, result.tileIds)
    }
    else {
      if (focusedTileId === tileId) {
        const replacementTileId = fws.getWindow(windowId)?.focusedTileId
        if (replacementTileId)
          layoutStore.setFocusedTile(replacementTileId)
      }
      tabStore.cleanupTile(tileId)
    }
  }

  const removeTileFromLayout = (tileId: string, windowId: string | null) => {
    if (windowId)
      removeTileFromWindow(tileId, windowId)
    else
      removeTileFromMain(tileId)
  }

  // Locate the tile that should inherit `tileId`'s tabs when the user picks
  // "move tabs" in the close-tile dialog. Same-root sibling first; for the
  // floating-window-single-tile case (no in-window heir), fall back to the
  // first leaf in the main layout.
  const findHeirForTile = (tileId: string): string | null => {
    const windowId = getWindowIdForTile(tileId)
    if (windowId) {
      const fws = floatingWindowStore
      // Bail if the window's gone — there's nowhere to redirect.
      if (!fws || !fws.getWindow(windowId))
        return null
      const sameRootHeir = fws.owner(windowId).findHeirTile(tileId)
      return sameRootHeir ?? layoutStore.owner().firstLeafId()
    }
    return layoutStore.owner().findHeirTile(tileId)
  }

  // handleTabClose mutates tabStore synchronously while we iterate; snapshot
  // the tab arrays once at request time so the close-all loop walks a stable
  // list. Used by all three close flows (tile / window / grid).
  const collectTabsFromTiles = (tileIds: readonly string[]) =>
    tileIds.flatMap(id => [...tabStore.getTabsForTile(id)])

  // Tile-close flow.
  interface ClosingTile {
    tileId: string
  }
  const closeTileFlow = createCloseFlow<ClosingTile>({
    handleTabClose,
    plan: (ctx) => {
      const originalWindowId = getWindowIdForTile(ctx.tileId)
      // `removeTileFromWindow` is itself idempotent against an
      // auto-disposed window (the close-all loop's
      // `removeEmptyFloatingWindow` may dispose mid-iteration), so
      // dispose serves both the preserve-and-discard-structure path and
      // the bare finalize path without a re-entry guard.
      return closePlanWithDispose({
        tabs: collectTabsFromTiles([ctx.tileId]),
        merge: () => {
          const heirId = findHeirForTile(ctx.tileId)
          if (heirId)
            tabStore.mergeTabsIntoTile(ctx.tileId, heirId)
        },
        dispose: () => removeTileFromLayout(ctx.tileId, originalWindowId),
      })
    },
  })

  // --- Close-floating-window flow ---
  //
  // Closing a floating window via the chrome close button has the same
  // "tabs at risk" question as closing a tile: ask the user whether to move
  // tabs back to the main layout or close them. Lives here (not in AppShell)
  // because TileRenderer owns the close-tile / close-grid flows and the
  // floating-window close path needs the same dependencies — handleTabClose,
  // tabStore, floatingWindowStore.
  interface ClosingFloatingWindow {
    windowId: string
  }

  // Drop the floating window itself and migrate focus back to the main
  // layout if it sat on the disposed window. Idempotent against an already-
  // disposed window (useTabOperations.removeEmptyFloatingWindow may have
  // dropped it during a close-all loop).
  const finishCloseFloatingWindow = (windowId: string, tileIds: string[]) => {
    const fws = floatingWindowStore
    if (!fws || !fws.getWindow(windowId))
      return
    fws.removeWindow(windowId)
    cleanupAfterWindowDisposal(layoutStore, tabStore, tileIds)
  }

  const closeFloatingWindowFlow = createCloseFlow<ClosingFloatingWindow>({
    handleTabClose,
    plan: (ctx) => {
      const fws = floatingWindowStore
      if (!fws)
        return { tabs: [], preserve: () => {}, finalize: () => {} }
      // Snapshot the tile-id list — the source set is invalidated mid-loop
      // by removeIfEmpty auto-cleanup inside handleTabClose.
      const tileIds = [...fws.getWindowTileIdSet(ctx.windowId) ?? []]
      const tabs = collectTabsFromTiles(tileIds)
      return closePlanWithDispose({
        tabs,
        merge: () => {
          const targetTileId = layoutStore.owner().firstLeafId()
          if (targetTileId) {
            for (const t of tileIds)
              tabStore.mergeTabsIntoTile(t, targetTileId)
          }
        },
        dispose: () => finishCloseFloatingWindow(ctx.windowId, tileIds),
      })
    },
  })

  // Pick the owner (main layout vs. one floating window) for a tile and
  // dispatch through the LayoutOwner interface so we don't repeat the
  // windowId branch at every call site.
  const ownerOf = (tileId: string): LayoutOwner => {
    const windowId = getWindowIdForTile(tileId)
    return windowId
      ? floatingWindowStore!.owner(windowId)
      : layoutStore.owner()
  }

  // Per-root predicate memos: one batched DFS per layout root. Each root
  // has its own memo so mutating one (e.g. dragging window A) doesn't
  // re-walk the others.
  const mainPredicates = createMemo(() => buildTilePredicateMap(layoutStore.state.root, 'main'))
  // `mapArray` reuses entries by reference, so each per-window memo is
  // created exactly once per window instance and torn down on removal.
  // Emit `[id, memo]` tuples here so the consolidated index below can
  // build a `Map` directly without a second indexing walk.
  const windowPredicateEntries = mapArray(
    () => floatingWindowStore?.state.windows ?? [],
    (w) => {
      const memo = createMemo(() => buildTilePredicateMap(w.layoutRoot, 'floating'))
      return [w.id, memo] as const
    },
  )
  // Map<windowId, perWindowPredicateMemo> built in one pass; rebuilt only
  // when the window list reshapes (insert/remove/reorder).
  const windowPredicatesById = createMemo(() => new Map(windowPredicateEntries()))
  const lookupPredicates = (tileId: string): TilePredicates | undefined => {
    const windowId = getWindowIdForTile(tileId)
    if (windowId === null)
      return mainPredicates().get(tileId)
    return windowPredicatesById().get(windowId)?.().get(tileId)
  }

  const splitTile = (tileId: string, direction: SplitOrientation) => {
    ownerOf(tileId).splitTile(tileId, direction)
  }

  const makeGrid = (tileId: string, rows: number, cols: number) => {
    ownerOf(tileId).makeGrid(tileId, rows, cols)
  }

  // Close-grid dialog state. `ownerTileId` is the tile that triggered the
  // dialog — used to look the owner up when building the plan, so the grid's
  // current tile ids are read once at request time.
  interface ClosingGrid {
    gridId: string
    ownerTileId: string
  }
  const closeGridFlow = createCloseFlow<ClosingGrid>({
    handleTabClose,
    plan: (ctx) => {
      // Capture owner + tile ids once: the dialog blocks UI while open, so
      // capturing at request time is safe; the close-all loop's auto-
      // cleanup may dispose a containing floating window, so finalize uses
      // the captured owner rather than re-resolving.
      const owner = ownerOf(ctx.ownerTileId)
      const tileIds = owner.collectTileIdsInGrid(ctx.gridId)
      return {
        tabs: collectTabsFromTiles(tileIds),
        preserve: () => {
          const newTileId = owner.replaceGridWithLeaf(ctx.gridId)
          if (newTileId)
            tabStore.reassignTabsToTile(tileIds, newTileId)
        },
        finalize: () => {
          owner.removeGrid(ctx.gridId)
          tabStore.cleanupTiles(tileIds)
        },
      }
    },
  })

  const handleTileClose = (tileId: string) => {
    const p = lookupPredicates(tileId)
    if (p?.closeMode.kind === 'grid')
      closeGridFlow.request({ gridId: p.closeMode.gridId, ownerTileId: tileId })
    else
      closeTileFlow.request({ tileId })
  }

  // Build the shared TileActions bag. Tile and the tile-level overflow menu
  // in TabBar both consume the same shape (TileActionsMenu's TileActions);
  // construct once per tile so the two surfaces stay in sync. Reads
  // predicates once per call instead of three times.
  const buildTileActions = (tileId: string): TileActions => {
    const p = lookupPredicates(tileId)
    return {
      closeMode: p?.closeMode ?? CLOSE_MODE_NONE,
      canSplit: p?.canSplit ?? false,
      canMakeGrid: p?.canMakeGrid ?? false,
      onSplit: (direction) => {
        splitTile(tileId, direction)
      },
      onMakeGrid: (rows, cols) => {
        makeGrid(tileId, rows, cols)
      },
      onClose: () => handleTileClose(tileId),
    }
  }

  const resolveFocusedTab = (): Tab | null => {
    const tileId = layoutStore.focusedTileId()
    return tileId ? getActiveTabForTile(tileId) : null
  }

  const agentThinking = (agentId: string) => shouldShowThinkingIndicator(
    agentTabToInfo(tabStore.getAgentTab(agentId)),
    agentSessionStore.getInfo(agentId),
    chatStore.getMessages(agentId),
    chatStore.state.streamingText[agentId],
    controlStore.getRequests(agentId).length,
  )

  const createTabBarForTile = (tileId: string, actions?: () => TileActions) => {
    // Reactive accessor either way: callers from `renderTile` pass their
    // own memo (so predicate updates propagate to surviving leaves); the
    // fallback path (mobile layout's focused-tile bar) creates one here
    // so it re-fires when `mainPredicates` updates without tileId
    // changing.
    const fallbackActions = createMemo(() => buildTileActions(tileId))
    const liveActions = actions ?? fallbackActions
    return (
      <TabBar
        tileId={tileId}
        tabs={tabStore.getTabsForTile(tileId)}
        activeTabKey={tabStore.getActiveTabKeyForTile(tileId)}
        readOnly={isActiveWorkspaceArchived()}
        closingTabKeys={closingTabKeys()}
        isEditingRef={(fn) => { setIsTabEditing(fn) }}
        onSelect={(tab) => {
          focusTile(tileId)
          handleTabSelect(tab)
        }}
        onClose={handleTabClose}
        onRename={(tab, title) => {
          tabStore.updateTabTitle(tab.type, tab.id, title)
          if (tab.type === TabType.AGENT) {
            const renameWorkerId = tabStore.getAgentTab(tab.id)?.workerId ?? ''
            workerRpc.renameAgent(renameWorkerId, { agentId: tab.id, title }).catch((err) => {
              showWarnToast('Failed to rename agent', err)
            })
          }
        }}
        newTab={{
          showAddButton: isActiveWorkspaceMutatable(),
          onNewAgent: agentOps.handleOpenAgent,
          onNewTerminal: termOps.handleOpenTerminal,
          onNewTerminalWithShell: termOps.handleOpenTerminalWithShell,
          onNewAgentAdvanced: () => setShowNewAgentDialog(true),
          onNewTerminalAdvanced: () => setShowNewTerminalDialog(true),
          availableProviders: agentOps.availableProviders(),
          availableShells: termOps.availableShells(),
          defaultShell: termOps.defaultShell(),
          newAgentLoadingProvider: newAgentLoadingProvider(),
          newTerminalLoading: newTerminalLoading(),
          newShellLoading: newShellLoading(),
          hasActiveTabContext: !!getCurrentTabContext().workerId,
        }}
        mobile={isMobileLayout()
          ? {
              onToggleLeftSidebar: toggleLeftSidebar,
              onToggleRightSidebar: toggleRightSidebar,
            }
          : undefined}
        tileActions={liveActions()}
      />
    )
  }

  const tabBarElement = () => createTabBarForTile(layoutStore.focusedTileId())

  const TileTerminalPane: Component<{
    terminals: TerminalTab[]
    activeTerminalId: string | null
    visible: boolean
    tileFocused: boolean
  }> = (props) => {
    let terminalPageScroll: ((direction: -1 | 1) => void) | undefined
    let terminalWrite: ((data: string) => void) | undefined
    let registeredTerminalId: string | null = null
    const syncTerminalHandler = () => {
      const activeTerminalId = props.activeTerminalId
      if (registeredTerminalId && registeredTerminalId !== activeTerminalId)
        terminalHandlers.delete(registeredTerminalId)
      registeredTerminalId = activeTerminalId
      if (activeTerminalId && terminalPageScroll && terminalWrite) {
        terminalHandlers.set(activeTerminalId, {
          pageScroll: terminalPageScroll,
          write: terminalWrite,
        })
      }
    }
    createEffect(syncTerminalHandler)
    onCleanup(() => {
      if (registeredTerminalId)
        terminalHandlers.delete(registeredTerminalId)
    })
    return (
      <div
        class={styles.tilePane}
        classList={{ [styles.tilePaneHidden]: !props.visible }}
      >
        <TerminalView
          terminals={props.terminals}
          activeTerminalId={props.activeTerminalId}
          visible={props.visible}
          tileFocused={props.tileFocused}
          onInput={termOps.handleTerminalInput}
          onResize={termOps.handleTerminalResize}
          onTitleChange={termOps.handleTerminalTitleChange}
          onBell={termOps.handleTerminalBell}
          onContentReady={id => tabStore.markTerminalContentReady(id)}
          pageScrollRef={(fn) => {
            terminalPageScroll = fn
            syncTerminalHandler()
          }}
          writeRef={(fn) => {
            terminalWrite = fn
            syncTerminalHandler()
          }}
        />
      </div>
    )
  }

  const renderTileContent = (tileId: string) => {
    // Memoised so the per-tile JSX bindings below can read `tab()` (and the
    // discriminated wrappers) many times per render without re-walking the
    // tab store on each access.
    const tab = createMemo(() => getActiveTabForTile(tileId))
    const agentTab = () => {
      const t = tab()
      return t?.type === TabType.AGENT ? t : null
    }
    const terminalTab = () => {
      const t = tab()
      return t?.type === TabType.TERMINAL ? t : null
    }
    const fileTab = () => {
      const t = tab()
      return t?.type === TabType.FILE ? t : null
    }
    // One pass over the tile's tabs produces buckets for the three For loops
    // below. The source `getTabsForTile` is itself memoised, so this memo only
    // re-runs when something in the tile actually changed.
    const tabsByType = createMemo(() => {
      const agent: AgentTab[] = []
      const file: FileTab[] = []
      const terminal: TerminalTab[] = []
      for (const t of tabStore.getTabsForTile(tileId)) {
        if (t.type === TabType.AGENT)
          agent.push(t)
        else if (t.type === TabType.FILE)
          file.push(t)
        else if (t.type === TabType.TERMINAL)
          terminal.push(t)
      }
      return { agent, file, terminal }
    })
    const tileAgentTabs = () => tabsByType().agent
    const tileFileTabs = () => tabsByType().file
    const tileTerminals = () => tabsByType().terminal
    const agentScrollStates = new Map<string, () => { distFromBottom: number, atBottom: boolean } | undefined>()
    const agentScrollToBottoms = new Map<string, () => void>()
    createEffect(() => {
      const activeId = agentTab()?.id
      if (layoutStore.focusedTileId() !== tileId)
        return
      getScrollStateRef.set(activeId ? agentScrollStates.get(activeId) : undefined)
      forceScrollToBottomRef.set(activeId ? agentScrollToBottoms.get(activeId) : undefined)
    })
    const hasTerminals = () => tileTerminals().length > 0

    return (
      <>
        <For each={tileAgentTabs()}>
          {(at) => {
            const agentId = at.id
            const agent = createMemo(() => tabStore.getAgentTab(agentId))
            onCleanup(() => {
              agentScrollStates.delete(agentId)
              agentScrollToBottoms.delete(agentId)
              chatHandlers.delete(agentId)
            })
            return (
              <div class={styles.tilePane} classList={{ [styles.tilePaneHidden]: agentTab()?.id !== at.id }}>
                <Show
                  when={agent()}
                  fallback={<div class={styles.placeholder}>Agent not found.</div>}
                >
                  <ChatView
                    agentId={agentId}
                    messages={chatStore.getMessages(agentId)}
                    messageVersion={chatStore.getMessageVersion(agentId)}
                    streamingText={chatStore.state.streamingText[agentId] ?? ''}
                    streamingType={agentSessionStore.getInfo(agentId).streamingType}
                    agentWorking={agentThinking(agentId)}
                    tabActive={agentTab()?.id === at.id}
                    messageErrors={chatStore.state.messageErrors}
                    messagePendingLabels={chatStore.state.messagePendingLabels}
                    onRetryMessage={messageId => agentOps.handleRetryMessage(agentId, messageId)}
                    onDeleteMessage={messageId => agentOps.handleDeleteMessage(agentId, messageId)}
                    workingDir={agent()?.workingDir}
                    homeDir={workerInfoStore.getHomeDir(agent()?.workerId ?? '')}
                    hasOlderMessages={chatStore.hasOlderMessages(agentId)}
                    fetchingOlder={chatStore.isFetchingOlder(agentId)}
                    onLoadOlderMessages={() => chatStore.loadOlderMessages(agent()?.workerId ?? '', agentId)}
                    onTrimOldMessages={() => chatStore.trimOldMessages(agentId, MAX_LOADED_CHAT_MESSAGES)}
                    savedViewportScroll={chatStore.getSavedViewportScroll(agentId)}
                    onClearSavedViewportScroll={() => chatStore.clearSavedViewportScroll(agentId)}
                    onScrollApiReady={(api) => {
                      agentScrollStates.set(agentId, api.getScrollState)
                      agentScrollToBottoms.set(agentId, api.forceScrollToBottom)
                      chatHandlers.set(agentId, { pageScroll: api.pageScroll })
                      if (agentTab()?.id === at.id) {
                        getScrollStateRef.set(api.getScrollState)
                        forceScrollToBottomRef.set(api.forceScrollToBottom)
                      }
                    }}
                    getToolUseParsedBySpanId={spanId => chatStore.getToolUseParsedBySpanId(agentId, spanId)}
                    getToolResultParsedBySpanId={spanId => chatStore.getToolResultParsedBySpanId(agentId, spanId)}
                    getCommandStreamBySpanId={spanId => chatStore.getCommandStream(agentId, spanId)}
                    getTodoById={taskId => chatStore.getTodoById(agentId, taskId)}
                    onQuote={isActiveWorkspaceArchived()
                      ? undefined
                      : (text) => {
                          appendText(agentId, text)
                          focusEditorRef()?.()
                        }}
                    onReply={isActiveWorkspaceArchived()
                      ? undefined
                      : (text) => {
                          appendText(agentId, text)
                          focusEditorRef()?.()
                        }}
                    agentStatus={agent()?.agentStatus}
                    startupError={agent()?.startupError}
                    startupMessage={agent()?.startupMessage}
                    providerLabel={agentProviderLabel(agent()?.agentProvider)}
                  />
                </Show>
              </div>
            )
          }}
        </For>

        <Show when={hasTerminals()}>
          <TileTerminalPane
            terminals={tileTerminals()}
            activeTerminalId={terminalTab()?.id ?? null}
            visible={!!terminalTab()}
            tileFocused={layoutStore.focusedTileId() === tileId}
          />
        </Show>

        <For each={tileFileTabs()}>
          {(ft) => {
            const fileRelPath = () => {
              const ctx = getMruAgentContext()
              return relativizePath(ft.filePath ?? '', ctx.workingDir, ctx.homeDir)
            }
            // Single lookup shared by `gitFileStatus` and
            // `hasStagedAndUnstaged` so both props read from one memo
            // cell instead of walking the file-status map on every
            // reactive tick.
            const gitEntry = createMemo(() => gitFileStatusStore?.getFileStatus(ft.filePath ?? ''))
            const hasStagedAndUnstaged = createMemo(() => {
              const entry = gitEntry()
              if (!entry)
                return false
              return entry.stagedStatus !== GitFileStatusCode.UNSPECIFIED
                && entry.unstagedStatus !== GitFileStatusCode.UNSPECIFIED
            })
            return (
              <div class={styles.tilePane} classList={{ [styles.tilePaneHidden]: fileTab()?.id !== ft.id }}>
                <FileViewer
                  workerId={ft.workerId ?? ''}
                  filePath={ft.filePath ?? ''}
                  rootPath={getMruAgentContext().workingDir}
                  homeDir={getMruAgentContext().homeDir}
                  displayMode={ft.displayMode}
                  onDisplayModeChange={mode => tabStore.setTabDisplayMode(ft.type, ft.id, mode)}
                  onQuote={isActiveWorkspaceArchived()
                    ? undefined
                    : (text, startLine, endLine) => {
                        if (startLine != null && endLine != null) {
                          insertIntoMruAgentEditor(tabStore, formatFileQuote(fileRelPath(), startLine, endLine, text))
                        }
                      }}
                  onMention={isActiveWorkspaceArchived()
                    ? undefined
                    : () => {
                        insertIntoMruAgentEditor(tabStore, formatFileMention(fileRelPath()), 'inline')
                      }}
                  fileViewMode={ft.fileViewMode}
                  fileDiffBase={ft.fileDiffBase}
                  gitFileStatus={gitEntry()}
                  hasStagedAndUnstaged={hasStagedAndUnstaged()}
                  onFileViewModeChange={mode => tabStore.setTabFileViewMode(ft.type, ft.id, mode)}
                  onFileDiffBaseChange={base => tabStore.setTabFileDiffBase(ft.type, ft.id, base)}
                />
              </div>
            )
          }}
        </For>

        <Show when={!tab() && activeWorkspace()}>
          <EmptyTilePlaceholder
            archived={isActiveWorkspaceArchived()}
            showActions={!layoutStore.hasMultipleTiles() || layoutStore.focusedTileId() === tileId}
            onOpenAgent={() => {
              focusTile(tileId)
              agentOps.handleOpenAgent()
            }}
            onOpenTerminal={() => {
              focusTile(tileId)
              termOps.handleOpenTerminal()
            }}
          />
        </Show>
      </>
    )
  }

  const focusedAgentId = createMemo(() => {
    const tileId = layoutStore.focusedTileId()
    const tab = getActiveTabForTile(tileId)
    if (!tab || tab.type !== TabType.AGENT)
      return null
    return tab.id
  })

  // Refs for ChatDropZone integration: addFiles and triggerSend from AgentEditorPanel.
  const addFilesRef = createImperativeRef<(files: FileList | File[]) => Promise<number>>()
  const addDropDataTransferRef = createImperativeRef<(dataTransfer: DataTransfer) => Promise<number>>()
  const triggerSendRef = createImperativeRef<() => void>()

  // Clear refs when no agent is focused to avoid stale closures.
  createEffect(() => {
    if (!focusedAgentId()) {
      addFilesRef.set(undefined)
      addDropDataTransferRef.set(undefined)
      triggerSendRef.set(undefined)
    }
  })

  const handleFileDrop = async (dataTransfer: DataTransfer, shiftKey: boolean) => {
    const addDrop = addDropDataTransferRef()
    if (addDrop) {
      const addedCount = await addDrop(dataTransfer)
      if (shiftKey && addedCount > 0)
        triggerSendRef()?.()
      return
    }
    const addFiles = addFilesRef()
    if (!addFiles)
      return
    const addedCount = await addFiles(dataTransfer.files)
    if (shiftKey && addedCount > 0)
      triggerSendRef()?.()
  }

  const FocusedAgentEditorPanel: Component<{ containerHeight: number }> = (props) => {
    const agentId = () => focusedAgentId()!
    return (
      <AgentEditorPanel
        agentId={agentId()}
        agent={agentTabToInfo(tabStore.getAgentTab(agentId()))}
        // eslint-disable-next-line solid/reactivity -- async event handler; reactive tracking isn't needed for user-invoked callbacks
        onSendMessage={async (content, fileAttachments?: FileAttachment[]) => {
          const id = focusedAgentId()
          if (!id)
            return
          forceScrollToBottomRef()?.()
          const sendAgent = tabStore.getAgentTab(id)
          const status = sendAgent?.agentStatus

          // Build optimistic message JSON with attachment data so retry can
          // recover the binary content without re-uploading.
          const optimisticPayload: Record<string, unknown> = { content }
          if (fileAttachments && fileAttachments.length > 0) {
            optimisticPayload.attachments = fileAttachments.map(a => ({
              filename: a.filename,
              mime_type: a.mimeType,
              data: uint8ArrayToBase64(a.data),
            }))
          }

          // Create an optimistic local message so it appears immediately in the chat.
          const localId = `local-${randomUUID()}`
          const localMsg = create(AgentChatMessageSchema, {
            id: localId,
            source: MessageSource.USER,
            content: new TextEncoder().encode(JSON.stringify(optimisticPayload)),
            contentCompression: ContentCompression.NONE,
            seq: 0n,
            createdAt: new Date().toISOString(),
            agentProvider: sendAgent?.agentProvider,
          })
          chatStore.addMessage(id, localMsg)

          const protoAttachments = fileAttachments?.map(a => ({
            filename: a.filename,
            mimeType: a.mimeType,
            data: a.data,
          })) ?? []

          // Agent is still starting — queue the message. The
          // useWorkspaceConnection status-change handler flushes on
          // ACTIVE, or marks failed on STARTUP_FAILED.
          if (status === AgentStatus.STARTING) {
            chatStore.setMessagePendingLabel(localId, `Queued — ${agentProviderLabel(sendAgent?.agentProvider)} is starting…`)
            chatStore.enqueuePendingOutbound(id, { localId, content, attachments: protoAttachments })
            return
          }
          const persistFailed = (reason: string) => {
            chatStore.setMessageError(localId, reason)
            chatStore.persistLocalMessage(
              id,
              localId,
              content,
              reason,
              fileAttachments?.map(a => ({
                filename: a.filename,
                mime_type: a.mimeType,
                data: uint8ArrayToBase64(a.data),
              })),
            )
          }

          // Agent failed to start — render the message as an error
          // bubble immediately and reject the send.
          if (status === AgentStatus.STARTUP_FAILED) {
            persistFailed('Agent failed to start')
            return
          }

          try {
            await workerRpc.sendAgentMessage(sendAgent?.workerId ?? '', {
              agentId: id,
              content,
              attachments: protoAttachments,
            })
            // Keep the optimistic message until the persisted message arrives.
            // chatStore.addMessage() reconciles the matching server echo in place.
          }
          catch {
            persistFailed('Failed to deliver')
          }
        }}
        addFilesRef={(fn) => { addFilesRef.set(fn) }}
        addDropDataTransferRef={(fn) => { addDropDataTransferRef.set(fn) }}
        triggerSendRef={(fn) => { triggerSendRef.set(fn) }}
        disabled={false}
        focusRef={(fn) => { focusEditorRef.set(fn) }}
        controlRequests={controlStore.getRequests(agentId())}
        onControlResponse={agentOps.handleControlResponse}
        onSettingChange={change => agentOps.handleAgentSettingChange(agentId(), change)}
        onPermissionModeChange={mode => agentOps.handlePermissionModeChange(agentId(), mode)}
        onInterrupt={() => agentOps.handleInterrupt(agentId())}
        settingsLoading={settingsLoading.loading()}
        agentSessionInfo={agentSessionStore.getInfo(agentId())}
        agentWorking={agentThinking(agentId())}
        containerHeight={props.containerHeight}
      />
    )
  }

  const renderTile = (tileId: string) => {
    // Memoise the action bag so predicate updates after structural
    // mutations (e.g. a sibling closes, flipping closeMode for surviving
    // leaves) propagate without requiring renderTile to re-run. <Tile>
    // and the TabBar overflow menu read the bag through reactive prop
    // getters, so passing `actions()` here keeps both surfaces in sync.
    const actions = createMemo(() => buildTileActions(tileId))
    // Memoise the per-tile lookups used in pop affordance bindings so each
    // prop re-evaluation reuses one cached projection per tile.
    const windowId = createMemo(() => getWindowIdForTile(tileId))
    const activeTab = createMemo(() => getActiveTabForTile(tileId))
    const pop = createMemo<TilePopAction | undefined>(() => {
      const tab = activeTab()
      if (!tab)
        return undefined
      const inMain = windowId() === null
      const handler = inMain ? onDetachTab : onAttachTab
      if (!handler)
        return undefined
      const label = inMain ? 'Pop out to floating window' : 'Pop in to main window'
      const testId = inMain ? 'pop-out-button' : 'pop-in-button'
      return { label, testId, onClick: () => handler(tab) }
    })
    return (
      <Tile
        tileId={tileId}
        isFocused={layoutStore.focusedTileId() === tileId}
        actions={actions()}
        tabBar={createTabBarForTile(tileId, actions)}
        onFocus={() => {
          focusTile(tileId)
          const tab = activeTab()
          if (tab) {
            tabStore.setActiveTab(tab.type, tab.id)
          }
        }}
        pop={pop()}
      >
        {renderTileContent(tileId)}
      </Tile>
    )
  }

  return {
    getActiveTabForTile,
    resolveFocusedTab,
    createTabBarForTile,
    tabBarElement,
    renderTileContent,
    focusedAgentId,
    splitFocusedTile(direction: SplitOrientation) {
      const tileId = layoutStore.focusedTileId()
      if (tileId)
        splitTile(tileId, direction)
    },
    scrollFocusedTabPage(direction: -1 | 1) {
      const tab = resolveFocusedTab()
      if (!tab)
        return
      if (tab.type === TabType.AGENT) {
        chatHandlers.get(tab.id)?.pageScroll(direction)
      }
      else if (tab.type === TabType.TERMINAL) {
        terminalHandlers.get(tab.id)?.pageScroll(direction)
      }
    },
    writeToFocusedTerminal(data: string) {
      const tab = resolveFocusedTab()
      if (tab?.type !== TabType.TERMINAL)
        return
      terminalHandlers.get(tab.id)?.write(data)
    },
    FocusedAgentEditorPanel,
    renderTile,
    handleFileDrop,
    fileDropDisabled: () => {
      const agentId = focusedAgentId()
      if (!agentId)
        return true
      return controlStore.getRequests(agentId).length > 0
    },
    requestCloseFloatingWindow: (windowId: string) => {
      closeFloatingWindowFlow.request({ windowId })
    },
    /**
     * Render the close-grid / close-tile / close-floating-window confirmation
     * dialogs. The parent layout component must include this in its tree so
     * the dialogs appear when their respective close flows trigger.
     */
    CloseDialogs: () => (
      <>
        <CloseFlowDialog
          flow={closeGridFlow}
          title="Close grid"
          testIdPrefix="close-grid"
          confirmLabel="Convert to tile"
          confirmTestIdSuffix="convert"
          noun="grid"
          tabCount={ctx => ownerOf(ctx.ownerTileId)
            .collectTileIdsInGrid(ctx.gridId)
            .reduce((n, id) => n + tabStore.getTabsForTile(id).length, 0)}
        />
        <CloseFlowDialog
          flow={closeTileFlow}
          title="Close tile"
          testIdPrefix="close-tile"
          confirmLabel="Move tabs to neighbor"
          confirmTestIdSuffix="move"
          noun="tile"
          tabCount={ctx => tabStore.getTabsForTile(ctx.tileId).length}
        />
        <CloseFlowDialog
          flow={closeFloatingWindowFlow}
          title="Close floating window"
          testIdPrefix="close-floating-window"
          confirmLabel="Move tabs to main"
          confirmTestIdSuffix="move"
          noun="window"
          tabCount={(ctx) => {
            const fws = floatingWindowStore
            if (!fws)
              return 0
            const set = fws.getWindowTileIdSet(ctx.windowId)
            if (!set)
              return 0
            let n = 0
            for (const t of set)
              n += tabStore.getTabsForTile(t).length
            return n
          }}
        />
      </>
    ),
  }
}

/**
 * Renders one of the three close-confirmation dialogs (grid/tile/floating
 * window). Each flow's "preserve tabs" primary, "close all tabs" secondary,
 * and tab-count copy share the same shape — only the labels and the
 * tab-count accessor vary.
 */
function CloseFlowDialog<Ctx>(props: {
  flow: CloseFlow<Ctx>
  title: string
  testIdPrefix: string
  confirmLabel: string
  confirmTestIdSuffix: string
  noun: string
  tabCount: (ctx: Ctx) => number
}): JSX.Element {
  return (
    <Show when={props.flow.signal()}>
      {(ctx) => {
        const count = createMemo(() => props.tabCount(ctx()))
        return (
          <ConfirmDialog
            title={props.title}
            data-testid={`${props.testIdPrefix}-dialog`}
            cancelTestId={`${props.testIdPrefix}-cancel`}
            confirmLabel={props.confirmLabel}
            confirmTestId={`${props.testIdPrefix}-${props.confirmTestIdSuffix}`}
            busy={props.flow.busy()}
            onConfirm={() => props.flow.primary()}
            onCancel={() => props.flow.cancel()}
            secondary={{
              label: 'Close all tabs',
              testId: `${props.testIdPrefix}-close-all`,
              onClick: () => { void props.flow.closeAll() },
              danger: true,
            }}
          >
            <p>{`This ${props.noun} contains ${pluralize(count(), 'tab')}. What would you like to do?`}</p>
          </ConfirmDialog>
        )
      }}
    </Show>
  )
}
