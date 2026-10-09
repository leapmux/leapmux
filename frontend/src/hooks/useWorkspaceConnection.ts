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
import { assertNever } from '~/lib/assertNever'
import { applyTerminalData, bufferHasVisibleContent } from '~/lib/terminal'
import { isPublishedActivityState } from '~/stores/agentActivity.store'
import { exceedsCatchUpGapLimit } from '~/stores/chatLiveTail'
import { parseTabKey } from '~/stores/tab.helpers'
import {
  clearPerTurnLiveState,
  handleActivityChanged,
  handleAgentMessage,
  handleAgentStatusChange,
  handleControlCancellation,
  handleControlRequest,
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

/** Retain a terminal frame until its xterm instance mounts. */
export interface PendingTerminalDataFrame {
  data: Uint8Array
  isSnapshot: boolean
  endOffset: bigint
}

/**
 * Limit the number of buffered frames per terminal before its xterm instance mounts.
 * Without this limit, a terminal that never mounts retains every live output frame.
 * This can occur when the terminal stays hidden or its startup or rendering fails.
 * A later snapshot restores the content from the worker's ring buffer.
 */
export const MAX_PENDING_TERMINAL_FRAMES = 256

/**
 * Queue TerminalData until an xterm instance mounts. A snapshot removes the earlier deltas.
 * Return true when the frame limit removes the oldest frames.
 * The caller must then request a full snapshot because incremental catch-up cannot restore those bytes.
 * See TerminalMeta.needsResync.
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
  // Remove the oldest frames when a terminal never mounts.
  // A snapshot clears the queue, so this limit applies to a long sequence of deltas.
  let evicted = false
  if (queue.length > MAX_PENDING_TERMINAL_FRAMES) {
    queue.splice(0, queue.length - MAX_PENDING_TERMINAL_FRAMES)
    evicted = true
  }
  pending.set(terminalId, queue)
  return evicted
}

/** Remove queued frames after the terminal tab closes or receives another placement. */
export function dropPendingTerminalData(pending: Map<string, PendingTerminalDataFrame[]>, terminalId: string): void {
  pending.delete(terminalId)
}

/**
 * Collect the supplied tabs whose worker went offline.
 * The caller supplies tabs from every workspace in the account.
 * A tab that another worker hosts retains its connected state.
 */
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
 * Clear the agent's live indicators when its worker goes offline.
 * A lost connection can omit these events:
 * - The result row.
 * - The turn-end divider.
 * - The INACTIVE status change.
 * Those events normally clear the indicators.
 * This separate cleanup prevents stale thinking counters and tool badges throughout the outage.
 * The export permits direct tests without the connection hook's internal offline signal.
 */
export function clearOfflineAgentState(
  agentId: string,
  stores: {
    chatStore: ReturnType<typeof createChatStore>
    agentSessionStore: ReturnType<typeof createAgentSessionStore>
    agentActivityStore?: AgentActivityStore
  },
): void {
  // An offline worker cannot report that the agent settled.
  // Remove its stored activity level so the view removes the spinner and the unusable Interrupt button.
  stores.agentActivityStore?.forget(agentId)
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
    // Load the newest page when the window is empty or its live catch-up gap exceeds the limit.
    // Forward paging and deferred catch-up cannot cross a gap that large.
    // Retain a history window that the user chose by scrolling away from the tail.
    // The user can return through normal history paging.
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
   * The focused tab's quake key, or null when the tab has none.
   * Use the shell's accessor so this hook and QuakeTerminalPanel select the same visible shell.
   * See isQuakeEntryOnScreen.
   */
  getActiveQuakeKeyId: () => string | null
  getActiveWorkspaceId: () => string | null
  /**
   * Play the alert after the worker reports that the agent settled. handleAgentSettled sets the
   * badge.
   */
  onAgentSettled?: (agentId: string, numToolUses?: number) => void
  /**
   * Refresh git status and the directory tree after each turn ends.
   * onAgentSettled can occur later when a subagent still runs after that turn.
   * The working tree needs a refresh at the turn boundary in either case.
   */
  onTurnEndRefresh?: (agentId: string) => void
}

type ReplayTopic = 'activity' | 'goal' | 'tasks'

interface RequestedReplay {
  workerId: string
  rootAgentId: string | undefined
  replayId: bigint
  cursorSeq: bigint
  liveClaims: Set<ReplayTopic>
}

/** Require each payload agent ID to match the frame destination. */
function replayPayloadMatchesAgent(agentEvent: AgentEvent): boolean {
  const inner = agentEvent.event
  switch (inner.case) {
    case 'statusChange':
    case 'controlRequest':
    case 'controlResponseChanged':
    case 'controlCancel':
    case 'todosChanged':
    case 'goalChanged':
    case 'backgroundTasksChanged':
      return inner.value.agentId === agentEvent.agentId
    case 'inputQueueChanged':
      return inner.value.snapshot?.agentId === agentEvent.agentId
    case 'agentMessage':
    case 'catchUpStart':
    case 'catchUpComplete':
    case 'turnEnd':
    case 'activityChanged':
      return true
    case undefined:
      return false
    default:
      return assertNever(inner)
  }
}

export function useWorkspaceConnection(params: WorkspaceConnectionParams) {
  const { chatStore, agentInputQueueStore, view, metadata, selection, controlStore, agentSessionStore, agentActivityStore, settingsLoading, repoGitStore } = params
  const [offlineWorkers, setOfflineWorkers] = createSignal<ReadonlySet<string>>(new Set())

  // Retain each transmitted FULL receipt and its independent live topic claims.
  const resumeTails = new Map<string, RequestedReplay>()
  // Retain TerminalData until the xterm instance mounts.
  // A snapshot removes earlier deltas that it replaces.
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
    // A removed tab supplies an empty worker ID.
    // Keep that ID outside the offline set because workerOnline treats it as connected.
    if (!workerId)
      return
    // Release optimistic branch pins when the worker reconnects. Retain the branch values.
    // A pin rejects a broadcast that still reports the branch before a successful local change.
    // The lost connection ends that claim.
    // Without this release, a background tab can retain its pin until the page closes.
    // Such a tab issues no refresh that can confirm the new branch and remove the pin.
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
   * Require an open quake panel whose directory belongs to the focused tab.
   * Use the same accessor that QuakeTerminalPanel uses to select its visible entry.
   * A separate selection rule can put a visible shell in NOTIFY mode and stop its output.
   * Define this function before the watch-plan memo.
   * createMemo runs immediately, so a later const definition would throw before initialization.
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
        // Read the loaded tail without reactive tracking, as with the resume sequence.
        // Each message changes it. That change must not send another watch request.
        // The worker uses it only to decide whether replay exceeds its limit.
        agentWindowTailSeq: agentId => untrack(() => chatStore.getLastSeq(agentId)),
        terminalAfterOffset: terminalId => untrack(() => metadata.get(terminalId)?.lastOffset ?? 0),
        // Track the resync flag so each set or clear sends the updated plan.
        // Do not track lastOffset, which changes for each pseudoterminal output chunk.
        terminalNeedsResync: terminalId => metadata.get(terminalId)?.needsResync === true,
        getAgentTab: (agentId: string) => view.getAgentTab(agentId),
        // Use FULL only for the open quake panel that belongs to the focused tab.
        // Use NOTIFY for the other shells to retain these updates:
        // - Cursor changes.
        // - Bells.
        // - Titles.
        // A reopened panel can then catch up from the worker's ring buffer.
        detachedTerminals: params.quakeStore.liveEntries().map(entry => ({
          terminalId: entry.terminalId,
          workerId: entry.workerId,
          mode: isQuakeEntryOnScreen(entry) ? WatchMode.FULL : WatchMode.NOTIFY,
        })),
      },
    ),
  )

  let abortSignalFor: (workerId: string) => AbortSignal | undefined = () => undefined

  /** Use the worker's root field, or a complete same-worker parent chain. */
  function replayRootFor(agentId: string, workerId: string): string | undefined {
    const visited = new Set<string>()
    let current = agentId
    while (!visited.has(current)) {
      visited.add(current)
      const tab = view.getAgentTab(current)
      if (!tab || tab.workerId !== workerId)
        return undefined
      if (tab.rootAgentId) {
        const root = view.getAgentTab(tab.rootAgentId)
        return root && root.workerId !== workerId ? undefined : tab.rootAgentId
      }
      if (!tab.parentAgentId)
        return current
      current = tab.parentAgentId
    }
    return undefined
  }

  /** Validate the exact transmitted origin before any replay effect. */
  function activeReplayFor(agentEvent: AgentEvent, streamWorkerId: string): RequestedReplay | undefined {
    const origin = agentEvent.replayAgentId
    const requested = resumeTails.get(origin)
    if (!origin || !requested || requested.workerId !== streamWorkerId
      || requested.replayId !== agentEvent.replayId
      || !agentSessionStore.acceptsReplay(origin, agentEvent.replayId)) {
      return undefined
    }
    if (view.getAgentTab(origin)?.workerId !== streamWorkerId
      || replayRootFor(origin, streamWorkerId) !== requested.rootAgentId) {
      return undefined
    }
    const destination = view.getAgentTab(agentEvent.agentId)
    if (destination && destination.workerId !== streamWorkerId)
      return undefined
    if (!replayPayloadMatchesAgent(agentEvent))
      return undefined
    if (agentEvent.agentId !== origin) {
      const kind = agentEvent.event.case
      if ((kind !== 'goalChanged' && kind !== 'backgroundTasksChanged')
        || requested.rootAgentId === undefined || agentEvent.agentId !== requested.rootAgentId) {
        return undefined
      }
    }
    return requested
  }

  /** Protect each active receipt's root topic after an accepted live publication. */
  function claimRootTopic(workerId: string, agentId: string, topic: 'goal' | 'tasks'): void {
    for (const [origin, requested] of resumeTails) {
      if (requested.workerId === workerId
        && (requested.rootAgentId === agentId || origin === agentId)
        && agentSessionStore.acceptsReplay(origin, requested.replayId)) {
        requested.liveClaims.add(topic)
      }
    }
  }

  const handleAgentEvent = (agentEvent: AgentEvent, streamWorkerId: string) => {
    const agentId = agentEvent.agentId
    const inner = agentEvent.event
    const replayId = agentEvent.replayId
    const requested = agentEvent.replay ? activeReplayFor(agentEvent, streamWorkerId) : undefined
    if (agentEvent.replay && !requested && inner.case !== 'agentMessage')
      return
    const resumeTail = requested?.cursorSeq

    // Use the frame's replay flag because live and replay events share this stream.
    // The worker registers the live watch before it sends replay events.
    // Arrival order cannot identify the delivery phase.
    // An inferred phase can suppress these effects during replay:
    // - A prompt badge.
    // - A plan title.
    // - INACTIVE cleanup.
    // See AgentEvent.replay.
    const catchUpPhase: CatchUpPhase = agentEvent.replay ? 'catchingUp' : 'live'
    const markLiveAgentActive = () => {
      if (catchUpPhase !== 'live')
        return
      const wid = view.getAgentTab(agentId)?.workerId || streamWorkerId || ''
      if (wid)
        setWorkerOnline(wid, true)
      const current = view.getAgentTab(agentId)
      if (current?.agentStatus === AgentStatus.INACTIVE) {
        // Record the live status so a pending ListAgents reply cannot restore its older answer.
        // See TabMetadataStore.liveStatusEpoch.
        metadata.patchLiveStatus(agentId, { agentStatus: AgentStatus.ACTIVE })
      }
    }

    switch (inner.case) {
      case 'agentMessage':
        if (!inner.value.transcriptOnly)
          markLiveAgentActive()
        handleAgentMessage(
          agentId,
          inner.value,
          { agentSessionStore, chatStore, view, metadata, selection, getActiveWorkspaceId: params.getActiveWorkspaceId },
          catchUpPhase,
          replayId,
          agentEvent.replay && !requested ? 'retain-transcript' : 'apply-current-state',
        )
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
      case 'controlResponseChanged':
        if (inner.case === 'controlRequest')
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
        handleControlCancellation(cc, controlStore)
        break
      }
      case 'turnEnd':
        // Refresh git status and the directory tree after each turn ends.
        // The working tree can change even when a subagent still runs.
        // handleAgentSettled plays the alert after a published transition out of WORKING.
        // An alert here would report completion while that subagent still runs.
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
        // The root agent owns its goal card and section visibility.
        // GoalChanged requires FULL interest. A child FULL replay can project its root's goal snapshot.
        const gc = inner.value
        if (requested?.liveClaims.has('goal'))
          break
        const outcome = chatStore.goal.replace(gc.agentId, gc.goal, gc.supportedActions, gc.goalUpdatedAt)
        if (catchUpPhase === 'live' && outcome !== 'stale') {
          claimRootTopic(streamWorkerId, gc.agentId, 'goal')
          if (outcome === 'progress-cleared')
            agentSessionStore.claimGoalProgressClear(gc.agentId, { phase: 'live' })
        }
        break
      }
      case 'backgroundTasksChanged': {
        // The root agent owns the registry and receives it through its existing WatchAgentEntry.
        // NOTIFY delivery keeps the sidebar and badge current when the root tab is hidden.
        const bc = inner.value
        if (requested?.liveClaims.has('tasks'))
          break
        chatStore.backgroundTasks.replace(bc.agentId, bc.tasks)
        if (catchUpPhase === 'live')
          claimRootTopic(streamWorkerId, bc.agentId, 'tasks')
        break
      }
      case 'activityChanged': {
        if (!isPublishedActivityState(inner.value.state))
          break
        const active = resumeTails.get(agentId)
        if (catchUpPhase === 'live' && active?.workerId === streamWorkerId
          && view.getAgentTab(agentId)?.workerId === streamWorkerId
          && agentSessionStore.acceptsReplay(agentId, active.replayId)) {
          active.liveClaims.add('activity')
        }
        // Apply the worker's authoritative activity transition, including through NOTIFY delivery.
        // A hidden tab can then receive its alert and badge when the agent settles.
        // A live transition still alerts while replay occurs.
        // catchUpStart supplies the replay baseline separately and produces no alert.
        // The worker sends no activityChanged event during replay.
        handleActivityChanged(agentId, inner.value, {
          metadata,
          selection,
          view,
          getActiveWorkspaceId: params.getActiveWorkspaceId,
          agentActivityStore,
          ...(params.onAgentSettled !== undefined ? { onAgentSettled: params.onAgentSettled } : {}),
        })
        break
      }
      case 'catchUpStart':
        chatStore.reconcileAuthoritativeTail(agentId, inner.value.latestSeq, resumeTail)
        // Restore an unclaimed valid activity level without an alert.
        // A valid live publication retains its activity claim until replay ends.
        // Tail reconciliation still applies when that claim refuses the older baseline.
        // See AgentActivityStore.seedPublished.
        if (!requested?.liveClaims.has('activity') && isPublishedActivityState(inner.value.activityState))
          agentActivityStore.seedPublished(agentId, inner.value.activityState)
        break
      case 'catchUpComplete':
        requested?.liveClaims.clear()
        agentSessionStore.retireReplay(agentId, replayId)
        chatStore.setCatchingUp(agentId, false)
        chatStore.reconcileAuthoritativeTail(
          agentId,
          inner.value.latestSeq,
          inner.value.startTailSeq === undefined
            ? resumeTail
            : inner.value.startTailSeq,
          true,
        )
        void chatStore.loadMessageMarks(
          view.getAgentTab(agentId)?.workerId ?? '',
          agentId,
          abortSignalFor(view.getAgentTab(agentId)?.workerId ?? '') ?? undefined,
        )
        break
    }
  }

  /**
   * Find a quake terminal by ID and apply the same visibility rule as its watch entry.
   * The bell and notification handlers receive only that ID.
   * A separate rule could make watch mode and desktop notifications disagree about the visible shell.
   */
  const isQuakeTerminalOnScreen = (terminalId: string): boolean => {
    const entry = params.quakeStore.entryForTerminal(terminalId)
    return entry !== undefined && isQuakeEntryOnScreen(entry)
  }

  const handleTerminalEvent = (termEvent: TerminalEvent, streamWorkerId: string) => {
    const terminalId = termEvent.terminalId

    switch (termEvent.event.case) {
      case 'data': {
        const { data, isSnapshot, endOffset } = termEvent.event.value
        // Discard frames when the terminal tab leaves the view.
        // No instance can mount to consume them after the tab closes or receives another placement.
        if (!view.getTerminalTab(terminalId)) {
          dropPendingTerminalData(pendingTerminalData, terminalId)
          break
        }
        const instance = getTerminalInstance(terminalId)
        if (!instance) {
          // Advance lastOffset only after a live instance receives the bytes.
          // An earlier advance would skip catch-up and leave a late instance blank.
          // Request a full snapshot if the frame limit removes bytes that incremental catch-up cannot restore.
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
        // An applied snapshot restores the buffer from the worker's ring.
        // Clear the flag that requests another full snapshot.
        metadata.patch(terminalId, { lastOffset: newOffset, ...(isSnapshot ? { needsResync: false } : {}) })
        break
      }
      case 'closed':
        // Close the quake terminal when its shell exits. Its next open starts a fresh shell.
        // Call handleShellExit before markTerminalExited so the panel shows no exit notice or prompt to press Enter.
        if (params.quakeStore.isQuakeTerminal(terminalId))
          params.quakeStore.handleShellExit(terminalId)
        else
          markTerminalExited(metadata, terminalId)
        // The pseudoterminal closed, so no instance can receive its buffered frames.
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
    onReplayRequested: (workerId, replayId, entries) => {
      batch(() => {
        for (const entry of entries) {
          agentSessionStore.beginReplay(entry.agentId, replayId)
          resumeTails.set(entry.agentId, {
            workerId,
            rootAgentId: replayRootFor(entry.agentId, workerId),
            replayId,
            cursorSeq: entry.cursorSeq,
            liveClaims: new Set<ReplayTopic>(),
          })
          chatStore.setCatchingUp(entry.agentId, true)
        }
      })
    },
    onReplayRetired: (workerId, replayId, agentIds, reason) => {
      batch(() => {
        for (const agentId of agentIds) {
          const requested = resumeTails.get(agentId)
          if (requested && requested.workerId !== workerId)
            continue
          if (reason === 'removed')
            agentSessionStore.removeReplay(agentId, replayId)
          else
            agentSessionStore.retireReplay(agentId, replayId)
          if (resumeTails.get(agentId)?.replayId === replayId) {
            resumeTails.delete(agentId)
            chatStore.setCatchingUp(agentId, false)
          }
        }
      })
    },
    onPromoted: (workerId, agentIds) => {
      for (const agentId of agentIds) {
        void chatStore.loadInitialMessages(workerId, agentId).catch(warnChatHistoryLoadFailed)
        void chatStore.loadMessageMarks(workerId, agentId, abortSignalFor(workerId))
      }
    },
  })
  abortSignalFor = streams.abortSignalFor

  // Mark the offline worker's running terminals disconnected and clear its agents' live indicators.
  // Retain the git store's last known working-tree state, as RepoGitStore.refresh does after a failed read.
  // Tab.gitToplevel also survives the outage. branchKeys.repoKeyAndLabel still groups tabs by that field.
  // Removing the git entry here would leave those groups without their branch labels.
  //
  // A background agent tab does not automatically restore a removed git entry:
  // - useTabHydrators retains its hydrated state for the page lifetime.
  // - A turn end supplies a new git status only for the agent whose turn ended.
  // - Replay supplies git status only when the client promotes the agent to FULL.
  // The user would otherwise need to select each tab or request a Files refresh after every outage.
  //
  // Permanent removal has a separate caller. useWorkerSection retains git entries after worker deregistration also.
  // RepoGitState.nonRepoProbeIgnored permits later non-repository answers if the repository disappears during the outage.
  createEffect(() => {
    const offline = offlineWorkers()
    if (offline.size === 0)
      return
    untrack(() => {
      // Include detached quake terminals because view.all contains placed tabs only.
      // Without their separate list, they would retain READY throughout the outage.
      // Build both lists once before the loop because neither depends on the worker.
      // A Hub restart can add several offline workers at once.
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
            // This direct status patch does not call handleAgentInactive.
            // Clear the live indicators here before that patch.
            clearOfflineAgentState(tab.id, { chatStore, agentSessionStore, agentActivityStore })
            if (tab.agentStatus === AgentStatus.ACTIVE)
              metadata.patch(tab.id, { agentStatus: AgentStatus.INACTIVE })
          }
        })
      }
    })
  })

  // Load history when a newly selected agent tab did not load it through onPromoted.
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
