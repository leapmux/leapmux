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
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { AgentTab, FileOpenSource, FileTab, Tab } from '~/stores/tab.types'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import { batch, createEffect, createSignal } from 'solid-js'
import { isWorkerUnreachable } from '~/api/workerErrors'
import * as workerRpc from '~/api/workerRpc'
import { showInfoToast, showWarnToast } from '~/components/common/Toast'
import { awaitCloseResult, summarizeWorktreeCloses, warnWorktreeUnreachable, worktreeRemovalToast } from '~/components/shell/closeResultToast'
import { getTerminalInstance } from '~/components/terminal/TerminalView'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createUpdatableDialogState } from '~/hooks/createDialogState'
import { makeIdGenerator } from '~/lib/idGenerator'
import { basename } from '~/lib/paths'
import { MAX_BACKGROUND_CHAT_MESSAGES } from '~/stores/chat.store'
import { descendantAgentTabs, resolveOptimisticGitInfo, seedOptimisticRepoGit, tabKey } from '~/stores/tab.helpers'
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
  /**
   * Active workspace id, for the layout-side reads only:
   * `activeTabForWorkspace`, `ownerWorkspaceFor` and `view.forWorkspace`. No
   * worker RPC in this module takes a workspace id any more.
   */
  getActiveWorkspaceId: () => string | undefined
  /**
   * The hub's last-known liveness for a worker: `false` only when the worker
   * list positively reports it offline, `undefined` when the list has not
   * mentioned the id.
   *
   * Feeds [isWorkerUnreachable]'s transport arm, which decides whether to skip
   * the uncommitted-work dialog and retire a tab. That is a destructive
   * decision, so an unknown reading must NOT count as offline -- see the note
   * on that function.
   */
  /**
   * Tri-state liveness for the DESTRUCTIVE close path: `false` only when the
   * worker list positively reports the worker offline, `undefined` when it has
   * not mentioned it. Named for the state, not as an `is*` predicate, because the
   * sidebar carries a same-shaped but fail-OPEN `isWorkerKnownOnline` and the two
   * are structurally assignable -- so the names are what keep a fail-open boolean
   * from reaching the path that retires a tab.
   */
  workerOnlineState: (workerId: string) => boolean | undefined
  repoGitStore: ReturnType<typeof createRepoGitStore>
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

  const lastTabConfirmDialog = createUpdatableDialogState<LastTabConfirmState>()

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
   * revoke is keyed by tabId -- as every tab-close RPC now is, so it needs no
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
    // A tab in a workspace that is not the one on screen (DeleteBranchDialog
    // opened on another workspace's branch row) closes through the SAME ladder
    // as any other. Only the two trailing layout steps are skipped, because
    // migrateFocusAfterTabClose / removeEmptyFloatingWindowForTile operate on
    // the ACTIVE layout and this tab's tileId belongs to an inactive workspace.
    //
    // It used to take a separate branch that fired the worker RPC itself. That
    // existed only to pass the tab's own `workspaceId` into the call, which the
    // request no longer carries -- and the duplicate had already drifted into a
    // leak: it skipped the agent-side `clearAgent` / `clearAttachments` /
    // `chatStore.forgetAgent`, so closing an agent tab in an inactive workspace
    // stranded its loaded window, live tail, command streams and span index,
    // and it skipped `disposeTerminalInstance`, which is the last chance to
    // reclaim a terminal's pooled WebGL slot after a cross-workspace move.
    // The shared handlers resolve a cross-workspace tab fine on their own:
    // getAgentTab / getTerminalTab read `byKey()`, which spans every workspace.
    const closesInactiveWorkspace = ownerWorkspaceFor(tab) !== null

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
    if (!closesInactiveWorkspace) {
      migrateFocusAfterTabClose(tab.tileId)
      removeEmptyFloatingWindowForTile(tab.tileId)
    }
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
  const closeWorktreeTabs = async (tabs: readonly Tab[], action: WorktreeAction): Promise<WorktreeCloseSummary> => {
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
          .then(() => closeTabWithAction(tab, action))
          .catch((err) => {
            showWarnToast('Failed to close tab', err)
            return undefined
          }),
      ),
    )
    return summarizeWorktreeCloses(results)
  }

  /**
   * Close a whole branch group and report what happened to its worktree, as a
   * hand-off: it returns at once and the report lands when the closes settle.
   *
   * This lives here, and not in the dialog that asks for it, because the work
   * outlives that dialog. Each agent close stops a subprocess, which takes a
   * 3-second grace before the kill, and `git worktree remove` then deletes the
   * whole directory — seconds on a tree that holds node_modules. The dialog
   * dismisses immediately, so a promise chain rooted in its closure would
   * report against an owner that is already disposed.
   *
   * `trackedAtInspect` is the caller's inspect-time snapshot of whether
   * LeapMux tracked the worktree. worktreeRemovalToast ranks it BELOW every
   * definitive worker outcome, because it can go stale between inspect and
   * confirm.
   */
  const closeWorktreeTabsAndReport = (tabs: readonly Tab[], action: WorktreeAction, trackedAtInspect: boolean): void => {
    void closeWorktreeTabs(tabs, action)
      .then((summary) => {
        // A KEEP close asked for no removal, so there is no removal outcome to
        // report. Say what the user actually chose.
        if (action !== WorktreeAction.REMOVE) {
          showInfoToast('Tabs closed; worktree kept on disk')
          return
        }
        // Toast the REAL outcome. worktreeRemovalToast owns the precedence
        // (ground truth over the stale inspect-time snapshot); null means stay
        // silent because a FAILED close already warned.
        const message = worktreeRemovalToast(summary, trackedAtInspect)
        if (message)
          showInfoToast(message)
      })
      // closeWorktreeTabs guards each per-tab close and folds every failure
      // into its summary, so it should never reject. Keep the handler anyway:
      // a rejection here would otherwise be an unhandled rejection nobody sees.
      .catch(err => showWarnToast('Failed to close the branch group', err))
  }

  /**
   * Close every subagent tab below `tab`, deepest first.
   *
   * A child agent tab is a transcript its parent's provider feeds, and it owns
   * no process of its own -- so once the parent tab is gone there is nothing
   * that can add to it, and the sidebar promotes it to a top-level row claiming
   * a lineage the user can no longer see.
   *
   * LOCAL ONLY: no per-child CloseAgent RPC. A child close is UI-only on the
   * worker (`closeAgentTabCommon` returns before any teardown for a row with a
   * parent), and the ONE RPC the clicked tab fires already stamps `closed_at`
   * over the whole subtree, so a call per child would cost one round trip each
   * to do nothing. Each child still gets the full local ladder -- the store
   * reclamation `retireAgentTabLocally` owns, the tombstone, and the two layout
   * steps -- which is what an `emitRemoveTab` on its own would skip.
   *
   * Deepest first, so each tab goes before the one that placed it and the
   * parent's own close is the one that finds the tile empty.
   *
   * Called from the COMMIT phase only, so a cancelled worktree prompt leaves
   * the whole subtree open.
   */
  const closeSubagentTabsUnder = (tab: Tab) => {
    if (tab.type !== TabType.AGENT)
      return
    for (const child of descendantAgentTabs(view.all(), tab.id)) {
      // Isolated per child, for the same reason `closeWorktreeTabs` isolates
      // its own closes: these are synchronous store and layout mutations, and
      // this runs one statement before the CLICKED tab's close. An unguarded
      // throw here would skip that close entirely, leaving the tab the user
      // asked to close on screen with nothing to say why.
      try {
        agentOps.retireAgentTabLocally(child.id)
        emitRemoveTab(TabType.AGENT, child.id)
        migrateFocusAfterTabClose(child.tileId)
        removeEmptyFloatingWindowForTile(child.tileId)
      }
      catch (err) {
        showWarnToast('Failed to close subagent tab', err)
      }
    }
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
    // A subagent (child) tab close is UI-only: the worker treats CloseAgent on
    // a child as tab-close-only (no teardown, no closed_at), so skip the
    // inspect/worktree prompt entirely and commit KEEP. Transcript + registry
    // survive; the tab can be revived (Part 5b).
    if (tab.type === TabType.AGENT && (tab as AgentTab).parentAgentId) {
      addClosingTabKey(key)
      try {
        // Its own subagents go with it, for the same reason a root's do.
        closeSubagentTabsUnder(tab)
        await closeTabWithAction(tab, WorktreeAction.KEEP)
        return true
      }
      finally {
        removeClosingTabKey(key)
      }
    }
    addClosingTabKey(key)

    // Decide phase: the tab stays visible (with a spinner) while we
    // await the worker's last-tab inspection and, if needed, the user's
    // dialog choice. This is the only phase that awaits; the commit
    // phase below mutates stores synchronously and fires the worker
    // close + hub unregister RPCs as fire-and-forget.
    //
    // Unreachable-worker fallback: when the worker referenced by the tab no
    // longer exists, isn't reachable, or is merely OFFLINE, the inspection RPC
    // fails -- with a NotFound-class connect error from the hub leg, or a
    // transport ChannelError once the hub tears down the channels an offline
    // worker was carrying. Without the carve-out below the user gets a "Failed
    // to prepare tab close" toast and the tab stays put — there's no way to
    // clean up a stale row, and no way to close a tab while the machine that
    // owns it is asleep.
    // The CLI's `tab close` does the same fallback (`isWorkerUnreachable` in
    // cmd/preflight.go); keep the CONNECT-CODE halves of these two predicates in
    // sync (the channel leg is browser-only). Note the CLI's half additionally
    // depends on controlipc.relayError preserving the upstream connect code --
    // this side always talks to the hub directly, so it sees the real code
    // either way, which is why the CLI's copy could silently stop matching
    // while this one kept working.
    let worktreeAction: WorktreeAction = WorktreeAction.KEEP
    // Hoisted out of the try because the outcome report below needs it after
    // the block, and `status` is scoped inside.
    let trackedAtInspect = false
    try {
      const workerId = tab.workerId ?? ''
      const status = await workerRpc.inspectLastTabClose(workerId, { tabType: tab.type, tabId: tab.id })
      trackedAtInspect = Boolean(status.worktreeId)
      if (status.shouldPrompt) {
        const choice = await askLastTabConfirmation(workerId, tab.type, tab.id, status)
        if (choice === 'cancel') {
          return false
        }
        if (choice === 'schedule-delete') {
          worktreeAction = WorktreeAction.REMOVE
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
      if (!isWorkerUnreachable(err, opts.workerOnlineState(tab.workerId ?? ''))) {
        showWarnToast('Failed to prepare tab close', err)
        return false
      }
      // The worker list positively reports this worker offline. We can't ask
      // it about worktree state, so skip the dialog and fall through to
      // commit. The downstream worker RPCs (closeAgent / closeTerminal /
      // revokeFileTabPath) are already fire-and-forget — they'll fail the
      // same way, get caught, and just toast. The CRDT tombstone still runs
      // and removes the row; the worker's orphan reconciler stops the process
      // when it next learns the tab is gone.
      showInfoToast('Worker is unreachable; removing the tab without closing it.')
      // A REMOVE cannot be honoured with nothing to run `git worktree remove`,
      // so report the downgrade and pin KEEP. Both statements are guards: the
      // only route into this catch today is a rejected inspect, which is
      // reached before the prompt can raise the action above KEEP. Stating the
      // invariant here is what keeps that true if an await is ever added after
      // the prompt.
      //
      // Note KEEP is NOT what the worktree gets, and that asymmetry is why
      // the reap needs its own guard rather than trusting this pin. An ONLINE
      // KEEP close calls unregisterTab, dropping the worktree to zero links,
      // which ListOrphanCandidateWorktrees deliberately excludes -- so the
      // directory survives indefinitely. Here the choice never reaches the
      // worker, the tab's rows close on their own, and every worktree_tabs
      // link becomes a strand, which is exactly the shape the reconciler
      // reaps. Service.worktreeHoldsUnsavedWork is what stops that reap from
      // discarding uncommitted or unpushed work the user was never asked
      // about.
      warnWorktreeUnreachable(worktreeAction)
      worktreeAction = WorktreeAction.KEEP
    }
    finally {
      removeClosingTabKey(key)
    }

    // Commit phase: synchronous UI mutations first so the tab
    // disappears immediately, then fire-and-forget worker cleanup and
    // hub unregister. closeTabWithAction owns both halves for AGENT,
    // TERMINAL, and FILE (cross-workspace included), so handleTabClose
    // only has to forward the user's worktreeAction choice.
    //
    // The subagents go first so the parent's close is the LAST one, and
    // therefore the one that finds the tile empty: each close prunes an emptied
    // floating window, and a parent that went first would prune the window its
    // own children still sit in.
    closeSubagentTabsUnder(tab)
    // A REMOVE reports the worker's REAL verdict when the close lands, and not
    // an optimistic "Worktree will be removed" at click time. The removal is a
    // hand-off, so the promise the user's choice produced was the only place
    // that could ever say what happened to the worktree — and dropping it left
    // "still in use elsewhere" and a degrade-to-KEEP silently contradicting
    // what the user was told. The Delete branch dialog reports the identical
    // verdict through the identical mapper.
    //
    // Wrapped in Promise.resolve() for the same reason closeWorktreeTabs
    // wraps its own calls: closeTabWithAction runs synchronous store mutations
    // before it returns, so a throw there is not a rejection to catch. The
    // wrapper turns both shapes into one.
    const closing = Promise.resolve().then(() => closeTabWithAction(tab, worktreeAction))
    if (worktreeAction === WorktreeAction.REMOVE) {
      void closing
        .then((result) => {
          const message = worktreeRemovalToast(summarizeWorktreeCloses([result]), trackedAtInspect)
          if (message)
            showInfoToast(message)
        })
        .catch(err => showWarnToast('Failed to close tab', err))
    }
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
    // A file tab inherits the git context of the tab it was opened from, the
    // same way a terminal opened "here" does -- same helper, same guard that it
    // only seeds when the two resolve to the same directory. Without it the tab
    // renders ungrouped until the next git-status refresh reaches it, even
    // though the answer was on screen at the moment of the open.
    const gitSeed = resolveOptimisticGitInfo(activeTab(), { workingDir: ctx.workingDir })
    seedOptimisticRepoGit(opts.repoGitStore, activeTab(), {
      workerId: ctx.workerId,
      workingDir: ctx.workingDir,
    })
    const placedTileId = openTabInFocusedTile(
      { view, layoutStore, selection, metadata },
      { type: TabType.FILE, id: tabId, workerId: ctx.workerId },
      {
        ...gitSeed,
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
    // A refused placement added no tab. Registering the path anyway would
    // persist a worker-side row (and broadcast it to peers) for a tab id
    // no tree holds — a phantom the reconciler only sweeps an hour later.
    if (!placedTileId) {
      showWarnToast('Cannot open the file', new Error('The workspace is not ready for a new tab yet.'))
      return
    }

    // E2EE worker-side path registration. The hub never sees the path; the
    // worker persists `(user_id, tab_id, file_path, working_dir)` and emits
    // FileTabPathRegistered on its OWN private-event stream so peer clients
    // learn the path and the resolved working dir.
    //
    // `workingDir` is what makes the worker able to answer branch-context
    // questions about this tab at all -- the last-tab close inspection, the
    // sibling-on-this-branch scan, PushBranch. It is the originating tab's dir,
    // the same value seeded onto the tab above, so the two sides group this tab
    // identically instead of the worker re-deriving one from the file path.
    //
    // Unconditional. This used to be gated on an active workspace id, left over
    // from when the request carried one -- a guard on a value the call no longer
    // sends, whose only effect would have been to skip the registration and
    // leave the tab permanently unresolvable to peers.
    //
    // Fire-and-forget — failure here doesn't unmount the locally-added tab; the
    // user can retry by re-opening.
    workerRpc.registerFileTabPath(ctx.workerId, {
      tabId,
      filePath: path,
      workingDir: ctx.workingDir,
    }).catch(() => {
      // Roll back the optimistic add so the user sees the failure surface (and
      // isn't left with a tab whose path peers can't resolve).
      emitRemoveTab(TabType.FILE, tabId)
    })
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
    closeWorktreeTabsAndReport,
    handleFileOpen,
    setIsTabEditing: (fn: () => boolean) => { isTabEditing = fn },
  }
}
