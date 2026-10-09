import type { WatchPlan } from './watchPlan'
import type { WatchEventsHandle } from '~/api/workerRpc'
import type { WatchAgentEntry, WatchEventsResponse, WatchRejection } from '~/generated/proto/leapmux/v1/workspace_pb'
import type { TabView } from '~/stores/tabView'
import { create } from '@bufbuild/protobuf'
import { createEffect, onCleanup } from 'solid-js'
import { isDisconnectError } from '~/api/workerErrors'
import { channelManager, watchEventsViaChannel } from '~/api/workerRpc'
import { showWarnToastWithLoggedCause } from '~/components/common/Toast'
import { EVENTS_REJECTION_RETRY } from '~/generated/contracts/retry'
import { WatchAgentEntrySchema, WatchMode, WatchRejectionReason } from '~/generated/proto/leapmux/v1/workspace_pb'
import { ChannelError } from '~/lib/channel'
import { emitDevEvent } from '~/lib/devInstrument'
import { createLogger } from '~/lib/logger'
import { createExponentialBackoff } from '~/lib/retry'
import { shouldRetryRejection, watchPlanKey } from './watchPlan'

const log = createLogger('watchEventsStreams')

/**
 * Wait for two failed reconnects before showing an outage toast. A mobile client often
 * reconnects when the user returns to the app. The base delays are one second and two seconds.
 * Jitter changes each actual delay.
 */
const SILENT_RECONNECT_ATTEMPTS = 2

/**
 * Show a connection message that the user can understand.
 * Channel error text describes internal state. showWarnToastWithLoggedCause logs that text separately.
 */
const OUTAGE_MESSAGE = 'Connection to worker lost, reconnecting…'

interface ReplayRequest {
  entry: Readonly<WatchAgentEntry>
  completed: boolean
}

interface WorkerStream {
  handle: WatchEventsHandle | null
  pendingPlan: WatchPlan | null
  /** Interest key from the last settled acknowledgment. */
  sentKey: string
  /** FULL identities from the last settled acknowledgment. */
  fullReplays: Map<string, bigint>
  /** Latest transmitted plan that awaits its exact acknowledgment. */
  inflightPlan: WatchPlan | null
  inflightUpdateId: bigint
  inflightKey: string
  /** Last transmitted plan, after any confirmed durable rejection. */
  transmittedPlan: WatchPlan | null
  /** Fixed FULL entries. Keep completed entries until their lifetime ends. */
  replayRequests: Map<string, ReplayRequest>
  /** New replay identities carried by the outstanding interest update. */
  inflightReplays: Map<string, bigint>
  closed: boolean
  drainScheduled: boolean
  opening: boolean
  abort: AbortController
}

export interface UseWatchEventsStreamsOpts {
  view: TabView
  plans: () => Map<string, WatchPlan>
  onEvent: (workerId: string, resp: WatchEventsResponse) => void
  onWorkerOnline: (workerId: string, online: boolean) => void
  /** Report accepted FULL promotions to the caller that loads initial messages. */
  onPromoted: (workerId: string, agentIds: string[]) => void
  onReplayRequested?: (workerId: string, replayId: bigint, entries: readonly WatchAgentEntry[]) => void
  onReplayRetired?: (workerId: string, replayId: bigint, agentIds: readonly string[], reason: 'removed' | 'demoted' | 'rejected' | 'closed') => void
}

/**
 * Keep one WatchEvents stream per worker whose tabs appear in the plans.
 * Send coalesced changes through InnerStreamRequest without waiting in the caller.
 * Commit interest only after an exact current acknowledgment.
 * A LOOKUP_FAILED response cannot establish FULL interest.
 * Replay receipts begin before transport and retain their exact request ID.
 */
