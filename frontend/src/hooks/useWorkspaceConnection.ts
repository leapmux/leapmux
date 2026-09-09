import type { CatchUpPhase } from './agentEvents'
import type { AgentEvent, TerminalEvent } from '~/generated/proto/leapmux/v1/workspace_pb'
import type { createLoadingSignal } from '~/hooks/createLoadingSignal'
import type { AgentActivityStore } from '~/stores/agentActivity.store'
import type { createAgentInputQueueStore } from '~/stores/agentInputQueue.store'
import type { createAgentSessionStore } from '~/stores/agentSession.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createControlStore } from '~/stores/control.store'
import type { QuakeTerminalStore } from '~/stores/quakeTerminal.store'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { AgentTab, Tab } from '~/stores/tab.types'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import { batch, createEffect, createMemo, createSignal, onCleanup, untrack } from 'solid-js'
import { showWarnToastUnlessDisconnected } from '~/components/common/Toast'
import { addTerminalInstanceReadyListener, getTerminalInstance } from '~/components/terminal/TerminalView'
import { AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { TerminalStatus } from '~/generated/proto/leapmux/v1/terminal_pb'
import { TabType, WatchMode } from '~/generated/proto/leapmux/v1/workspace_pb'
import { applyTerminalData, bufferHasVisibleContent } from '~/lib/terminal'
import { exceedsCatchUpGapLimit } from '~/stores/chatLiveTail'
import { parseTabKey } from '~/stores/tab.helpers'
import {
  clearPerTurnLiveState,
  handleActivityChanged,
  handleAgentMessage,
  handleAgentStatusChange,
  handleControlRequest,
  handleStreamChunk,
  handleStreamEnd,
} from './agentEvents'
import {
  applyTerminalStatusChange,
  handleTerminalBell,
  handleTerminalNotification,
  handleTerminalProgress,
  handleTerminalTitleChanged,
  markTerminalExited,
} from './terminalEvents'
import { useWatchEventsStreams } from './useWatchEventsStreams'
import { buildWatchPlans } from './watchPlan'

function warnChatHistoryLoadFailed(err: unknown): void {
  showWarnToastUnlessDisconnected('Failed to load chat history', err)
}

/**
 * Which tabs a worker going offline affects.
 *
 * Both cases filter on `workerId`, and that is the whole point: this walks
 * `view.all()` — every tab in the ACCOUNT, not one workspace — so a tab hosted
 * by any other worker is still perfectly connected.
 */
export interface PendingTerminalDataFrame {
  data: Uint8Array
  isSnapshot: boolean
  endOffset: bigint
}

/**
 * Per-terminal cap on buffered pre-mount TerminalData. A terminal whose xterm
 * never mounts (kept off-screen, a render bug, a STARTUP_FAILED pane) would
 * otherwise accumulate every live PTY delta for the session — unbounded memory
 * on a chatty TUI (a build log, htop). A later snapshot re-syncs from the
 * worker's ring, so dropping the oldest frames loses nothing the mount cannot
 * recover. The cap is generous: a normally-mounting terminal never reaches it.
 */
export const MAX_PENDING_TERMINAL_FRAMES = 256

/**
 * Queue TerminalData until an xterm instance mounts. Snapshots clear prior
 * deltas.
 *
 * Returns true when the cap evicted oldest frames — those bytes are lost to
 * this client, so the caller must flag the terminal for a full-snapshot
 * resubscribe (see `TerminalMeta.needsResync`).
 */
export function enqueuePendingTerminalData(
  pending: Map<string, PendingTerminalDataFrame[]>,
  terminalId: string,
  frame: PendingTerminalDataFrame,
): boolean {
  const queue = pending.get(terminalId) ?? []
  if (frame.isSnapshot)
    queue.length = 0
  queue.push(frame)
  // Bound memory for a terminal that never mounts: drop the oldest frames. A
  // snapshot clears the queue, so this only trims a long run of pure deltas.
  let evicted = false
  if (queue.length > MAX_PENDING_TERMINAL_FRAMES) {
    queue.splice(0, queue.length - MAX_PENDING_TERMINAL_FRAMES)
    evicted = true
  }
  pending.set(terminalId, queue)
  return evicted
}

/** Drop queued frames for a terminal that is gone (tab closed / re-placed). */
export function dropPendingTerminalData(pending: Map<string, PendingTerminalDataFrame[]>, terminalId: string): void {
  pending.delete(terminalId)
}

export function collectWorkerOfflineTargets(
  tabs: readonly Tab[],
  workerId: string,
): { terminals: Set<string>, agents: AgentTab[] } {
  const terminals = new Set<string>()
  const agents: AgentTab[] = []
  for (const tab of tabs) {
    if (tab.workerId !== workerId)
      continue
    if (tab.type === TabType.TERMINAL && tab.status === TerminalStatus.READY)
      terminals.add(tab.id)
    else if (tab.type === TabType.AGENT)
      agents.push(tab)
  }
  return { terminals, agents }
}

/**
 * Drop every live, unpersisted indicator an agent carries, because its worker
 * went offline.
 *
 * A dropped link ends the turn without a word: no result row, no turn-end
 * divider and no INACTIVE status change. Every OTHER site that reclaims this
 * state runs off one of those events, so this sweep is the only thing standing
 * between an outage and a chat that reads as busy for as long as it lasts --
 * streaming text half-written, a command stream mid-line, a thinking counter and
 * a running-tool badge frozen on their last value.
 *
 * Exported for its own test: the effect that calls it fires on an internal
 * offline signal a test cannot drive.
 */
export function clearOfflineAgentState(
  agentId: string,
  stores: {
    chatStore: ReturnType<typeof createChatStore>
    agentSessionStore: ReturnType<typeof createAgentSessionStore>
    agentActivityStore?: AgentActivityStore
  },
): void {
  // The worker that was going to report the settle is gone, so a retained busy
  // flag would pin the spinner and keep the Interrupt button on an agent that
  // nothing can interrupt.
  stores.agentActivityStore?.forget(agentId)
  stores.chatStore.streamingText.clear(agentId)
  for (const spanId of Object.keys(stores.chatStore.getAgentCommandStreams(agentId)))
    stores.chatStore.clearCommandStream(agentId, spanId)
  clearPerTurnLiveState(agentId, stores)
}

export function reconcileLaggingTails(deps: {
  agentTabs: () => ReadonlyArray<{ id: string, workerId: string }>
  hasNewerMessages: (agentId: string) => boolean
  caughtUpToLiveTail: (agentId: string) => boolean
  isTailFillDeferred: (agentId: string) => boolean
  getLastSeq: (agentId: string) => bigint
  getLiveTailSeq: (agentId: string) => bigint
  isFetchingNewer: (agentId: string) => boolean
  catchUpToTail: (workerId: string, agentId: string, afterSeq: bigint) => void
  resumeDeferredTailFill: (workerId: string, agentId: string) => void
  jumpToLatest: (workerId: string, agentId: string) => void
}): void {
  for (const tab of deps.agentTabs()) {
    if (!tab.workerId)
      continue
    const caughtUp = deps.caughtUpToLiveTail(tab.id)
    if (caughtUp)
      continue
    const lastSeq = deps.getLastSeq(tab.id)
    const atTail = !deps.hasNewerMessages(tab.id)
    const deferred = deps.isTailFillDeferred(tab.id)
    // Re-anchor when forward-fill cannot reach the tail: an empty window, or a
    // gap beyond the catch-up limit. The drain abandons a gap that large, so
    // the window re-anchors on the newest page. A deferred tail fill cannot
    // cross a gap that large either. A plain scrolled-away wall keeps its
    // window: the reader chose that gap, and scroll-down paging recovers it.
    if (lastSeq === 0n || (exceedsCatchUpGapLimit(deps.getLiveTailSeq(tab.id), lastSeq) && (atTail || deferred))) {
      if (!deps.isFetchingNewer(tab.id))
        deps.jumpToLatest(tab.workerId, tab.id)
      continue
    }
    if (atTail)
      deps.catchUpToTail(tab.workerId, tab.id, lastSeq)
    else if (deferred)
      deps.resumeDeferredTailFill(tab.workerId, tab.id)
  }
}

export interface WorkspaceConnectionParams {
  chatStore: ReturnType<typeof createChatStore>
  agentInputQueueStore: ReturnType<typeof createAgentInputQueueStore>
  view: TabView
  metadata: TabMetadataStore
  selection: TabSelectionStore
  controlStore: ReturnType<typeof createControlStore>
  agentSessionStore: ReturnType<typeof createAgentSessionStore>
  agentActivityStore: AgentActivityStore
  settingsLoading: ReturnType<typeof createLoadingSignal>
  repoGitStore: ReturnType<typeof createRepoGitStore>
  quakeStore: QuakeTerminalStore
  /**
   * The quake key the panel is CURRENTLY showing -- the focused tab's -- or
   * null when the focused tab names none.
   *
   * The shell's own accessor, passed in rather than re-derived, so "is this
   * shell on screen?" has one answer here and in `QuakeTerminalPanel`. See
   * `isQuakeEntryOnScreen`.
   */
  getActiveQuakeKeyId: () => string | null
  getActiveWorkspaceId: () => string | null
  /** Alert + badge when an agent SETTLES. See handleAgentSettled. */
  onAgentSettled?: (agentId: string, numToolUses?: number) => void
  /**
   * Refresh derived views after each TURN end -- git status and the directory
   * tree. Separate from onAgentSettled because the two fire at different moments: a
   * turn that leaves a subagent running changed the working tree but has not
   * settled the agent.
   */
  onTurnEndRefresh?: (agentId: string) => void
}

export function useWorkspaceConnection(params: WorkspaceConnectionParams) {
  const { chatStore, agentInputQueueStore, view, metadata, selection, controlStore, agentSessionStore, agentActivityStore, settingsLoading, repoGitStore } = params
  const [offlineWorkers, setOfflineWorkers] = createSignal<ReadonlySet<string>>(new Set())

  // Resume cursor sent per agent on promotion — CatchUpStart reap ceiling.
  const resumeTails = new Map<string, bigint>()
  // TerminalData that arrived before the xterm instance was mounted. Snapshot
  // frames clear prior deltas so we only replay what still matters.
  const pendingTerminalData = new Map<string, PendingTerminalDataFrame[]>()

  function flushPendingTerminalData(terminalId: string): void {
    const queued = pendingTerminalData.get(terminalId)
    if (!queued?.length)
      return
    const instance = getTerminalInstance(terminalId)
    if (!instance)
      return
    pendingTerminalData.delete(terminalId)
    const tab = view.getTerminalTab(terminalId)
    const checkContent = tab && !tab.contentReady
    let lastOffset = metadata.get(terminalId)?.lastOffset ?? 0
    for (const frame of queued) {
      const onParsed = () => {
        if (checkContent && bufferHasVisibleContent(instance.terminal))
          metadata.patch(terminalId, { contentReady: true })
      }
      lastOffset = applyTerminalData(instance, frame.isSnapshot
        ? { kind: 'snapshot', data: frame.data, endOffset: Number(frame.endOffset), onParsed }
        : { kind: 'delta', data: frame.data, endOffset: Number(frame.endOffset), currentOffset: lastOffset, onParsed })
      if (frame.isSnapshot)
        metadata.patch(terminalId, { needsResync: false })
    }
    metadata.patch(terminalId, { lastOffset })
  }

  onCleanup(addTerminalInstanceReadyListener((id) => {
    flushPendingTerminalData(id)
  }))

  const workerOnline = (workerId: string): boolean => {
    if (!workerId)
      return true
    return !offlineWorkers().has(workerId)
  }

  const setWorkerOnline = (workerId: string, online: boolean) => {
    // A removed tab resolves workerId to '' — seeding the offline set with the
    // empty key is pure churn (the reader treats '' as always-online, but the
    // sweep effect still iterates it as a no-op on every offline-set change).
    if (!workerId)
      return
    // The link came BACK. Release this worker's optimistic branch pins, keeping
    // the branch values: a pin says "a branch change succeeded, so ignore a
    // broadcast that still reports the old branch", and a dropped link ends
    // that claim. Only an agreeing refresh clears a pin otherwise, and a
    // background tab issues none -- so a stamp made just before the worker died
    // would label the tab for the rest of the page.
    if (online && untrack(offlineWorkers).has(workerId))
      repoGitStore.releaseBranchPinsForWorker(workerId)
    setOfflineWorkers((prev) => {
      const next = new Set(prev)
      if (online)
        next.delete(workerId)
      else
        next.add(workerId)
      return next
    })
  }

  /**
   * Whether one quake panel is on screen: it is open AND its directory is the
   * one the focused tab works in.
   *
   * This is the SAME question `QuakeTerminalPanel` answers to decide what to
   * render -- it shows the entry for `activeQuakeKeyId` and nothing else -- and
   * it reads the identical accessor rather than re-deriving it. A second
   * derivation would let the FULL/NOTIFY watch decision disagree with what the
   * user is looking at, which is how a visible shell stops receiving bytes.
   *
   * Declared above the watch-plan memo on purpose. `createMemo` runs its body
   * once at creation to collect dependencies, so a `const` declared below it
   * would be in its temporal dead zone and throw.
   */
  const isQuakeEntryOnScreen = (entry: { keyId: string, open: boolean }): boolean =>
    entry.open && params.getActiveQuakeKeyId() === entry.keyId

  const watchPlans = createMemo(() =>
    buildWatchPlans(
      params.view.all(),
      params.getActiveWorkspaceId(),
      tileId => selection.activeKeyForTile(tileId),
      {
        agentResumeSeq: agentId => untrack(() => chatStore.getResumeAfterSeq(agentId)),
        // Untracked like the resume seq: the loaded tail moves with every
        // message, and the plan must not re-send a watch update per arrival.
        // The worker reads it only for the capped-replay skip decision.
        agentWindowTailSeq: agentId => untrack(() => chatStore.getLastSeq(agentId)),
        terminalAfterOffset: terminalId => untrack(() => metadata.get(terminalId)?.lastOffset ?? 0),
        // Tracked on purpose: the plan must go out the moment a terminal is
        // flagged, and back out when the snapshot lands. Only this field is
        // tracked — lastOffset above moves at PTY-read frequency, and keying
        // the plan on it would re-send a watch update per output chunk.
        terminalNeedsResync: terminalId => metadata.get(terminalId)?.needsResync === true,
        getAgentTab: (agentId: string) => view.getAgentTab(agentId),
        // A quake terminal is FULL only while its panel is open AND its
        // directory is the focused tab's -- the same "does the user look at
        // it?" question a placed terminal answers through its tile. NOTIFY
        // otherwise, which keeps the cursor moving so a reopen catches up from
        // the worker's ring instead of paying for a cold snapshot, and keeps
        // the bell and the title flowing for a shell the user cannot see,
        // which is when those matter most.
        detachedTerminals: params.quakeStore.liveEntries().map(entry => ({
          terminalId: entry.terminalId,
          workerId: entry.workerId,
          mode: isQuakeEntryOnScreen(entry) ? WatchMode.FULL : WatchMode.NOTIFY,
        })),
      },
    ),
  )

  let abortSignalFor: (workerId: string) => AbortSignal | undefined = () => undefined

  const handleAgentEvent = (agentEvent: AgentEvent, streamWorkerId: string) => {
    const agentId = agentEvent.agentId
    const inner = agentEvent.event

    // The FRAME says which it is. A client registers its live watch before the
    // replay burst runs and both write the same
    // stream, so arrival order cannot tell them
    // apart. Guessing from it dropped a permission
    // prompt's badge, a plan-title update and an
    // INACTIVE sweep whenever they raced a replay. See AgentEvent.replay.
    const catchUpPhase: CatchUpPhase = agentEvent.replay ? 'catchingUp' : 'live'
    const markLiveAgentActive = () => {
      if (catchUpPhase !== 'live')
        return
      const wid = view.getAgentTab(agentId)?.workerId || streamWorkerId || ''
      if (wid)
        setWorkerOnline(wid, true)
      const current = view.getAgentTab(agentId)
      if (current?.agentStatus === AgentStatus.INACTIVE) {
        metadata.patch(agentId, { agentStatus: AgentStatus.ACTIVE })
      }
    }

    switch (inner.case) {
      case 'agentMessage':
        markLiveAgentActive()
        handleAgentMessage(
          agentId,
          inner.value,
          { agentSessionStore, chatStore, view, metadata, selection, getActiveWorkspaceId: params.getActiveWorkspaceId },
          catchUpPhase,
        )
        break
      case 'streamChunk':
        markLiveAgentActive()
        handleStreamChunk(agentId, inner.value, chatStore)
        break
      case 'streamEnd':
        markLiveAgentActive()
        handleStreamEnd(agentId, inner.value, { chatStore })
        break
      case 'statusChange':
        handleAgentStatusChange(
          agentId,
          inner.value,
          catchUpPhase,
          { agentSessionStore, chatStore, view, metadata, selection, getActiveWorkspaceId: params.getActiveWorkspaceId, controlStore, repoGitStore },
          settingsLoading,
          online => setWorkerOnline(view.getAgentTab(agentId)?.workerId || streamWorkerId || '', online),
          streamWorkerId,
        )
        break
      case 'controlRequest':
        markLiveAgentActive()
        handleControlRequest(
          agentId,
          inner.value,
          catchUpPhase,
          { agentSessionStore, chatStore, view, metadata, selection, getActiveWorkspaceId: params.getActiveWorkspaceId, controlStore },
        )
        break
      case 'controlCancel': {
        const cc = inner.value
        controlStore.removeRequest(cc.agentId, cc.requestId)
        break
      }
      case 'turnEnd':
        // A turn ended. That refreshes git status and the directory tree, which
        // is right per TURN -- the working tree changed whether or not a
        // subagent is still running.
        //
        // It does NOT alert. The alert belongs to the busy -> idle edge
        // (handleAgentSettled): a turn that leaves a subagent working is not a
        // finished piece of work, and ringing here told the user otherwise.
        params.onTurnEndRefresh?.(agentId)
        break
      case 'inputQueueChanged': {
        agentInputQueueStore.apply(inner.value.snapshot)
        break
      }
      case 'todosChanged': {
        const tc = inner.value
        chatStore.todos.replace(tc.agentId, tc.todos)
        break
      }
      case 'goalChanged': {
        // Keyed by the ROOT owner agent id like the registry above, and
        // notification-class for the same reason: an off-screen root tab must
        // still update its goal card and its section visibility.
        const gc = inner.value
        chatStore.goal.replace(gc.agentId, gc.goal, gc.supportedActions, gc.goalUpdatedAt)
        break
      }
      case 'backgroundTasksChanged': {
        // The registry is keyed by the ROOT owner agent id and rides the root
        // tab's existing WatchAgentEntry (notification-class, so an off-screen
        // root tab still updates the sidebar/badge).
        const bc = inner.value
        chatStore.backgroundTasks.replace(bc.agentId, bc.tasks)
        break
      }
      case 'activityChanged':
        // The Worker's authoritative "is this agent working". Notification-class
        // and edge-triggered, so an off-screen tab still learns that its agent
        // settled -- which is the tab that most needs to ring and badge.
        //
        // No `catchUpPhase` here, unlike every other alerting branch. Each of
        // these is a TRANSITION, so a settle that lands while this tab replays
        // is a live settle and rings. The catch-up BASELINE is a level and
        // arrives on catchUpStart below, where a level seeds the store. The
        // replay never sends this message at all, so the frame's own flag would
        // answer 'live' here in every case.
        handleActivityChanged(agentId, inner.value, {
          metadata,
          selection,
          view,
          getActiveWorkspaceId: params.getActiveWorkspaceId,
          agentActivityStore,
          onAgentSettled: params.onAgentSettled,
        })
        break
      case 'catchUpStart':
        chatStore.reconcileAuthoritativeTail(agentId, inner.value.latestSeq, resumeTails.get(agentId))
        // The activity level the replay opens with. It seeds the spinner ahead
        // of the message burst and rings nothing. It is the PUBLISHED level, so
        // it also resets the edge
        // baseline. This client may
        // hold a WORKING from a link
        // that dropped without an
        // offline sweep, and the
        // Worker's own answer is what
        // supersedes it. See AgentActivityStore.seedPublished.
        agentActivityStore.seedPublished(agentId, inner.value.activityState)
        break
      case 'catchUpComplete':
        chatStore.setCatchingUp(agentId, false)
        chatStore.reconcileAuthoritativeTail(
          agentId,
          inner.value.latestSeq,
          inner.value.startTailSeq === undefined
            ? resumeTails.get(agentId)
            : inner.value.startTailSeq,
          true,
        )
        void chatStore.loadMessageMarks(
          view.getAgentTab(agentId)?.workerId ?? '',
          agentId,
          abortSignalFor(view.getAgentTab(agentId)?.workerId ?? '') ?? undefined,
        )
        chatStore.sweepOrphanedBufferedSpans(agentId)
        break
    }
  }

  /**
   * Whether a quake terminal is on screen, by terminal id.
   *
   * The bell and notification helpers hold an id and nothing else, so this is
   * the id-keyed door onto the ONE rule above. Two copies of the rule would let
   * the FULL/NOTIFY watch decision and the "should this raise a desktop
   * notification?" decision disagree about the same shell.
   */
  const isQuakeTerminalOnScreen = (terminalId: string): boolean => {
    const keyId = params.quakeStore.keyOf(terminalId)
    if (keyId === undefined)
      return false
    const entry = params.quakeStore.entryFor(keyId)
    return entry !== undefined && isQuakeEntryOnScreen(entry)
  }

  const handleTerminalEvent = (termEvent: TerminalEvent, streamWorkerId: string) => {
    const terminalId = termEvent.terminalId

    switch (termEvent.event.case) {
      case 'data': {
        const { data, isSnapshot, endOffset } = termEvent.event.value
        // If the tab is gone, don't buffer for it — its terminalId left the
        // view (closed / re-placed), so nothing will ever mount to drain it.
        if (!view.getTerminalTab(terminalId)) {
          dropPendingTerminalData(pendingTerminalData, terminalId)
          break
        }
        const instance = getTerminalInstance(terminalId)
        if (!instance) {
          // Do not advance lastOffset until bytes land on a live instance —
          // otherwise a late mount would skip catch-up and leave a blank PTY.
          // An eviction marks the terminal for a full-snapshot resubscribe:
          // the dropped frames leave a hole no incremental delta can fill.
          const evicted = enqueuePendingTerminalData(pendingTerminalData, terminalId, { data, isSnapshot, endOffset })
          if (evicted)
            metadata.patch(terminalId, { needsResync: true })
          break
        }
        const tab = view.getTerminalTab(terminalId)
        const checkContent = tab && !tab.contentReady
        const onParsed = () => {
          if (checkContent && bufferHasVisibleContent(instance.terminal))
            metadata.patch(terminalId, { contentReady: true })
        }
        const newOffset = applyTerminalData(instance, isSnapshot
          ? { kind: 'snapshot', data, endOffset: Number(endOffset), onParsed }
          : { kind: 'delta', data, endOffset: Number(endOffset), currentOffset: metadata.get(terminalId)?.lastOffset ?? 0, onParsed })
        // An applied snapshot is itself the resync: the buffer was rebuilt
        // from the worker's ring, so no forced resubscribe is pending.
        metadata.patch(terminalId, { lastOffset: newOffset, ...(isSnapshot ? { needsResync: false } : {}) })
        break
      }
      case 'closed':
        // A quake terminal is TERMINATED by its shell exiting, not left in
        // an EXITED state that waits for Enter: there is no pane to leave behind,
        // and the next open spawns a fresh shell. Routed before
        // markTerminalExited so the panel never paints the exit notice.
        if (params.quakeStore.isQuakeTerminal(terminalId))
          params.quakeStore.handleShellExit(terminalId)
        else
          markTerminalExited(metadata, terminalId)
        // The PTY is gone; any buffered pre-mount bytes will never be written.
        dropPendingTerminalData(pendingTerminalData, terminalId)
        break
      case 'statusChange':
        applyTerminalStatusChange(
          metadata,
          repoGitStore,
          view.getTerminalTab(terminalId),
          terminalId,
          termEvent.event.value,
          streamWorkerId,
        )
        break
      case 'bell':
        handleTerminalBell(terminalId, {
          metadata,
          selection,
          getActiveWorkspaceId: params.getActiveWorkspaceId,
          view,
          isDetachedOnScreen: isQuakeTerminalOnScreen,
          detachedOwnerOf: params.quakeStore.badgeTabFor,
        })
        break
      case 'notification':
        handleTerminalNotification(terminalId, termEvent.event.value, {
          metadata,
          selection,
          getActiveWorkspaceId: params.getActiveWorkspaceId,
          view,
          isDetachedOnScreen: isQuakeTerminalOnScreen,
          detachedOwnerOf: params.quakeStore.badgeTabFor,
        })
        break
      case 'titleChanged':
        handleTerminalTitleChanged(terminalId, termEvent.event.value, metadata)
        break
      case 'progress':
        handleTerminalProgress(terminalId, termEvent.event.value, metadata)
        break
    }
  }

  const streams = useWatchEventsStreams({
    view,
    plans: watchPlans,
    onEvent: (workerId, resp) => {
      switch (resp.event.case) {
        case 'agentEvent':
          handleAgentEvent(resp.event.value, workerId)
          break
        case 'terminalEvent':
          handleTerminalEvent(resp.event.value, workerId)
          break
      }
    },
    onWorkerOnline: setWorkerOnline,
    onPromoted: (workerId, agentIds) => {
      for (const agentId of agentIds) {
        chatStore.setCatchingUp(agentId, true)
        const resumeSeq = untrack(() => chatStore.getResumeAfterSeq(agentId))
        resumeTails.set(agentId, resumeSeq)
        void chatStore.loadInitialMessages(workerId, agentId).catch(warnChatHistoryLoadFailed)
        void chatStore.loadMessageMarks(workerId, agentId, abortSignalFor(workerId))
      }
    },
  })
  abortSignalFor = streams.abortSignalFor

  // When a worker goes offline, mark its running terminals disconnected and
  // clear stale streaming state for its agents.
  //
  // This effect deliberately does NOT sweep the repo-keyed git store. An entry
  // there is the last known state of a working tree. It is not a claim about
  // the link. Every other worker-sourced field on the tab row survives an
  // outage the same way, and `RepoGitStore.refresh` keeps last-good state on
  // its own side.
  //
  // A sweep here removed the branch with no way back. `Tab.gitToplevel`
  // outlives the outage, and `branchKeys.repoKeyAndLabel` groups a tab by
  // that field alone. The branch label comes from this store. A dropped entry
  // therefore put every tab of that worker under its repo with no branch name.
  //
  // Three separate rules kept a BACKGROUND tab from recovering. An agent tab
  // stays `hydrated` for the life of the page, so `useTabHydrators` never
  // re-asks. The worker sends a git status only at that agent's own turn end.
  // The catch-up replay after a reconnect carries one only for an agent that
  // the client promotes to FULL. So the user clicked each tab, or the Files
  // refresh button, once per tab, after every dropped link.
  //
  // Permanent removal is a different event with its own caller. Worker
  // deregistration deliberately does NOT clear the repo store
  // (`useWorkerSection` states why: the entries should outlive the link), so
  // nothing here sweeps them either.
  //
  // Keeping an entry has its own cost, and `RepoGitState.nonRepoProbeIgnored`
  // pays it: a repo deleted during the outage would otherwise suppress every
  // later "not a git repository" answer.
  createEffect(() => {
    const offline = offlineWorkers()
    if (offline.size === 0)
      return
    untrack(() => {
      // `view.all()` holds PLACED tabs only, so a quake terminal is not in
      // it and would stay reading READY for the whole outage. Its tab object
      // comes from the detached family instead.
      //
      // Built ONCE, above the loop: neither list depends on the worker, and a
      // hub restart puts several workers in this set at the same tick.
      const searchable = [...params.view.all(), ...params.view.detachedTerminalTabs()]
      for (const workerId of offline) {
        const { terminals: affectedTerminals, agents } = collectWorkerOfflineTargets(searchable, workerId)
        batch(() => {
          if (affectedTerminals.size > 0) {
            params.metadata.patchMatching(
              (_meta, tabId) => affectedTerminals.has(tabId),
              { terminalStatus: TerminalStatus.DISCONNECTED },
            )
          }
          for (const tab of agents) {
            // The patch below writes the tab's status directly and never reaches
            // handleAgentInactive, so this sweep owns the whole reclamation.
            clearOfflineAgentState(tab.id, { chatStore, agentSessionStore, agentActivityStore })
            if (tab.agentStatus === AgentStatus.ACTIVE)
              metadata.patch(tab.id, { agentStatus: AgentStatus.INACTIVE })
          }
        })
      }
    })
  })

  // Lazy message loading for agent tabs promoted to FULL outside onPromoted
  // (e.g. user switches to an agent tab whose history was never loaded).
  createEffect(() => {
    const activeKey = selection.activeKeyForWorkspace(params.getActiveWorkspaceId() ?? '')
    if (!activeKey)
      return
    const parsed = parseTabKey(activeKey)
    if (!parsed || parsed.type !== TabType.AGENT)
      return
    const tabId = parsed.id
    if (chatStore.isInitialLoadComplete(tabId))
      return
    const agent = view.getAgentTab(tabId)
    if (!agent || !agent.workerId)
      return
    chatStore.loadInitialMessages(agent.workerId, tabId).catch(warnChatHistoryLoadFailed)
    void chatStore.loadMessageMarks(agent.workerId, tabId, abortSignalFor(agent.workerId))
  })

  createEffect(() => reconcileLaggingTails({
    agentTabs: () => params.view.all()
      .filter(t => t.type === TabType.AGENT)
      .map(t => ({ id: t.id, workerId: t.workerId ?? '' })),
    hasNewerMessages: id => chatStore.hasNewerMessages(id),
    caughtUpToLiveTail: id => chatStore.caughtUpToLiveTail(id),
    isTailFillDeferred: id => chatStore.isTailFillDeferred(id),
    getLastSeq: id => chatStore.getLastSeq(id),
    getLiveTailSeq: id => chatStore.liveTail.get(id),
    isFetchingNewer: id => chatStore.isFetchingNewer(id),
    catchUpToTail: (workerId, agentId, afterSeq) => {
      void chatStore.catchUpToTail(workerId, agentId, afterSeq, abortSignalFor(workerId)).catch(warnChatHistoryLoadFailed)
    },
    resumeDeferredTailFill: (workerId, agentId) => {
      void chatStore.resumeDeferredTailFill(workerId, agentId, abortSignalFor(workerId)).catch(warnChatHistoryLoadFailed)
    },
    jumpToLatest: (workerId, agentId) => {
      void chatStore.jumpToLatestMessages(workerId, agentId, abortSignalFor(workerId)).catch(warnChatHistoryLoadFailed)
    },
  }))

  return {
    workerOnline,
  }
}
