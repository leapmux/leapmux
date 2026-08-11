import type { Component, JSX } from 'solid-js'
import type { CloseFlow } from './closeFlow'
import type { mruAgentEditorDeps } from './mruAgentEditorDeps'
import type { TabContext } from './tabContext'
import type { TileActions, TilePopAction } from './TileActionsMenu'
import type { useAgentOperations } from './useAgentOperations'
import type { useTerminalOperations } from './useTerminalOperations'
import type { FileAttachment } from '~/components/chat/attachments'
import type { ChatMessageLookups, ChatRailProps } from '~/components/chat/ChatView'
import type { BranchRef } from '~/components/workspace/WorkspaceTabTree'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { ToggleDialogState } from '~/hooks/createDialogState'
import type { createLoadingSignal } from '~/hooks/createLoadingSignal'
import type { ImperativeRef } from '~/lib/imperativeRef'
import type { createAgentSessionStore } from '~/stores/agentSession.store'
import type { createChatStore } from '~/stores/chat.store'
import type { TabWorkState } from '~/stores/chatBackgroundTasks'
import type { SavedViewportScroll } from '~/stores/chatTypes'
import type { createControlStore } from '~/stores/control.store'
import type { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import type { createGitFileStatusStore } from '~/stores/gitFileStatus.store'
import type { createLayoutStore, SplitOrientation, TilePredicates } from '~/stores/layout.store'
import type { LayoutOwner } from '~/stores/layoutOwner'
import type { AgentTab, FileTab, Tab, TerminalTab } from '~/stores/tab.types'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import { create } from '@bufbuild/protobuf'
import { createEffect, createMemo, For, mapArray, onCleanup, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { AgentEditorPanel } from '~/components/chat/AgentEditorPanel'
import { getCachedMarkPreview, warmMarkPreview } from '~/components/chat/chatMarkPreview'
import { ChatView } from '~/components/chat/ChatView'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { showWarnToast } from '~/components/common/Toast'
import { FileViewer } from '~/components/fileviewer/FileViewer'
import { TerminalView } from '~/components/terminal/TerminalView'
import { focusedBranchAction } from '~/components/workspace/branchActions'
import { AgentChatMessageSchema, AgentStatus, ContentCompression, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { uint8ArrayToBase64 } from '~/lib/base64'
import { randomUUID } from '~/lib/idGenerator'
import { createImperativeRef } from '~/lib/imperativeRef'
import { createStableKeys } from '~/lib/keyedRows'
import { parentDirectory, relativizePath } from '~/lib/paths'
import { pluralize } from '~/lib/plural'
import { formatFileMention, formatFileQuote } from '~/lib/quoteUtils'
import { chipTasksFor, countActiveBackgroundTasks, rootWorkState, subagentWorkState } from '~/stores/chatBackgroundTasks'
import { appendText, insertIntoMruAgentEditor } from '~/stores/editorRef.store'
import { buildTilePredicateMap, CLOSE_MODE_NONE } from '~/stores/layout.store'
import { agentTabToInfo, isSteerableAgentTab, rootAgentIdFor } from '~/stores/tab.helpers'
import { emitMergeTabsIntoTile, emitReassignTabsToTile } from '~/stores/tabOps'
import { workerInfoStore } from '~/stores/workerInfo.store'
import { shouldShowThinkingIndicator } from '~/utils/agentState'
import * as styles from './AppShell.css'
import { closePlanWithDispose, createCloseFlow } from './closeFlow'
import { EmptyTilePlaceholder } from './EmptyTilePlaceholder'
import { TabBar } from './TabBar'
import { Tile } from './Tile'
import { cleanupAfterWindowDisposal, focusTile as focusTileShared } from './tileLifecycle'

/**
 * Why a non-steerable subagent's composer is dead. The composer states it as the
 * box's own placeholder, on the `[+]` menu's attach item, and on every settings
 * submenu, so the input never claims a lost connection for a transcript that was
 * never writable in the first place.
 */
const SUBAGENT_NO_MESSAGES_HINT = 'This subagent doesn\'t accept messages.'

/**
 * Options for {@link createTileRenderer}. Grouped by concern so a single
 * factory call is readable; flat aliases at the top of the function body
 * keep the implementation working against familiar names.
 */
interface TileRendererOpts {
  /** Reactive shell stores; stable for the renderer's lifetime. */
  stores: {
    view: TabView
    metadata: TabMetadataStore
    selection: TabSelectionStore
    chatStore: ReturnType<typeof createChatStore>
    controlStore: ReturnType<typeof createControlStore>
    layoutStore: ReturnType<typeof createLayoutStore>
    agentSessionStore: ReturnType<typeof createAgentSessionStore>
    gitFileStatusStore?: ReturnType<typeof createGitFileStatusStore>
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
  /**
   * Wiring for "insert this text into the MRU agent's editor", built once by
   * AppShell. See `mruAgentEditorDeps` — the quote and mention handlers below
   * used to hand-copy this object literal.
   */
  mruEditorDeps: ReturnType<typeof mruAgentEditorDeps>
  /** Per-tab handlers + in-flight close set. */
  tab: {
    handleTabSelect: (tab: Tab) => void
    handleTabClose: (tab: Tab) => Promise<boolean>
    setIsTabEditing: (fn: () => boolean) => void
    closingTabKeys: () => Set<string>
  }
  /** New-tab loading flags + dialog handles. */
  newTab: {
    newAgentLoadingProvider: () => AgentProvider | null
    newTerminalLoading: () => boolean
    newShellLoading: () => boolean
    newAgentDialog: ToggleDialogState
    newTerminalDialog: ToggleDialogState
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
    getScrollStateRef: ImperativeRef<() => SavedViewportScroll | undefined>
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
  /**
   * Open a subagent tab from a background-task row (shared by the sidebar
   * section and the ThinkingIndicator chip popover). Omit to keep rows/chips
   * non-clickable.
   */
  onOpenBackgroundTask?: (item: { childAgentId?: string, parentAgentId?: string, title?: string }) => void
  /**
   * Branch-action callbacks for the composer's GitBranch chip. Each receives a
   * fully-built {@link BranchRef} (the focused agent's repo + the tabs on its
   * branch); the shell opens the Change/Delete Branch dialog from it. Omit to
   * keep the chip non-interactive.
   */
  branch?: {
    onChangeBranch?: (ref: BranchRef) => void
    onDeleteBranch?: (ref: BranchRef) => void
    /**
     * Whether the branch's Worker is reachable. Both branch actions run on the
     * machine the repository is on, so the composer's branch chip disables
     * them when it is not — the same guard the sidebar's branch row applies.
     */
    isWorkerKnownOnline?: (workerId: string) => boolean
  }
}

export function createTileRenderer(opts: TileRendererOpts) {
  const {
    view,
    metadata,
    selection,
    chatStore,
    controlStore,
    layoutStore,
    agentSessionStore,
    gitFileStatusStore,
  } = opts.stores
  const { agentOps, termOps } = opts.ops
  const mruEditorDeps = opts.mruEditorDeps
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
    newAgentDialog,
    newTerminalDialog,
  } = opts.newTab
  const { isMobileLayout, toggleLeftSidebar, toggleRightSidebar } = opts.chrome
  const { focusEditorRef, getScrollStateRef, forceScrollToBottomRef } = opts.refs
  const branchCallbacks = opts.branch
  const { settingsLoading } = opts
  const floatingWindowStore = opts.floatingWindow?.store
  const onDetachTab = opts.floatingWindow?.onDetachTab
  const onAttachTab = opts.floatingWindow?.onAttachTab

  const chatHandlers = new Map<string, { pageScroll: (direction: -1 | 1) => void }>()
  // One walk per tick for the whole renderer, not two per file pane.
  // `getMruAgentContext` sorts the workspace's tabs on every call, and the file
  // pane reads it through Solid prop GETTERS -- `rootPath` and `homeDir` are two
  // separate reads, so an account with N open file panes paid 2N sorts, and the
  // getters' tracked sources include the account-wide tab join, so any tab's MRU
  // stamp or title rename re-ran all of them.
  //
  // The `equals` compares the two FIELDS rather than the fresh object the getter
  // mints, so an unchanged answer also stops the read from invalidating
  // `FileViewer`.
  const mruAgentContext = createMemo(() => getMruAgentContext(), undefined, {
    equals: (a, b) => a.workingDir === b.workingDir && a.homeDir === b.homeDir,
  })

  const terminalHandlers = new Map<string, { pageScroll: (direction: -1 | 1) => void, write: (data: string) => void }>()

  const getActiveTabForTile = (tileId: string): Tab | null =>
    selection.activeTabForTile(tileId) ?? null

  const getWindowIdForTile = (tileId: string) => floatingWindowStore?.getWindowForTile(tileId) ?? null

  const focusTile = (tileId: string) => focusTileShared(layoutStore, floatingWindowStore, tileId)

  // Main-layout strategy: close the tile. Its per-tile selection entry is
  // reclaimed by `useSelectionSweep` once the tile leaves the projection.
  const removeTileFromMain = (tileId: string) => {
    layoutStore.closeTile(tileId)
  }

  // Floating-window strategy: closeTile may dispose the entire window when
  // its last tile is removed, in which case focus migrates back to the main
  // layout. Per-tile selection entries for the disposed tiles are reclaimed by
  // `useSelectionSweep` when the window leaves the projection.
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
      cleanupAfterWindowDisposal(layoutStore, result.tileIds)
    }
    else {
      if (focusedTileId === tileId) {
        const replacementTileId = fws.getWindow(windowId)?.focusedTileId
        if (replacementTileId)
          layoutStore.setFocusedTile(replacementTileId)
      }
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

  // handleTabClose emits ops that land in `speculativeState` synchronously
  // while we iterate, so the join re-derives mid-loop; snapshot the tab arrays
  // once at request time and walk a stable list. Used by all three close flows
  // (tile / window / grid).
  const collectTabsFromTiles = (tileIds: readonly string[]) =>
    tileIds.flatMap(id => [...view.forTile(id)])

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
          if (heirId) {
            // Carry the source tile's selection to the heir when the heir has
            // none of its own, so "move tabs" lands the user on the tab they
            // were looking at rather than on the heir's first tab.
            //
            // `setActiveInTile`, NOT `setActiveById`: closing a tile does not
            // move focus (the close control stops propagation, so the tile is
            // never focused on its way out, and the heir is an adjacent
            // sibling unrelated to focus). Claiming the workspace pointer here
            // would hand it to a tab in a background tile while the user is
            // still reading another one — badging what they are looking at and
            // seeding the next agent from the wrong repo.
            const sourceActive = selection.activeTabForTile(ctx.tileId)
            emitMergeTabsIntoTile(view.forTile(ctx.tileId), view.forTile(heirId), heirId)
            selection.claimTileIfUnclaimed(sourceActive, heirId)
          }
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
  // the tab view, selection and floatingWindowStore.
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
    cleanupAfterWindowDisposal(layoutStore, tileIds)
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
            // The window's focused tile holds the tab the user was last on;
            // carry that selection to the destination when it has none.
            const focusedSourceTile = fws.getWindow(ctx.windowId)?.focusedTileId ?? tileIds[0]
            const sourceActive = focusedSourceTile ? selection.activeTabForTile(focusedSourceTile) : undefined
            for (const t of tileIds)
              emitMergeTabsIntoTile(view.forTile(t), view.forTile(targetTileId), targetTileId)
            if (sourceActive && !selection.state.activeByTile[targetTileId])
              selection.setActiveById(sourceActive.type, sourceActive.id)
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
          // Carry the selection of the cell the user was IN onto the
          // replacement leaf, the same way the tile-merge path above does.
          // `newTileId` is a fresh leaf (or the grid's own id) and so never
          // has an `activeByTile` entry; without this the surviving tile falls
          // through to `mruHead`, which right after a reload is plain position
          // order — `mru` is client-local and never persisted — so the user
          // lands on the leftmost merged tab instead of the one they had open.
          //
          // Prefer the FOCUSED cell, then the one whose close button was
          // clicked, then any: "the tab I was looking at" is a specific cell's
          // question, not the grid's.
          const focused = layoutStore.focusedTileId()
          const preferredOrder = [
            ...(tileIds.includes(focused) ? [focused] : []),
            ...(tileIds.includes(ctx.ownerTileId) ? [ctx.ownerTileId] : []),
            ...tileIds,
          ]
          const sourceActive = preferredOrder
            .map(id => selection.activeTabForTile(id))
            .find(tab => tab !== undefined)
          const newTileId = owner.replaceGridWithLeaf(ctx.gridId)
          if (newTileId) {
            emitReassignTabsToTile(view.all(), tileIds, newTileId)
            selection.claimTileIfUnclaimed(sourceActive, newTileId)
          }
        },
        finalize: () => {
          owner.removeGrid(ctx.gridId)
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

  // Background-task registry helpers. The registry is keyed by ROOT owner id,
  // so resolve up to the root for a child tab. Only roots key a registry, so a
  // child ChatView correctly shows no chip (empty).
  const bgRootFor = (agentId: string): string => rootAgentIdFor((id: string) => view.getAgentTab(id), agentId)
  const bgTasksFor = (agentId: string) => chatStore.backgroundTasks.get(bgRootFor(agentId))
  // Scoped in the store beside the other registry-scoping rules; see
  // chipTasksFor for why a child tab must not show its parent's count.
  // "This tab is a subagent's own transcript." One definition, because the
  // parent link is what four separate call sites were each re-deriving.
  const isChildAgent = (agentId: string) => !!view.getAgentTab(agentId)?.parentAgentId
  const chipTasksForTab = (agentId: string) =>
    chipTasksFor(agentId, bgTasksFor(agentId), isChildAgent(agentId))
  // What the THINKING INDICATOR reads, which is not what the chip counts.
  //
  // The registry is keyed by ROOT owner, so counting it whole answers "is any
  // subagent of this root still running" -- right for a root tab, wrong for a
  // child: it kept a FINISHED subagent's indicator spinning for as long as any
  // SIBLING subagent ran. A child instead reads only its OWN row, and that row
  // can say `finished`, which no count can express: a child whose row ended is
  // done, whatever its transcript's last message looks like.
  const indicatorWorkState = (agentId: string): TabWorkState =>
    isChildAgent(agentId)
      ? subagentWorkState(agentId, bgTasksFor(agentId))
      : rootWorkState(bgTasksFor(agentId))
  const agentThinking = (agentId: string) => shouldShowThinkingIndicator(
    agentTabToInfo(view.getAgentTab(agentId)),
    agentSessionStore.getInfo(agentId),
    chatStore.getMessages(agentId),
    chatStore.streamingText.get(agentId),
    controlStore.getRequests(agentId).length,
    indicatorWorkState(agentId),
  )
  // Todos are owned by the root agent (the child has no independent todo list).
  // Resolve to the root so a child tab shows the root's todos, mirroring
  // background tasks. The root entry in the watch plan delivers the live updates.
  const todosFor = (agentId: string) => chatStore.todos.get(bgRootFor(agentId))
  const onOpenBackgroundTask = opts.onOpenBackgroundTask

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
        tabs={view.forTile(tileId)}
        activeTabKey={selection.activeKeyForTile(tileId)}
        readOnly={isActiveWorkspaceArchived()}
        closingTabKeys={closingTabKeys()}
        isEditingRef={(fn) => { setIsTabEditing(fn) }}
        onSelect={(tab) => {
          focusTile(tileId)
          handleTabSelect(tab)
        }}
        onClose={handleTabClose}
        onRename={(tab, title) => {
          // Clearing ptyTitle lets a manual rename stick: TitleChanged only
          // patches ptyTitle, and tabDisplayLabel prefers title.
          metadata.patch(tab.id, tab.type === TabType.TERMINAL
            ? { title, ptyTitle: '' }
            : { title })
          if (tab.type === TabType.AGENT) {
            const renameWorkerId = view.getAgentTab(tab.id)?.workerId ?? ''
            workerRpc.renameAgent(renameWorkerId, { agentId: tab.id, title }).catch((err) => {
              showWarnToast('Failed to rename agent', err)
            })
          }
          else if (tab.type === TabType.TERMINAL) {
            const renameWorkerId = view.getTerminalTab(tab.id)?.workerId ?? ''
            workerRpc.updateTerminalTitle(renameWorkerId, { terminalId: tab.id, title }).catch((err) => {
              showWarnToast('Failed to rename terminal', err)
            })
          }
        }}
        newTab={{
          showAddButton: isActiveWorkspaceMutatable(),
          onNewAgent: agentOps.handleOpenAgent,
          onNewTerminal: termOps.handleOpenTerminal,
          onNewTerminalWithShell: termOps.handleOpenTerminalWithShell,
          onNewAgentAdvanced: () => newAgentDialog.open(),
          onNewTerminalAdvanced: () => newTerminalDialog.open(),
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
          getLastOffset={id => metadata.get(id)?.lastOffset}
          activeTerminalId={props.activeTerminalId}
          visible={props.visible}
          tileFocused={props.tileFocused}
          onInput={termOps.handleTerminalInput}
          onResize={termOps.handleTerminalResize}
          onContentReady={id => metadata.patch(id, { contentReady: true })}
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
    // discriminated wrappers) many times per render without re-resolving the
    // tile's active tab through the projection on each access.
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
      for (const t of view.forTile(tileId)) {
        if (t.type === TabType.AGENT)
          agent.push(t)
        else if (t.type === TabType.FILE)
          file.push(t)
        else if (t.type === TabType.TERMINAL)
          terminal.push(t)
      }
      return { agent, file, terminal }
    })
    // The panes below key their `<For>`s on TAB IDs, not on the `Tab` objects.
    //
    // A `Tab` is a JOIN result (see tabView), rebuilt whenever ANY field it derives
    // from `tabMetadata` changes -- the MRU stamp every click on a tile writes, a
    // title rename, a git badge refresh, an agent status flip, a notification badge.
    // `<For>` keys by item IDENTITY, so keying a pane on the object made each of
    // those tear the whole pane down and rebuild it: the chat transcript's DOM went
    // with it, taking the user's in-progress text selection, every lifted
    // per-message expand/collapse choice, and the reading position. Clicking to
    // select text was self-defeating -- the click's own MRU stamp destroyed the
    // selection it had just made.
    //
    // A pane's identity is its tab id and nothing else. Ids are strings, so the
    // `shallowEqualArrays` guard means the `<For>` sees a change only when a tab is
    // actually added, removed, or reordered; every other field is read reactively
    // INSIDE the row, which is where a change should land.
    const tileAgentTabIds = createStableKeys(() => tabsByType().agent, t => t.id)
    const tileFileTabIds = createStableKeys(() => tabsByType().file, t => t.id)
    const tileTerminals = () => tabsByType().terminal
    const agentScrollStates = new Map<string, () => SavedViewportScroll | undefined>()
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
        <For each={tileAgentTabIds()}>
          {(agentId) => {
            const agent = createMemo(() => view.getAgentTab(agentId))
            // The per-agent reactive lookups ChatView's renderers / entry cache / height
            // estimator consult. Built ONCE here (the <For> child runs once per agent),
            // so `props.lookups` is a stable object -- a fresh object per access would
            // allocate on every spanId lookup the entry cache makes.
            const lookups: ChatMessageLookups = {
              getToolUseParsedBySpanId: spanId => chatStore.getToolUseParsedBySpanId(agentId, spanId),
              getToolUseContentVersionBySpanId: spanId => chatStore.getToolUseContentVersionBySpanId(agentId, spanId),
              getToolUseRevisionBySpanId: spanId => chatStore.getToolUseRevisionBySpanId(agentId, spanId),
              getToolResultParsedBySpanId: spanId => chatStore.getToolResultParsedBySpanId(agentId, spanId),
              getToolResultContentVersionBySpanId: spanId => chatStore.getToolResultContentVersionBySpanId(agentId, spanId),
              getToolResultRevisionBySpanId: spanId => chatStore.getToolResultRevisionBySpanId(agentId, spanId),
              getCommandStreamBySpanId: spanId => chatStore.getCommandStream(agentId, spanId),
              hasRenderableCommandStreamBySpanId: spanId => chatStore.hasRenderableCommandStream(agentId, spanId),
              getMessageContentVersion: id => chatStore.getMessageContentVersion(id),
              getTodoById: taskId => chatStore.todos.getById(bgRootFor(agentId), taskId),
            }
            // The scroll-rail prop object, memoized like `lookups` above: getRailData does
            // firstServerSeq/lastServerSeq scans and the rail's per-frame memos read
            // props.rail.{minSeq,maxSeq,marks} several times per scroll frame -- a fresh
            // `{...getRailData(), previewFor, warmPreview}` per access would re-run those scans
            // and allocate an object + two closures each time. Memoizing recomputes only when
            // getRailData's reactive deps change (marks / window / live tail), and the preview
            // callbacks are built ONCE (stable references) rather than per read.
            const railPreviewFor = (seq: bigint) => getCachedMarkPreview(agentId, seq)
            const railWarmPreview = (seq: bigint) => {
              const workerId = agent()?.workerId
              if (!workerId)
                return
              warmMarkPreview(workerId, agentId, seq, {
                getLoadedMessageBySeq: chatStore.getLoadedMessageBySeq,
                fetchMessageBySeq: chatStore.fetchMessageBySeq,
              })
            }
            // One evaluation per change, and one stable array identity.
            //
            // `agentLifecycle` below is a plain object literal, so Solid treats
            // it as ONE reactive unit: every read re-evaluates every field,
            // including `thinkingTokens`, which streams many deltas per turn.
            // Calling this twice there walked the parent chain and re-filtered
            // the whole registry twice per delta, and each fresh array identity
            // defeated BackgroundTaskList's own sort-and-group memo.
            const chipTasks = createMemo(() => chipTasksForTab(agentId))

            const railProps = createMemo<ChatRailProps>(() => ({
              ...chatStore.getRailData(agentId),
              previewFor: railPreviewFor,
              warmPreview: railWarmPreview,
            }))
            onCleanup(() => {
              agentScrollStates.delete(agentId)
              agentScrollToBottoms.delete(agentId)
              chatHandlers.delete(agentId)
            })
            return (
              <div class={styles.tilePane} classList={{ [styles.tilePaneHidden]: agentTab()?.id !== agentId }}>
                <Show
                  when={agent()}
                  fallback={<div class={styles.placeholder}>Agent not found.</div>}
                >
                  <ChatView
                    agentId={agentId}
                    isChildTranscript={isChildAgent(agentId)}
                    messages={chatStore.getMessages(agentId)}
                    messageVersion={chatStore.getMessageVersion(agentId)}
                    streamingText={chatStore.streamingText.get(agentId)}
                    streamingType={agentSessionStore.getInfo(agentId).streamingType}
                    tabActive={agentTab()?.id === agentId}
                    messageErrors={chatStore.messageErrors()}
                    messagePendingLabels={chatStore.messagePendingLabels()}
                    onRetryMessage={messageId => agentOps.handleRetryMessage(agentId, messageId)}
                    onDeleteMessage={messageId => agentOps.handleDeleteMessage(agentId, messageId)}
                    workingDir={agent()?.workingDir}
                    homeDir={workerInfoStore.getHomeDir(agent()?.workerId ?? '')}
                    pagination={{
                      hasOlderMessages: chatStore.hasOlderMessages(agentId),
                      fetchingOlder: chatStore.isFetchingOlder(agentId),
                      onLoadOlderMessages: () => chatStore.loadOlderMessages(agent()?.workerId ?? '', agentId),
                      onTrimOldMessages: minKeep => chatStore.trimOldestToViewport(agentId, minKeep),
                      hasNewerMessages: chatStore.hasNewerMessages(agentId),
                      fetchingNewer: chatStore.isFetchingNewer(agentId),
                      atWindowCeiling: chatStore.atWindowCeiling(agentId),
                      onLoadNewerMessages: () => chatStore.loadNewerPage(agent()?.workerId ?? '', agentId),
                      onJumpToLatest: () => chatStore.jumpToLatestMessages(agent()?.workerId ?? '', agentId),
                      onJumpToOldest: () => chatStore.jumpToOldestMessages(agent()?.workerId ?? '', agentId),
                      onJumpToSeq: seq => chatStore.jumpToMessagesAroundSeq(agent()?.workerId ?? '', agentId, seq),
                    }}
                    rail={railProps()}
                    savedViewportScroll={chatStore.viewportScroll.get(agentId)}
                    onClearSavedViewportScroll={() => chatStore.viewportScroll.clear(agentId)}
                    // Unmount save (tile split/merge, workspace switch): keep the
                    // reading position for the remount's restoreOnMount. The store gates
                    // the write on the agent's chat window still being live, so an
                    // agent-close unmount (which reaps the store first) can't leak an
                    // entry back for a dead agent, while a workspace switch-away (which
                    // only scopes the tab out) still saves. See saveViewportScrollForRemount.
                    onSaveViewportScroll={state => chatStore.saveViewportScrollForRemount(agentId, state)}
                    onScrollApiReady={(api) => {
                      agentScrollStates.set(agentId, api.getScrollState)
                      agentScrollToBottoms.set(agentId, api.forceScrollToBottom)
                      chatHandlers.set(agentId, { pageScroll: api.pageScroll })
                      if (agentTab()?.id === agentId) {
                        getScrollStateRef.set(api.getScrollState)
                        forceScrollToBottomRef.set(api.forceScrollToBottom)
                      }
                    }}
                    lookups={lookups}
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
                    agentLifecycle={{
                      agentWorking: agentThinking(agentId),
                      thinkingTokens: agentSessionStore.getInfo(agentId).thinkingTokens,
                      agentStatus: agent()?.agentStatus,
                      startupError: agent()?.startupError,
                      startupMessage: agent()?.startupMessage,
                      providerLabel: agentProviderLabel(agent()?.agentProvider),
                      backgroundTaskCount: countActiveBackgroundTasks(chipTasks()),
                      backgroundTasks: chipTasks(),
                      onOpenSubagent: onOpenBackgroundTask,
                      todos: todosFor(agentId),
                    }}
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

        <For each={tileFileTabIds()}>
          {(fileTabId) => {
            // Resolved reactively from the id (see tileFileTabIds): the row must
            // survive a metadata change instead of remounting the viewer on one.
            const ft = createMemo(() => view.getFileTab(fileTabId))
            const filePath = () => ft()?.filePath ?? ''
            // MRU-agent-relative, deliberately UNLIKE `rootPath` below. This
            // string is typed into that agent's editor (`insertIntoMruAgentEditor`),
            // so the agent's own dir is the base that makes it resolvable there.
            const fileRelPath = () => {
              const ctx = mruAgentContext()
              return relativizePath(filePath(), ctx.workingDir, ctx.homeDir)
            }
            // The TAB's own dir, unlike `fileRelPath` above. This one reaches
            // FileActionsMenu's "Copy relative path", which is about the file the
            // tab is showing -- so it is relative to where that tab lives, not to
            // whichever agent the user happened to click last. Keying it on the
            // MRU agent also made the menu item a silent no-op in a workspace with
            // no agent at all: `getMruAgentContext` answers `''` there, and
            // `relativizePath` returns the absolute path unchanged for an empty
            // base. The `parentDirectory` fallback covers the CRDT-first mount,
            // where the dir is empty until the worker echo lands -- the same
            // fallback `getCurrentTabContext` already writes for FILE tabs.
            const fileRootPath = () => ft()?.workingDir || parentDirectory(filePath())
            const fileHomeDir = () => workerInfoStore.getHomeDir(ft()?.workerId ?? '')
            // Single lookup shared by `gitFileStatus` and
            // `hasStagedAndUnstaged` so both props read from one memo
            // cell instead of walking the file-status map on every
            // reactive tick.
            const gitEntry = createMemo(() => gitFileStatusStore?.getFileStatus(filePath()))
            const hasStagedAndUnstaged = createMemo(() => {
              const entry = gitEntry()
              if (!entry)
                return false
              return entry.stagedStatus !== GitFileStatusCode.UNSPECIFIED
                && entry.unstagedStatus !== GitFileStatusCode.UNSPECIFIED
            })
            return (
              <div class={styles.tilePane} classList={{ [styles.tilePaneHidden]: fileTab()?.id !== fileTabId }}>
                <FileViewer
                  workerId={ft()?.workerId ?? ''}
                  filePath={filePath()}
                  rootPath={fileRootPath()}
                  homeDir={fileHomeDir()}
                  displayMode={ft()?.displayMode}
                  onDisplayModeChange={mode => metadata.patch(fileTabId, { displayMode: mode })}
                  onQuote={isActiveWorkspaceArchived()
                    ? undefined
                    : (text, startLine, endLine) => {
                        if (startLine != null && endLine != null) {
                          insertIntoMruAgentEditor(mruEditorDeps, formatFileQuote(fileRelPath(), startLine, endLine, text))
                        }
                      }}
                  onMention={isActiveWorkspaceArchived()
                    ? undefined
                    : () => {
                        insertIntoMruAgentEditor(mruEditorDeps, formatFileMention(fileRelPath()), 'inline')
                      }}
                  fileViewMode={ft()?.fileViewMode}
                  fileDiffBase={ft()?.fileDiffBase}
                  gitFileStatus={gitEntry()}
                  hasStagedAndUnstaged={hasStagedAndUnstaged()}
                  onFileViewModeChange={mode => metadata.patch(fileTabId, { fileViewMode: mode })}
                  onFileDiffBaseChange={base => metadata.patch(fileTabId, { fileDiffBase: base })}
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
    // A child (subagent) tab whose provider cannot steer a subagent conversation
    // is a READ-ONLY transcript: the worker routes both a child message
    // (SendChildInput) and a child interrupt (InterruptChild) through the same
    // ChildSteerer, so a provider that implements neither can do neither. Roots
    // and steerable children stay fully interactive. isSteerableAgentTab
    // resolves acceptsMessages (backend-authoritative) with a
    // supportsSubagentSend fallback for optimistic state.
    //
    // One predicate feeds the composer gate, its hint, and the Interrupt button,
    // so the three cannot disagree about what this tab can do.
    const subagentReadOnly = () => {
      const t = view.getAgentTab(agentId())
      if (!t?.parentAgentId)
        return false
      return !isSteerableAgentTab(t)
    }
    // The composer's GitBranch chip: one call answers both "can these actions
    // run?" and "what ref do the dialogs get?", so an enabled menu item can
    // never resolve to nothing. `buildRef` is lazy — the guard is read on
    // every reactive tick, and building the ref walks the whole workspace.
    const branchAction = () => focusedBranchAction({
      tab: view.getAgentTab(agentId()),
      workspaceId: activeWorkspace()?.id ?? '',
      workspaceTabs: () => view.forWorkspace(activeWorkspace()?.id ?? ''),
      isWorkerKnownOnline: branchCallbacks?.isWorkerKnownOnline,
    })
    const branchDisabledReason = () => branchAction().disabledReason
    return (
      <AgentEditorPanel
        agentId={agentId()}
        agent={agentTabToInfo(view.getAgentTab(agentId()))}
        // eslint-disable-next-line solid/reactivity -- async event handler; reactive tracking isn't needed for user-invoked callbacks
        onSendMessage={async (content, fileAttachments?: FileAttachment[]) => {
          const id = focusedAgentId()
          if (!id)
            return
          forceScrollToBottomRef()?.()
          const sendAgent = view.getAgentTab(id)
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
            chatStore.pendingOutbound.enqueue(id, { localId, content, attachments: protoAttachments })
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
        disabledReason={subagentReadOnly() ? SUBAGENT_NO_MESSAGES_HINT : undefined}
        focusRef={(fn) => { focusEditorRef.set(fn) }}
        controlRequests={controlStore.getRequests(agentId())}
        onControlResponse={agentOps.handleControlResponse}
        onSettingChange={change => agentOps.handleAgentSettingChange(agentId(), change)}
        onPermissionModeChange={mode => agentOps.handlePermissionModeChange(agentId(), mode)}
        onInterrupt={() => agentOps.handleInterrupt(agentId())}
        canInterrupt={!subagentReadOnly()}
        settingsLoading={settingsLoading.loading()}
        agentSessionInfo={agentSessionStore.getInfo(agentId())}
        agentWorking={agentThinking(agentId())}
        onChangeBranch={() => {
          const build = branchAction().buildRef
          if (build)
            branchCallbacks?.onChangeBranch?.(build())
        }}
        onDeleteBranch={() => {
          const build = branchAction().buildRef
          if (build)
            branchCallbacks?.onDeleteBranch?.(build())
        }}
        branchDisabledReason={branchDisabledReason()}
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
            selection.setActive(tab)
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
            .reduce((n, id) => n + view.forTile(id).length, 0)}
        />
        <CloseFlowDialog
          flow={closeTileFlow}
          title="Close tile"
          testIdPrefix="close-tile"
          confirmLabel="Move tabs to neighbor"
          confirmTestIdSuffix="move"
          noun="tile"
          tabCount={ctx => view.forTile(ctx.tileId).length}
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
              n += view.forTile(t).length
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
