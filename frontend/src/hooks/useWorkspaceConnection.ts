import type { CatchUpPhase } from './agentEvents'
import type { AgentEvent, TerminalEvent } from '~/generated/leapmux/v1/workspace_pb'
import type { createLoadingSignal } from '~/hooks/createLoadingSignal'
import type { createAgentSessionStore } from '~/stores/agentSession.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createControlStore } from '~/stores/control.store'
import type { AgentTab, Tab } from '~/stores/tab.types'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'

import type { TabView } from '~/stores/tabView'
import { batch, createEffect, createMemo, createSignal, onCleanup, untrack } from 'solid-js'
import { showWarnToastUnlessDisconnected } from '~/components/common/Toast'
import { addTerminalInstanceReadyListener, getTerminalInstance } from '~/components/terminal/TerminalView'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { applyTerminalData, bufferHasVisibleContent } from '~/lib/terminal'
import { parseTabKey } from '~/stores/tab.helpers'
import {
  handleAgentMessage,
  handleAgentStatusChange,
  handleControlRequest,
  handleStreamChunk,
  handleStreamEnd,
  handleTurnEnd,
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

/**
 * Which tabs a worker going offline affects.
 *
 * Both arms filter on `workerId`, and that is the whole point: this walks
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

export function reconcileLaggingTails(deps: {
  agentTabs: () => ReadonlyArray<{ id: string, workerId: string }>
  hasNewerMessages: (agentId: string) => boolean
  caughtUpToLiveTail: (agentId: string) => boolean
  isTailFillDeferred: (agentId: string) => boolean
  getLastSeq: (agentId: string) => bigint
  isFetchingNewer: (agentId: string) => boolean
  catchUpToTail: (workerId: string, agentId: string, afterSeq: bigint) => void
  resumeDeferredTailFill: (workerId: string, agentId: string) => void
  jumpToLatest: (workerId: string, agentId: string) => void
}): void {
  for (const tab of deps.agentTabs()) {
    if (!tab.workerId)
      continue
    const atTail = !deps.hasNewerMessages(tab.id)
    const caughtUp = deps.caughtUpToLiveTail(tab.id)
    const deferred = deps.isTailFillDeferred(tab.id)
    if (caughtUp)
      continue
    if (deps.getLastSeq(tab.id) === 0n) {
      if (!deps.isFetchingNewer(tab.id))
        deps.jumpToLatest(tab.workerId, tab.id)
      continue
    }
    if (atTail)
      deps.catchUpToTail(tab.workerId, tab.id, deps.getLastSeq(tab.id))
    else if (deferred)
      deps.resumeDeferredTailFill(tab.workerId, tab.id)
  }
}

export interface WorkspaceConnectionParams {
  chatStore: ReturnType<typeof createChatStore>
  view: TabView
  metadata: TabMetadataStore
  selection: TabSelectionStore
  controlStore: ReturnType<typeof createControlStore>
  agentSessionStore: ReturnType<typeof createAgentSessionStore>
  settingsLoading: ReturnType<typeof createLoadingSignal>
  getActiveWorkspaceId: () => string | null
  /** Called when an agent turn ends (turn completed or control request received). */
  onTurnEnd?: (agentId: string, numToolUses?: number) => void
}