export function useWatchEventsStreams(opts: UseWatchEventsStreamsOpts): {
  abortSignalFor: (workerId: string) => AbortSignal | undefined
} {
  const streams = new Map<string, WorkerStream>()
  let updateId = 0n
  /** Record one toast for the current outage across all workers. */
  let outageAnnounced = false
  const reconnectBackoff = createExponentialBackoff<string>({
    initialMs: 1000,
    maxMs: 30000,
    multiplier: 2,
    // Spread reconnect attempts after a shared Hub outage.
    jitterFactor: 0.2,
  })
  // The CLI's streamevents command uses these same generated retry values.
  // The browser and CLI therefore use the same LOOKUP_FAILED schedule.
  const rejectionBackoff = createExponentialBackoff<string>(EVENTS_REJECTION_RETRY)

  function getOrCreate(workerId: string): WorkerStream {
    let s = streams.get(workerId)
    if (!s) {
      s = {
        handle: null,
        pendingPlan: null,
        sentKey: '',
        fullReplays: new Map(),
        inflightPlan: null,
        inflightUpdateId: 0n,
        inflightKey: '',
        transmittedPlan: null,
        replayRequests: new Map(),
        inflightReplays: new Map(),
        closed: false,
        drainScheduled: false,
        opening: false,
        abort: new AbortController(),
      }
      streams.set(workerId, s)
    }
    return s
  }

  /** Compare the newest requested interest. An older acknowledgment cannot supersede a queued or transmitted change. */
  function interestMatches(s: WorkerStream, key: string): boolean {
    if (s.pendingPlan)
      return key === watchPlanKey(s.pendingPlan)
    return key === (s.inflightPlan ? s.inflightKey : s.sentKey)
  }

  /**
   * Retire only the specified receipts. Group the callback by each exact request ID.
   */
  function retireReplays(workerId: string, s: WorkerStream, agentIds: readonly string[], reason: 'removed' | 'demoted' | 'rejected' | 'closed', requestId?: bigint): void {
    const groups = new Map<bigint, string[]>()
    for (const agentId of agentIds) {
      const id = s.replayRequests.get(agentId)?.entry.replayId
      if (id === undefined || (requestId !== undefined && id !== requestId))
        continue
      s.replayRequests.delete(agentId)
      const group = groups.get(id) ?? []
      group.push(agentId)
      groups.set(id, group)
    }
    for (const [id, group] of groups)
      opts.onReplayRetired?.(workerId, id, group, reason)
  }

  /** Allocate a positive uint64 without reuse when a worker returns. */
  function allocateUpdateId(workerId: string): bigint | null {
    if (updateId === (1n << 64n) - 1n) {
      log.warn('The watch request ID range is exhausted.', { workerId })
      cancelWorker(workerId)
      return null
    }
    updateId++
    return updateId
  }

  /** Keep unchanged FULL receipts and open new receipts before transport. */
  function requestReplays(workerId: string, s: WorkerStream, plan: WatchPlan, requestId: bigint): WatchPlan {
    const modes = new Map(plan.agents.map(entry => [entry.agentId, entry.mode]))
    retireReplays(workerId, s, [...s.replayRequests.keys()].filter(agentId => !modes.has(agentId)), 'removed')
    retireReplays(workerId, s, [...s.replayRequests.keys()].filter(agentId => modes.get(agentId) !== WatchMode.FULL), 'demoted')
    const previousFull = new Set(s.transmittedPlan?.agents.filter(entry => entry.mode === WatchMode.FULL).map(entry => entry.agentId))
    const requested: WatchAgentEntry[] = []
    s.inflightReplays = new Map()
    const agents = plan.agents.map((entry) => {
      if (entry.mode !== WatchMode.FULL)
        return create(WatchAgentEntrySchema, { ...entry, replayId: 0n })
      const existing = s.replayRequests.get(entry.agentId)
      if (existing && previousFull.has(entry.agentId))
        return { ...existing.entry }
      if (existing && !existing.completed)
        retireReplays(workerId, s, [entry.agentId], 'rejected', existing.entry.replayId)
      const fixed = Object.freeze(create(WatchAgentEntrySchema, { ...entry, replayId: requestId }))
      s.replayRequests.set(entry.agentId, { entry: fixed, completed: false })
      s.inflightReplays.set(entry.agentId, fixed.replayId)
      requested.push({ ...fixed })
      return { ...fixed }
    })
    const transmitted = {
      agents,
      terminals: plan.terminals.map(entry => ({ ...entry })),
      terminalResync: new Set(plan.terminalResync),
    }
    s.transmittedPlan = transmitted
    if (requested.length > 0)
      opts.onReplayRequested?.(workerId, requestId, requested.map(entry => ({ ...entry })))
    return transmitted
  }

  /** Clear stream authority and retire its exact replay receipts. */
  function resetForReconnect(workerId: string, s: WorkerStream): void {
    retireReplays(workerId, s, [...s.replayRequests.keys()], 'closed')
    s.handle = null
    s.inflightPlan = null
    s.inflightUpdateId = 0n
    s.inflightKey = ''
    s.transmittedPlan = null
    s.fullReplays.clear()
    s.inflightReplays.clear()
    s.sentKey = ''
  }

  function tabExists(entityId: string): boolean {
    return !!opts.view.getAgentTab(entityId) || !!opts.view.getTerminalTab(entityId)
  }

  function anyRetryable(agents: readonly WatchRejection[], terminals: readonly WatchRejection[]): boolean {
    for (const r of agents) {
      if (shouldRetryRejection(r, tabExists(r.entityId)))
        return true
    }
    for (const r of terminals) {
      if (shouldRetryRejection(r, tabExists(r.entityId)))
        return true
    }
    return false
  }

  /** Identify a failed transport. */
  function isTransportError(err: unknown): boolean {
    return err instanceof ChannelError && err.source === 'transport'
  }

  /** Identify a relay refusal that another reconnect cannot resolve. */
  function isFatalTransportError(err: unknown): boolean {
    return err instanceof ChannelError && err.fatal
  }

  /**
   * Report that this client cannot receive the worker's events.
   * useWorkspaceConnection clears the live state, including after a fatal close.
   * That effect marks READY terminals DISCONNECTED and ACTIVE agents INACTIVE.
   * It also clears their live counters. announceOutage controls the user notification separately.
   */
  function markWorkerOffline(workerId: string): void {
    opts.onWorkerOnline(workerId, false)
  }

  /**
   * Show at most one toast after the quiet reconnect attempts fail.
   * scheduleReconnect calls this after each failed reconnect, including application failures.
   * A stopped worker can fail through the Hub connection before a transport opens.
   * The error goes to the log. A normal onEnd supplies no error.
   */
  function announceOutage(workerId: string, err: unknown): void {
    // attemptCount is zero on the first loss and one after the first reconnect fails.
    // Show the toast only after SILENT_RECONNECT_ATTEMPTS quiet failures.
    if (reconnectBackoff.attemptCount(workerId) < SILENT_RECONNECT_ATTEMPTS) {
      log.debug('The connection ended. Delay the outage toast until reconnects fail.', { workerId, err })
      return
    }
    // A Hub outage can disconnect several workers. Show one toast for that outage.
    if (outageAnnounced)
      return
    outageAnnounced = true
    showWarnToastWithLoggedCause(OUTAGE_MESSAGE, err)
  }

  /** Commit accepted interest and report its new FULL agents. */
  function commitAckedPlan(workerId: string, s: WorkerStream, plan: WatchPlan, acceptedFull: ReadonlyMap<string, bigint>): void {
    const prevFull = s.fullReplays
    s.sentKey = watchPlanKey(plan)
    s.fullReplays = new Map(plan.agents
      .filter(entry => entry.mode === WatchMode.FULL && acceptedFull.get(entry.agentId) === entry.replayId)
      .map(entry => [entry.agentId, entry.replayId]))
    s.inflightPlan = null
    s.inflightUpdateId = 0n
    s.inflightKey = ''
    s.inflightReplays.clear()
    const promoted = [...s.fullReplays].filter(([agentId, replayId]) => prevFull.get(agentId) !== replayId).map(([agentId]) => agentId)
    if (promoted.length > 0)
      opts.onPromoted(workerId, promoted)
  }

  function handleUpdateAck(workerId: string, resp: WatchEventsResponse): boolean | null {
    const ack = resp.event.case === 'updateAck' ? resp.event.value : undefined
    if (!ack)
      return null
    const s = streams.get(workerId)
    if (!s || s.closed)
      return null

    // Only the exact outstanding request can change interest or the retry count.
    if (!s.inflightPlan || ack.updateId !== s.inflightUpdateId)
      return null

    const inflight = s.inflightPlan
    const acceptedFull = new Map(ack.agentStates.filter(entry => entry.mode === WatchMode.FULL).map(entry => [entry.agentId, entry.replayId]))
    const unaccepted = [...s.replayRequests].filter(([agentId, receipt]) => acceptedFull.get(agentId) !== receipt.entry.replayId)
    retireReplays(workerId, s, unaccepted.filter(([, receipt]) => !receipt.completed).map(([agentId]) => agentId), 'rejected')
    // LOOKUP_FAILED preserves prior worker registrations. Durable refusals remove the refused entries.
    const durable = new Set(ack.rejectedAgents.filter(entry => entry.reason !== WatchRejectionReason.LOOKUP_FAILED).map(entry => entry.entityId))
    const unregistered = new Set(unaccepted.map(([agentId]) => agentId))
    if (s.transmittedPlan && (durable.size > 0 || unregistered.size > 0))
      s.transmittedPlan = { ...s.transmittedPlan, agents: s.transmittedPlan.agents.filter(entry => !durable.has(entry.agentId) && !unregistered.has(entry.agentId)) }
    const needsRetry = anyRetryable(ack.rejectedAgents, ack.rejectedTerminals)

    if (inflight && !needsRetry) {
      // Settle this requested plan after a durable refusal. Record only the acknowledged FULL
      // identities as accepted.

      commitAckedPlan(workerId, s, inflight, acceptedFull)
    }
    else if (needsRetry) {
      // LOOKUP_FAILED leaves interest unconfirmed. Send the current plan after the retry delay.
      s.inflightPlan = null
      s.inflightUpdateId = 0n
      s.inflightKey = ''
      s.inflightReplays.clear()
      s.sentKey = ''
    }

    if (!needsRetry)
      return false
    if (rejectionBackoff.isExhausted(workerId)) {
      log.warn('The retry count reached its maximum for rejected watch requests.', { workerId })
      return true
    }
    rejectionBackoff.schedule(workerId, () => {
      const current = streams.get(workerId)
      if (!current || current.closed)
        return
      // Read the latest coalesced interest when the retry runs.
      const latest = opts.plans().get(workerId)
      if (!latest)
        return
      current.pendingPlan = latest
      scheduleDrain(workerId)
    })
    return true
  }

  async function openStream(workerId: string, plan: WatchPlan): Promise<void> {
    const s = getOrCreate(workerId)
    if (s.closed || s.opening)
      return
    const nextId = allocateUpdateId(workerId)
    if (nextId === null)
      return
    s.opening = true
    try {
      s.inflightUpdateId = nextId
      s.inflightKey = watchPlanKey(plan)
      const transmitted = requestReplays(workerId, s, plan, nextId)
      s.inflightPlan = transmitted
      const handle = await watchEventsViaChannel(workerId, {
        agents: transmitted.agents,
        terminals: transmitted.terminals,
        updateId: nextId,
      })
      if (s.closed) {
        handle.close()
        return
      }
      s.handle?.close()
      s.handle = handle
      // 161-watch-stream-continuity.spec.ts counts successful stream opens.
      // An interest update must not emit this event.
      emitDevEvent('leapmux:watch-events-open', () => ({ workerId, updateId: nextId }))
      // A delayed callback from a replaced stream cannot change its successor.
      const isCurrentHandle = () => !s.closed && s.handle === handle

      handle.onEvent((resp) => {
        if (!isCurrentHandle())
          return
        reconnectBackoff.reset(workerId)
        if (resp.event.case === 'updateAck') {
          // A settled acknowledgment resets the rejection count.
          // An ignored acknowledgment must preserve a pending retry and its count.
          if (handleUpdateAck(workerId, resp) === false)
            rejectionBackoff.reset(workerId)
        }
        else {
          if (resp.event.case === 'agentEvent') {
            const event = resp.event.value
            const receipt = s.replayRequests.get(event.agentId)
            if (event.replay && event.event.case === 'catchUpComplete'
              && event.replayAgentId === event.agentId
              && receipt?.entry.mode === WatchMode.FULL
              && receipt.entry.replayId === event.replayId) {
              receipt.completed = true
            }
          }
          // Unrelated traffic must preserve the LOOKUP_FAILED retry cap.
          opts.onEvent(workerId, resp)
        }
      })
      handle.onEnd(() => {
        if (!isCurrentHandle())
          return
        markWorkerOffline(workerId)
        resetForReconnect(workerId, s)
        scheduleReconnect(workerId)
      })
      handle.onError((err) => {
        if (!isCurrentHandle())
          return
        if (isTransportError(err) || isDisconnectError(err))
          markWorkerOffline(workerId)
        else
          log.warn('The watch stream failed.', err)
        // Application errors retain the reconnect delay and retry count.
        resetForReconnect(workerId, s)
        scheduleReconnect(workerId, err)
      })

      // Clear the shared toast state only after every remaining worker reconnects.
      // A sibling that still lacks a handle belongs to the same outage.
      if (![...streams.values()].some(other => !other.closed && other.handle === null))
        outageAnnounced = false
      opts.onWorkerOnline(workerId, true)
    }
    catch (err) {
      if (s.closed || streams.get(workerId) !== s)
        return
      log.debug('The watch stream did not open.', { workerId, err })
      if (isTransportError(err) || isDisconnectError(err))
        markWorkerOffline(workerId)
      resetForReconnect(workerId, s)
      scheduleReconnect(workerId, err)
    }
    finally {
      s.opening = false
      // Send a plan that arrived while the stream opened.
      if (!s.closed && s.pendingPlan)
        scheduleDrain(workerId)
    }
  }

  function sendUpdate(workerId: string, plan: WatchPlan): void {
    const s = getOrCreate(workerId)
    const nextId = allocateUpdateId(workerId)
    if (nextId === null)
      return
    s.inflightUpdateId = nextId
    s.inflightKey = watchPlanKey(plan)
    const transmitted = requestReplays(workerId, s, plan, nextId)
    s.inflightPlan = transmitted
    try {
      s.handle!.update({
        agents: transmitted.agents,
        terminals: transmitted.terminals,
        updateId: nextId,
      })
    }
    catch (err) {
      log.warn('The watch update failed. Send the latest plan again.', { workerId, err })
      for (const [agentId, replayId] of s.inflightReplays)
        retireReplays(workerId, s, [agentId], 'rejected', replayId)
      s.inflightPlan = null
      s.inflightUpdateId = 0n
      s.inflightKey = ''
      s.inflightReplays.clear()
      s.pendingPlan = opts.plans().get(workerId) ?? plan
      scheduleDrain(workerId)
    }
  }

  /**
   * Schedule the next reconnect unless the relay refuses further connections.
   * A fatal ChannelError makes ensureWebSocket reject before it uses the network.
   * A retry timer cannot resolve that refusal and would repeat without a maximum attempt count.
   * Keep the worker without a handle or a timer until a real interest change occurs.
   * That change attempts one new open. A fresh login can clear the relay refusal.
   * All failed reconnects pass through this function, so it controls the outage toast also.
   */
  function scheduleReconnect(workerId: string, err?: unknown): void {
    const s = streams.get(workerId)
    if (!s || s.closed)
      return
    // onEnd supplies no error and can arrive after the relay refuses another connection.
    // Check the relay in every path before scheduling a timer or a reconnect toast.
    // The shell already shows the Hub's refusal in a persistent toast.
    if (isFatalTransportError(err) || channelManager.fatalCloseInfo())
      return
    announceOutage(workerId, err)
    // 184-disconnect-toast.spec.ts observes each later opportunity to repeat the toast.
    // Emit after the toast decision. Count the first connection loss as one failure.
    emitDevEvent('leapmux:watch-events-redial', () => ({ workerId, failures: reconnectBackoff.attemptCount(workerId) + 1 }))
    reconnectBackoff.schedule(workerId, () => {
      if (s.closed)
        return
      // Read the latest plan when the timer runs.
      // Assigning pendingPlan before the timer would make openStream's finally skip the retry delay.
      // cancelWorker closes this record when its worker leaves the plan.
      const latest = opts.plans().get(workerId)
      if (latest)
        s.pendingPlan = latest
      s.abort.abort()
      s.abort = new AbortController()
      void drainWorker(workerId)
    })
  }

  function scheduleDrain(workerId: string): void {
    const s = getOrCreate(workerId)
    if (s.drainScheduled)
      return
    s.drainScheduled = true
    queueMicrotask(() => {
      s.drainScheduled = false
      void drainWorker(workerId)
    })
  }

  /** Queue the plan without waiting for the channel to open. */
  function update(workerId: string, plan: WatchPlan): void {
    const s = getOrCreate(workerId)
    s.pendingPlan = plan
    scheduleDrain(workerId)
  }

  async function drainWorker(workerId: string): Promise<void> {
    const s = streams.get(workerId)
    if (!s || s.closed)
      return
    if (s.opening) {
      // Keep pendingPlan until openStream's finally schedules another drain.
      return
    }
    const plan = s.pendingPlan
    if (!plan)
      return
    s.pendingPlan = null
    const key = watchPlanKey(plan)
    // Skip an acknowledged plan or the same outstanding request.
    if (s.handle && interestMatches(s, key))
      return
    if (!s.handle) {
      await openStream(workerId, plan)
      return
    }
    sendUpdate(workerId, plan)
  }

  function cancelWorker(workerId: string, reason: 'removed' | 'closed' = 'closed'): void {
    const s = streams.get(workerId)
    if (!s)
      return
    s.closed = true
    s.pendingPlan = null
    retireReplays(workerId, s, [...s.replayRequests.keys()], reason)
    // Reset this worker only. A sibling worker must retain its reconnect timer.
    reconnectBackoff.reset(workerId)
    rejectionBackoff.reset(workerId)
    s.handle?.close()
    s.handle = null
    s.abort.abort()
    streams.delete(workerId)
  }

  createEffect(() => {
    const plans = opts.plans()
    for (const [workerId] of streams) {
      if (!plans.has(workerId))
        cancelWorker(workerId, 'removed')
    }
    for (const [workerId, plan] of plans) {
      const s = getOrCreate(workerId)
      s.closed = false
      const key = watchPlanKey(plan)
      if (s.handle && interestMatches(s, key))
        continue
      update(workerId, plan)
    }
  })

  onCleanup(() => {
    reconnectBackoff.cancelAll()
    for (const workerId of [...streams.keys()])
      cancelWorker(workerId)
  })

  // A page refresh can omit Solid cleanup. Cancel open streams on unload also.
  const onBeforeUnload = () => {
    for (const workerId of [...streams.keys()])
      cancelWorker(workerId)
  }
  window.addEventListener('beforeunload', onBeforeUnload)
  onCleanup(() => window.removeEventListener('beforeunload', onBeforeUnload))

  return {
    abortSignalFor: (workerId: string) => streams.get(workerId)?.abort.signal,
  }
}
