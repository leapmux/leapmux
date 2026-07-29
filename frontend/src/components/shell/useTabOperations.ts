import type { TabContext } from './tabContext'
import type { useAgentOperations } from './useAgentOperations'
import type { useTerminalOperations } from './useTerminalOperations'
import type { WorktreeCloseSummary } from '~/components/shell/closeResultToast'
import type { LastTabCloseChoice, LastTabConfirmState } from '~/components/shell/LastTabCloseDialog'
import type { CloseTabResult } from '~/generated/leapmux/v1/common_pb'
import type { InspectLastTabCloseResponse } from '~/generated/leapmux/v1/git_pb'
import type { createChatStore } from '~/stores/chat.store'
import type { SavedViewportScroll } from '~/stores/chatTypes'
import type { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { FileOpenSource, FileTab, Tab } from '~/stores/tab.types'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import { batch, createEffect, createSignal } from 'solid-js'
import { isWorkerUnreachable } from '~/api/workerErrors'
import * as workerRpc from '~/api/workerRpc'
import { showInfoToast, showWarnToast } from '~/components/common/Toast'
import { awaitCloseResult, warnWorktreeUnreachable } from '~/components/shell/closeResultToast'
import { getTerminalInstance } from '~/components/terminal/TerminalView'
import { WorktreeAction, WorktreeRemovalOutcome } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createDialogState } from '~/hooks/createDialogState'
import { makeIdGenerator } from '~/lib/idGenerator'
import { basename } from '~/lib/paths'
import { MAX_BACKGROUND_CHAT_MESSAGES } from '~/stores/chat.store'
import { tabKey } from '~/stores/tab.helpers'
import { emitRemoveTab } from '~/stores/tabOps'
import { openTabInFocusedTile } from './openTabInFocusedTile'
import { focusTile, removeEmptyFloatingWindow } from './tileLifecycle'

interface UseTabOperationsOpts {
  view: TabView
  metadata: TabMetadataStore
  selection: TabSelectionStore
  chatStore: ReturnType<typeof createChatStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  floatingWindowStore?: ReturnType<typeof createFloatingWindowStore>
  agentOps: ReturnType<typeof useAgentOperations>
  termOps: ReturnType<typeof useTerminalOperations>
  activeTab: () => Tab | undefined
  getCurrentTabContext: () => TabContext
  focusEditor: () => void
  getScrollState: () => SavedViewportScroll | undefined
  setFileTreePath: (path: string) => void
  /** Active workspace id used for file-tab E2EE worker RPCs. */
  getActiveWorkspaceId: () => string | undefined
}