export function useWorkspaceConnection(params: WorkspaceConnectionParams) {
  const { chatStore, view, metadata, selection, controlStore, agentSessionStore, settingsLoading } = params
  const [offlineWorkers, setOfflineWorkers] = createSignal<ReadonlySet<string>>(new Set())

  // Per-agent catch-up phase across all workers.
  const catchUpPhases = new Map<string, CatchUpPhase>()
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
    setOfflineWorkers((prev) => {
      const next = new Set(prev)
      if (online)
        next.delete(workerId)
      else
        next.add(workerId)
      return next
    })
  }

  const watchPlans = createMemo(() =>
    buildWatchPlans(
      params.view.all(),
      params.getActiveWorkspaceId(),
      tileId => selection.activeKeyForTile(tileId),
      agentId => untrack(() => chatStore.getResumeAfterSeq(agentId)),
      terminalId => untrack(() => metadata.get(terminalId)?.lastOffset ?? 0),
      // Tracked on purpose: the plan must go out the moment a terminal is
      // flagged, and back out when the snapshot lands. Only this field is
      // tracked — lastOffset above moves at PTY-read frequency, and keying
      // the plan on it would re-send a watch update per output chunk.
      terminalId => metadata.get(terminalId)?.needsResync === true,
      (agentId: string) => view.getAgentTab(agentId),
    ),
  )

  let abortSignalFor: (workerId: string) => AbortSignal | undefined = () => undefined

  const handleAgentEvent = (agentEvent: AgentEvent) => {
    const agentId = agentEvent.agentId
    const inner = agentEvent.event

    const catchUpPhase = catchUpPhases.get(agentId) ?? 'live'
    const markLiveAgentActive = () => {
      if (catchUpPhase !== 'live')
        return
      const wid = view.getAgentTab(agentId)?.workerId ?? ''
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
          params.onTurnEnd,
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
          { agentSessionStore, chatStore, view, metadata, selection, getActiveWorkspaceId: params.getActiveWorkspaceId, controlStore },
          settingsLoading,
          online => setWorkerOnline(view.getAgentTab(agentId)?.workerId ?? '', online),
          params.onTurnEnd,
        )
        break
      case 'controlRequest':
        markLiveAgentActive()
        handleControlRequest(
          agentId,
          inner.value,
          catchUpPhase,
          { agentSessionStore, chatStore, view, metadata, selection, getActiveWorkspaceId: params.getActiveWorkspaceId, controlStore },
          params.onTurnEnd,
        )
        break
      case 'controlCancel': {
        const cc = inner.value
        controlStore.removeRequest(cc.agentId, cc.requestId)
        break
      }
      case 'turnEnd':
        handleTurnEnd(
          agentId,
          inner.value,
          { metadata, selection, getActiveWorkspaceId: params.getActiveWorkspaceId, view },
          params.onTurnEnd,
        )
        break
      case 'messageError': {
        const me = inner.value
        if (me.error)
          chatStore.setMessageError(me.messageId, me.error)
        else
          chatStore.clearMessageError(me.messageId)
        break
      }
      case 'messageDeleted': {
        const md = inner.value
        chatStore.removeMessage(md.agentId, md.messageId, md.seq, md.newLatestSeq)
        break
      }
      case 'todosChanged': {
        const tc = inner.value
        chatStore.todos.replace(tc.agentId, tc.todos)
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
      case 'catchUpStart':
        chatStore.reconcileAuthoritativeTail(agentId, inner.value.latestSeq, resumeTails.get(agentId))
        break
      case 'catchUpComplete':
        catchUpPhases.set(agentId, 'live')
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

  const handleTerminalEvent = (termEvent: TerminalEvent) => {
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
        markTerminalExited(metadata, terminalId)
        // The PTY is gone; any buffered pre-mount bytes will never be written.
        dropPendingTerminalData(pendingTerminalData, terminalId)
        break
      case 'statusChange':
        applyTerminalStatusChange(metadata, view.getTerminalTab(terminalId), terminalId, termEvent.event.value)
        break
      case 'bell':
        handleTerminalBell(terminalId, { metadata, selection, getActiveWorkspaceId: params.getActiveWorkspaceId, view })
        break
      case 'notification':
        handleTerminalNotification(terminalId, termEvent.event.value, {
          metadata,
          selection,
          getActiveWorkspaceId: params.getActiveWorkspaceId,
          view,
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
          handleAgentEvent(resp.event.value)
          break
        case 'terminalEvent':
          handleTerminalEvent(resp.event.value)
          break
      }
    },
    onWorkerOnline: setWorkerOnline,
    onPromoted: (workerId, agentIds) => {
      for (const agentId of agentIds) {
        catchUpPhases.set(agentId, 'catchingUp')
        chatStore.setCatchingUp(agentId, true)
        const resumeSeq = untrack(() => chatStore.getResumeAfterSeq(agentId))
        resumeTails.set(agentId, resumeSeq)
        void chatStore.loadInitialMessages(workerId, agentId).catch((err) => {
          showWarnToastUnlessDisconnected('Failed to load chat history', err)
        })
        void chatStore.loadMessageMarks(workerId, agentId, abortSignalFor(workerId))
      }
    },
  })
  abortSignalFor = streams.abortSignalFor

  // When a worker goes offline, mark its running terminals disconnected and
  // clear stale streaming state for its agents.
  createEffect(() => {
    const offline = offlineWorkers()
    if (offline.size === 0)
      return
    untrack(() => {
      for (const workerId of offline) {
        const { terminals: affectedTerminals, agents } = collectWorkerOfflineTargets(params.view.all(), workerId)
        batch(() => {
          if (affectedTerminals.size > 0) {
            params.metadata.patchMatching(
              (_meta, tabId) => affectedTerminals.has(tabId),
              { terminalStatus: TerminalStatus.DISCONNECTED },
            )
          }
          for (const tab of agents) {
            chatStore.streamingText.clear(tab.id)
            for (const spanId of Object.keys(chatStore.getAgentCommandStreams(tab.id)))
              chatStore.clearCommandStream(tab.id, spanId)
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
    chatStore.loadInitialMessages(agent.workerId, tabId).catch((err) => {
      showWarnToastUnlessDisconnected('Failed to load chat history', err)
    })
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
    isFetchingNewer: id => chatStore.isFetchingNewer(id),
    catchUpToTail: (workerId, agentId, afterSeq) => {
      void chatStore.catchUpToTail(workerId, agentId, afterSeq, abortSignalFor(workerId))
    },
    resumeDeferredTailFill: (workerId, agentId) => {
      void chatStore.resumeDeferredTailFill(workerId, agentId, abortSignalFor(workerId))
    },
    jumpToLatest: (workerId, agentId) => {
      void chatStore.jumpToLatestMessages(workerId, agentId, abortSignalFor(workerId))
    },
  }))

  return {
    workerOnline,
  }
}