export function useTabOperations(opts: UseTabOperationsOpts) {
  const {
    view,
    metadata,
    selection,
    chatStore,
    layoutStore,
    floatingWindowStore,
    agentOps,
    termOps,
    activeTab,
    getCurrentTabContext,
    focusEditor,
    getScrollState,
    setFileTreePath,
    getActiveWorkspaceId,
  } = opts

  const [closingTabKeys, setClosingTabKeys] = createSignal<Set<string>>(new Set())

  const lastTabConfirmDialog = createDialogState<LastTabConfirmState>()

  let isTabEditing: () => boolean = () => false

  const addClosingTabKey = (key: string) =>
    setClosingTabKeys(prev => new Set([...prev, key]))
  const removeClosingTabKey = (key: string) =>
    setClosingTabKeys((prev) => {
      const next = new Set(prev)
      next.delete(key)
      return next
    })

  const handleTabSelect = (tab: Tab) => {
    // Read scroll state before any store updates so the DOM measurement
    // happens while the previous tab is still visible. "Active agent"
    // is now derived: if the previously-active tab was an AGENT, use
    // its id.
    const prevTab = activeTab()
    const prevAgentId = prevTab?.type === TabType.AGENT ? prevTab.id : null
    const scrollState = prevAgentId ? getScrollState() : undefined

    // Batch the scroll-save and tab-switch store updates so that
    // SolidJS defers effects until both are applied.  Without this,
    // the savedViewportScroll effect fires while the old tab is still
    // visible, schedules a rAF that clears the saved state, and by the
    // time the user switches back the saved state is gone.
    batch(() => {
      if (prevAgentId) {
        // The previous tab is still visible here, so getScrollState() returning
        // undefined means there is genuinely nothing to restore (the list ref is
        // gone or the pane has zero height). An all-hidden window scrolled away
        // from the bottom returns a raw-scrollTop fallback instead, which we save.
        // Clear any stale save from a prior visit rather than leaving it to
        // restore the wrong position -- viewportScroll.set only writes, never clears.
        if (scrollState !== undefined)
          chatStore.viewportScroll.set(prevAgentId, scrollState)
        else
          chatStore.viewportScroll.clear(prevAgentId)
      }
      selection.setActive(tab)
      // Focus follows the selection, and must: `AppShell.activeTab` reads
      // `activeTabForTile(focusedTileId())`, so selecting a tab WITHOUT
      // focusing its tile leaves the editor pane, `getCurrentTabContext`, the
      // git-status gate and every tab shortcut operating on a different tab
      // than the one the user just clicked — while that tab renders as active
      // in its own strip. The sidebar's cross-workspace click is the sharpest
      // case: it selects a tab in a workspace that is not on screen yet, where
      // nothing else would ever set focus.
      //
      // The workspace is passed explicitly because the tile may belong to a
      // workspace the user is not looking at yet.
      if (tab.tileId)
        focusTile(layoutStore, floatingWindowStore, tab.tileId, tab.workspaceId)
    })

    // When switching tabs within the same tile, the previous agent becomes
    // hidden immediately. Trim it now instead of waiting for future messages
    // or for the visible ChatView's bottom-sticky path to run.
    if (
      prevAgentId
      && prevTab?.type === TabType.AGENT
      && prevTab.id !== tab.id
      && prevTab.tileId
      && prevTab.tileId === tab.tileId
      && chatStore.getMessages(prevAgentId).length > MAX_BACKGROUND_CHAT_MESSAGES
    ) {
      chatStore.trimOldestEnd(prevAgentId, MAX_BACKGROUND_CHAT_MESSAGES)
    }

    if (tab.type === TabType.AGENT) {
      requestAnimationFrame(() => {
        if (isTabEditing())
          return
        focusEditor()
      })
    }
    else if (tab.type === TabType.TERMINAL) {
      requestAnimationFrame(() => {
        if (isTabEditing())
          return
        const instance = getTerminalInstance(tab.id)
        instance?.terminal.focus()
      })
    }
  }

  const askLastTabConfirmation = (workerId: string, tabType: TabType, tabId: string, status: InspectLastTabCloseResponse): Promise<LastTabCloseChoice> => {
    return new Promise((resolve) => {
      lastTabConfirmDialog.open({ ...status, workerId, tabId, tabType, resolve })
    })
  }

  const removeEmptyFloatingWindowForTile = (tileId: string | undefined) =>
    removeEmptyFloatingWindow(layoutStore, floatingWindowStore, view, tileId)

  // After a tab close empties the focused tile, follow the surviving
  // active tab to its tile. Leaving focus on the now-empty tile would
  // leave the user looking at an EmptyTilePlaceholder while the work
  // they were doing lives on another tile. Mirrors the cross-tile drag
  // focus-follows-tab UX.
  //
  // Nothing promotes a successor at CLOSE time — a close is a tombstone, not
  // a store call. `activeTabForWorkspace` below is what does the work: the
  // pointer it reads is healed on READ against the projection, falling through
  // to the MRU head once the closed tab stops resolving.
  const migrateFocusAfterTabClose = (sourceTileId: string | undefined) => {
    if (!sourceTileId)
      return
    if (layoutStore.focusedTileId() !== sourceTileId)
      return
    if (view.forTile(sourceTileId).length > 0)
      return
    const active = selection.activeTabForWorkspace(getActiveWorkspaceId() ?? '')
    if (active?.tileId && active.tileId !== sourceTileId)
      layoutStore.setFocusedTile(active.tileId)
  }

  /**
   * Identify the workspace that owns `tab` for a cross-workspace
   * close (sidebar middle-click on a tab in workspace B while the
   * UI is on workspace A). Returns null when the tab belongs to the
   * active workspace.
   *
   * The tab carries the workspace the projection resolved it to, so this is a
   * field read. It used to be a search through registry snapshots, because a
   * tab in a non-active workspace existed only there and the active workspace's
   * store could not answer for it.
   */
  const ownerWorkspaceFor = (tab: Tab): string | null => {
    return tab.workspaceId && tab.workspaceId !== getActiveWorkspaceId() ? tab.workspaceId : null
  }

  /**
   * Close a FILE tab with a pre-determined worktree action. Mirrors
   * the shape of `agentOps.handleAgentClose` / `termOps.handleTerminalClose`
   * so the three tab types follow the same pattern (sync local
   * cleanup + fire-and-forget worker RPC + toast on failure). The
   * worker drives the unified closeTabCommon flow on its side; the
   * revoke is keyed by tabId, so unlike closeTerminal it needs no
   * workspaceId.
   */
  const handleFileClose = (tabId: string, workerId: string, worktreeAction: WorktreeAction): Promise<CloseTabResult | undefined> => {
    if (!workerId) {
      // No worker to send the revoke to. A REMOVE therefore can't
      // reach the worktree — surface it rather than letting the caller
      // assume removal happened.
      warnWorktreeUnreachable(worktreeAction)
      return Promise.resolve(undefined)
    }
    return awaitCloseResult(workerRpc.revokeFileTabPath(workerId, { tabId, worktreeAction }), 'Failed to close file')
  }

  /**
   * Close an agent / terminal / file tab with a pre-determined worktree
   * action, skipping the inspect+confirm prompt that handleTabClose
   * runs. Used by the delete-branch flow where the user has already
   * chosen the worktree fate for the whole branch group, so re-prompting
   * per tab would be wrong UX. Also runs the focus migration +
   * floating-window cleanup that an ad-hoc dispatch from the dialog
   * would otherwise miss.
   *
   * Intentionally does NOT add to `closingTabKeys`: handleTabClose
   * removes the key in its `finally{}` right before calling this for
   * its commit phase, so adding here would leave the key set forever
   * for the normal close flow. The sidebar X button concurrent-click
   * window is bounded by handleAgentClose / handleTerminalClose /
   * revokeFileTabPath's own per-tab dedup on the worker side
   * (idempotent close).
   */
  const closeTabWithAction = (tab: Tab, worktreeAction: WorktreeAction): Promise<CloseTabResult | undefined> => {
    // Cross-workspace branch: the tab belongs to a workspace that isn't the
    // one on screen (DeleteBranchDialog opened on another workspace's branch
    // row). The tab itself is perfectly visible -- every workspace is in the
    // one projection -- but the agent/terminal helpers below drive the ACTIVE
    // workspace's session and worker context, so the close RPC has to be sent
    // directly against the tab's own workerId and workspace.
    const crossWorkspaceWsId = ownerWorkspaceFor(tab)
    if (crossWorkspaceWsId) {
      const workerId = tab.workerId ?? ''
      let closeResult: Promise<CloseTabResult | undefined> = Promise.resolve(undefined)
      if (workerId) {
        if (tab.type === TabType.AGENT) {
          closeResult = awaitCloseResult(workerRpc.closeAgent(workerId, { agentId: tab.id, worktreeAction }), 'Failed to close agent')
        }
        else if (tab.type === TabType.TERMINAL) {
          closeResult = awaitCloseResult(
            workerRpc.closeTerminal(workerId, {
              workspaceId: crossWorkspaceWsId,
              terminalId: tab.id,
              worktreeAction,
            }),
            'Failed to close terminal',
          )
        }
        else if (tab.type === TabType.FILE) {
          closeResult = handleFileClose(tab.id, workerId, worktreeAction)
        }
      }
      else {
        // No worker id on the snapshot tab, so the close RPC can't fire
        // and a REMOVE can't reach the worktree. Don't drop it silently.
        warnWorktreeUnreachable(worktreeAction)
      }
      // One tombstone, wherever the tab lives. The projection drops it from
      // every view — this client's sidebar included — and from peer clients,
      // with no second write to keep some other representation in step.
      emitRemoveTab(tab.type, tab.id)
      // Skip migrateFocusAfterTabClose / removeEmptyFloatingWindowForTile
      // — those operate on the ACTIVE layout, and the closed tab's
      // tileId belongs to the inactive workspace.
      return closeResult
    }

    let closeResult: Promise<CloseTabResult | undefined>
    if (tab.type === TabType.AGENT) {
      closeResult = agentOps.handleAgentClose(tab.id, worktreeAction)
    }
    else if (tab.type === TabType.TERMINAL) {
      closeResult = termOps.handleTerminalClose(tab.id, worktreeAction)
    }
    else if (tab.type === TabType.FILE) {
      // Mirrors handleAgentClose / handleTerminalClose: sync local
      // cleanup first so the tab disappears immediately, then the
      // fire-and-forget worker RPC. The worker drives closeTabCommon
      // server-side, so worktreeAction REMOVE actually removes the
      // worktree from disk once no other tabs reference it — matching
      // the AGENT / TERMINAL last-close behavior.
      emitRemoveTab(tab.type, tab.id)
      if (tab.workerId) {
        closeResult = handleFileClose(tab.id, tab.workerId, worktreeAction)
      }
      else {
        warnWorktreeUnreachable(worktreeAction)
        closeResult = Promise.resolve(undefined)
      }
    }
    else {
      return Promise.resolve(undefined)
    }
    migrateFocusAfterTabClose(tab.tileId)
    removeEmptyFloatingWindowForTile(tab.tileId)
    return closeResult
  }

  /**
   * Close every tab in a worktree branch group with WorktreeAction.REMOVE
   * and report what actually happened to the worktree. Drives the
   * DeleteBranchDialog worktree path: it fires all the per-tab closes
   * (sync local cleanup happens immediately), awaits their results, and
   * folds the per-close WorktreeRemovalOutcome into one summary so the
   * dialog can toast the truth instead of optimistically assuming
   * removal.
   *
   * Per-tab close failures already surface their own warn toast via
   * `toastCloseFailure` inside the close helpers (that's the worktree
   * path + stderr the user needs for manual cleanup); this returns the
   * aggregate so the caller only adds the POSITIVE outcome message.
   */
  const closeWorktreeTabs = async (tabs: readonly Tab[]): Promise<WorktreeCloseSummary> => {
    const results = await Promise.all(
      // Isolate each per-tab close. closeTabWithAction runs synchronous
      // store mutations (removeTab, focus migration, floating-window prune)
      // before returning its promise, so a throw there — not just an RPC
      // rejection, which the close helpers already swallow — would reject
      // the whole Promise.all and discard the other tabs' (and the
      // worktree-removal) outcomes, surfacing a misleading "Delete failed".
      // Guard each so one tab's failure can't abort the group.
      tabs.map(tab =>
        Promise.resolve()
          .then(() => closeTabWithAction(tab, WorktreeAction.REMOVE))
          .catch((err) => {
            showWarnToast('Failed to close tab', err)
            return undefined
          }),
      ),
    )
    let removed = false
    let failed = false
    let stillReferenced = false
    let unknown = false
    for (const result of results) {
      if (!result) {
        // No definitive outcome for this tab: the close RPC was rejected, the
        // worker was unreachable, or the local close threw (each already
        // warn-toasted its own detail). A worker-reported outcome is always a
        // CloseTabResult — even a degraded-to-KEEP close returns one with
        // UNSPECIFIED — so a missing result genuinely means "we don't know".
        // Record it so the dialog reports "couldn't confirm" rather than a
        // clean "not removed".
        unknown = true
        continue
      }
      switch (result.worktreeRemoval) {
        case WorktreeRemovalOutcome.REMOVED:
          removed = true
          break
        case WorktreeRemovalOutcome.FAILED:
          failed = true
          break
        case WorktreeRemovalOutcome.STILL_REFERENCED:
          stillReferenced = true
          break
      }
    }
    return { removed, failed, stillReferenced, unknown }
  }

  /**
   * Close a tab. Returns true on success, false if the user cancelled the
   * last-tab/worktree confirmation prompt or an error aborted the close.
   * Auto-removes the parent floating window if this close empties it.
   *
   * The same flow applies to AGENT, TERMINAL, and FILE tabs: ask the
   * worker via inspectLastTabClose whether closing this tab would
   * empty its worktree (or its non-worktree branch with pending git
   * state), surface the confirmation dialog when needed, and dispatch
   * to closeTabWithAction with the user-chosen WorktreeAction. The
   * worker mirrors the symmetry server-side via closeTabCommon, so a
   * FILE-only worktree close goes through the same `git worktree
   * remove` pipeline as an AGENT- or TERMINAL-only one.
   */
  const handleTabClose = async (tab: Tab): Promise<boolean> => {
    const key = tabKey(tab)
    if (closingTabKeys().has(key))
      return false
    addClosingTabKey(key)

    // Decide phase: the tab stays visible (with a spinner) while we
    // await the worker's last-tab inspection and, if needed, the user's
    // dialog choice. This is the only phase that awaits; the commit
    // phase below mutates stores synchronously and fires the worker
    // close + hub unregister RPCs as fire-and-forget.
    //
    // Orphan-worker fallback: when the worker referenced by the tab
    // no longer exists / isn't reachable, the inspection RPC fails
    // with a NotFound-class connect error. Without the carve-out
    // below the user gets a "Failed to prepare tab close" toast and
    // the tab stays put — there's no way to clean up a stale row.
    // The CLI's `tab close` does the same fallback (`isWorkerUnreachable` in
    // cmd/preflight.go); keep these two predicates in sync. Note the CLI's half
    // additionally depends on remoteipc.relayError preserving the upstream
    // connect code -- this side always talks to the hub directly, so it sees
    // the real code either way, which is why the CLI's copy could silently
    // stop matching while this one kept working.
    let worktreeAction: WorktreeAction = WorktreeAction.KEEP
    try {
      const workerId = tab.workerId ?? ''
      const status = await workerRpc.inspectLastTabClose(workerId, { tabType: tab.type, tabId: tab.id })
      if (status.shouldPrompt) {
        const choice = await askLastTabConfirmation(workerId, tab.type, tab.id, status)
        if (choice === 'cancel') {
          return false
        }
        if (choice === 'schedule-delete') {
          worktreeAction = WorktreeAction.REMOVE
          showInfoToast('Worktree will be removed')
        }
      }
      else if (status.errorHint) {
        // The worker let the close proceed without a prompt only because
        // git was unavailable (worktree dir gone, transient git failure,
        // corrupt repo) — see InspectLastTabCloseResponse.error_hint. The
        // close still wins (we fall through to commit), but warn the user
        // that the usual uncommitted/unpushed-work check was skipped, so a
        // broken repo doesn't silently swallow the safety dialog. The hint
        // IS the user-facing message (a complete sentence), so it goes in
        // the toast directly rather than as an error attachment.
        showWarnToast(status.errorHint)
      }
    }
    catch (err) {
      if (!isWorkerUnreachable(err)) {
        showWarnToast('Failed to prepare tab close', err)
        return false
      }
      // Worker is gone for an existence/auth reason. We can't ask
      // it about worktree state, so skip the dialog and fall
      // through to commit. The downstream worker RPCs (closeAgent /
      // closeTerminal / revokeFileTabPath) are already fire-and-forget
      // — they'll fail with the same code, get caught, and just toast.
      // The CRDT tombstone still runs and removes the orphan row.
      showInfoToast('Worker is unreachable; removing the tab without closing it.')
    }
    finally {
      removeClosingTabKey(key)
    }

    // Commit phase: synchronous UI mutations first so the tab
    // disappears immediately, then fire-and-forget worker cleanup and
    // hub unregister. closeTabWithAction owns both halves for AGENT,
    // TERMINAL, and FILE (cross-workspace included), so handleTabClose
    // only has to forward the user's worktreeAction choice.
    closeTabWithAction(tab, worktreeAction)
    return true
  }

  const generateFileTabId = makeIdGenerator('file')
  const handleFileOpen = (path: string, openSource?: FileOpenSource) => {
    const ctx = getCurrentTabContext()
    if (!ctx.workerId)
      return

    const existingTab = view.forWorkspace(getActiveWorkspaceId() ?? '').find(
      t => t.type === TabType.FILE && t.filePath === path && t.workerId === ctx.workerId,
    )
    if (existingTab) {
      selection.setActive(existingTab)
      // Focus follows, for the same reason `handleTabSelect` does it: without
      // it, the editor pane, `getCurrentTabContext` and the git-status gate all
      // keep answering for the FOCUSED tile's tab while the file the user just
      // clicked merely fronts in some other tile's strip. It also makes the two
      // branches of this function agree — the new-tab branch below goes through
      // `openTabInFocusedTile`, which always lands on the focused tile.
      if (existingTab.tileId)
        focusTile(layoutStore, floatingWindowStore, existingTab.tileId, existingTab.workspaceId)
      return
    }

    // Determine initial view mode based on open source.
    let fileViewMode: FileTab['fileViewMode'] = 'working'
    let fileDiffBase: FileTab['fileDiffBase']
    if (openSource === 'staged') {
      fileViewMode = 'unified-diff'
      fileDiffBase = 'head-vs-staged'
    }
    else if (openSource === 'changed' || openSource === 'unstaged') {
      fileViewMode = 'unified-diff'
      fileDiffBase = 'head-vs-working'
    }

    const fileName = basename(path) || path
    const tabId = generateFileTabId()
    openTabInFocusedTile(
      { view, layoutStore, selection, metadata },
      { type: TabType.FILE, id: tabId, workerId: ctx.workerId },
      {
        filePath: path,
        workingDir: ctx.workingDir,
        title: fileName,
        fileViewMode,
        fileDiffBase,
        fileOpenSource: openSource,
        // This path already knows everything the FILE hydrator would fetch, so
        // say so — the local open paths are required to (`SharedMeta.hydrated`).
        // Leaving it unset is why the hydrator carried a `!tab.filePath` clause,
        // and that clause is exactly the payload sniff the flag's own doc
        // forbids: any other writer of `filePath` can forge it.
        hydrated: true,
      },
    )

    // E2EE worker-side path registration. The hub never sees the
    // path; the worker persists `(tab_id, workspace_id, file_path)`
    // and emits FileTabPathRegistered on the workspace's private-event
    // stream so peer clients populate their local fileTabPaths cache.
    // Fire-and-forget — failure here doesn't unmount the locally-added
    // tab; the user can retry by re-opening.
    const wsId = getActiveWorkspaceId()
    if (wsId) {
      workerRpc.registerFileTabPath(ctx.workerId, {
        workspaceId: wsId,
        tabId,
        filePath: path,
      }).catch(() => {
        // Roll back the optimistic add so the user sees the failure
        // surface (and isn't left with a tab whose path peers can't
        // resolve).
        emitRemoveTab(TabType.FILE, tabId)
      })
    }
  }

  // Reset file tree selection when active tab changes
  createEffect(() => {
    const _tab = activeTab()
    const ctx = getCurrentTabContext()
    setFileTreePath(ctx.workingDir || '~')
  })

  return {
    closingTabKeys,
    lastTabConfirmDialog,
    handleTabSelect,
    handleTabClose,
    closeTabWithAction,
    closeWorktreeTabs,
    handleFileOpen,
    setIsTabEditing: (fn: () => boolean) => { isTabEditing = fn },
  }
}
