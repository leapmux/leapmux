import type { WatchPlan } from './watchPlan'
import type { WatchEventsHandle } from '~/api/workerRpc'
import type { WatchEventsResponse, WatchRejection } from '~/generated/leapmux/v1/workspace_pb'
import type { TabView } from '~/stores/tabView'
import { createEffect, onCleanup } from 'solid-js'
import { channelManager, watchEventsViaChannel } from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { WatchMode } from '~/generated/leapmux/v1/workspace_pb'
import { ChannelError } from '~/lib/channel'
import { emitDevEvent } from '~/lib/devInstrument'
import { createLogger } from '~/lib/logger'
import { createExponentialBackoff } from '~/lib/retry'
import { shouldRetryRejection, watchPlanKey } from './watchPlan'

const log = createLogger('watchEventsStreams')

const REJECTION_RETRY_MAX = 8

interface WorkerStream {
  handle: WatchEventsHandle | null
  pendingPlan: WatchPlan | null
  /** Last ACKED interest key — drain short-circuits when current matches. */
  sentKey: string
  /** Last ACKED FULL agent ids — promotion detection is ack-gated. */
  fullAgents: Set<string>
  /** Plan last put on the wire (open or update); committed on matching ack. */
  inflightPlan: WatchPlan | null
  inflightId: number
  inflightKey: string
  updateId: number
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
  /** Agents that transitioned into FULL — drives loadInitialMessages. */
  onPromoted: (workerId: string, agentIds: string[]) => void
}

/**
 * One long-lived WatchEvents stream per worker hosting placed tabs. Plans are
 * revised in place via InnerStreamRequest; tab switches enqueue synchronously
 * and drain on an async loop (never awaited by callers).
 *
 * Local sentKey/fullAgents advance only on UpdateAck (ack-as-authority), so a
 * LOOKUP_FAILED / unapplied revision cannot leave the client believing FULL.
 */
export function useWatchEventsStreams(opts: UseWatchEventsStreamsOpts): {
  abortSignalFor: (workerId: string) => AbortSignal | undefined
} {
  const streams = new Map<string, WorkerStream>()
  const reconnectBackoff = createExponentialBackoff<string>({
    initialMs: 1000,
    maxMs: 30000,
    multiplier: 2,
    // Desynchronize multi-worker reconnect after a correlated hub blip.
    jitterFactor: 0.2,
  })
  const rejectionBackoff = createExponentialBackoff<string>({
    initialMs: 500,
    maxMs: 15000,
    multiplier: 2,
    jitterFactor: 0.2,
    maxAttempts: REJECTION_RETRY_MAX,
  })

  function getOrCreate(workerId: string): WorkerStream {
    let s = streams.get(workerId)
    if (!s) {
      s = {
        handle: null,
        pendingPlan: null,
        sentKey: '',
        fullAgents: new Set(),
        inflightPlan: null,
        inflightId: 0,
        inflightKey: '',
        updateId: 0,
        closed: false,
        drainScheduled: false,
        opening: false,
        abort: new AbortController(),
      }
      streams.set(workerId, s)
    }
    return s
  }

  /** True when `key` is already the acked interest or is mid-flight on the wire. */
  function interestMatches(s: WorkerStream, key: string): boolean {
    return key === s.sentKey || key === s.inflightKey
  }

  /**
   * Forget the inflight revision and acked-FULL belief so a reopen's ack
   * re-fires onPromoted / catchingUp (a fresh watchSession always replays
   * FULL entities). Called from every teardown path that drops a handle.
   */
  function resetForReconnect(s: WorkerStream): void {
    s.handle = null
    s.inflightPlan = null
    s.inflightKey = ''
    s.fullAgents.clear()
    s.sentKey = ''
  }

  function promotedAgents(prevFull: Set<string>, plan: WatchPlan): string[] {
    const out: string[] = []
    for (const a of plan.agents) {
      if (a.mode === WatchMode.FULL && !prevFull.has(a.agentId))
        out.push(a.agentId)
    }
    return out
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

  /** The socket died, as opposed to the request failing on its merits. */
  function isTransportError(err: unknown): boolean {
    return err instanceof ChannelError && err.source === 'transport'
  }

  /** A refusal no redial can clear -- the relay has latched (see ChannelRelay). */
  function isFatalTransportError(err: unknown): boolean {
    return err instanceof ChannelError && err.fatal
  }

  function reportTransportLoss(workerId: string, err: unknown): void {
    // Suppress only the TOAST for a fatal refusal: the relay latches this
    // state, so "reconnecting..." would be a lie, and the shell already shows a
    // sticky toast naming the real cause (the hub refused US another
    // connection, which says nothing about the worker).
    if (!isFatalTransportError(err))
      showWarnToast('Connection to worker lost, reconnecting\u2026', err)
    // Offline is reported either way, because it is not a health verdict about
    // the worker -- nothing renders it as one. Its only reader is the cleanup
    // effect in useWorkspaceConnection, which patches this worker's READY
    // terminals to DISCONNECTED, drops its ACTIVE agents back to INACTIVE, and
    // clears their streaming text. Skipping that for a fatal close left a
    // half-streamed assistant message rendered as in-flight forever, with
    // nothing left that would ever reconnect and finish it.
    opts.onWorkerOnline(workerId, false)
  }

  /** Commit inflight plan as acked interest; fire onPromoted for new FULL agents. */
  function commitAckedPlan(workerId: string, s: WorkerStream, plan: WatchPlan): void {
    const prevFull = new Set(s.fullAgents)
    s.sentKey = watchPlanKey(plan)
    s.fullAgents = new Set(plan.agents.filter(a => a.mode === WatchMode.FULL).map(a => a.agentId))
    s.inflightPlan = null
    s.inflightKey = ''
    const promoted = promotedAgents(prevFull, plan)
    if (promoted.length > 0)
      opts.onPromoted(workerId, promoted)
  }

  function handleUpdateAck(workerId: string, resp: WatchEventsResponse): boolean {
    const ack = resp.event.case === 'updateAck' ? resp.event.value : undefined
    if (!ack)
      return false
    const s = streams.get(workerId)
    if (!s || s.closed)
      return false

    // Stale ack from a superseded revision — ignore for commit/retry.
    // updateId 0 means unset (tests / older workers); still process those.
    const ackId = Number(ack.updateId)
    if (s.inflightId !== 0 && ackId !== 0 && ackId < s.inflightId)
      return false

    const inflight = s.inflightPlan
    const needsRetry = anyRetryable(ack.rejectedAgents, ack.rejectedTerminals)

    if (inflight && !needsRetry) {
      // Durable rejects (NOT_FOUND, …): still commit — the worker applied what
      // it could; re-stating the same plan loops forever. A later genuine plan
      // key change re-includes the entity if it comes back.
      commitAckedPlan(workerId, s, inflight)
    }
    else if (needsRetry) {
      // LOOKUP_FAILED: drop local "synced" belief so drain will restate.
      s.inflightPlan = null
      s.inflightKey = ''
      s.sentKey = ''
    }

    if (!needsRetry)
      return false
    if (rejectionBackoff.isExhausted(workerId)) {
      log.warn('rejection retry budget exhausted', { workerId })
      return true
    }
    rejectionBackoff.schedule(workerId, () => {
      const current = streams.get(workerId)
      if (!current || current.closed)
        return
      // Always drain the latest coalesced interest — never the ack-time plan.
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
    s.opening = true
    try {
      const nextId = ++s.updateId
      emitDevEvent('leapmux:watch-events-open', () => ({ workerId, updateId: nextId }))
      s.inflightId = nextId
      s.inflightPlan = plan
      s.inflightKey = watchPlanKey(plan)
      const handle = await watchEventsViaChannel(workerId, {
        agents: plan.agents,
        terminals: plan.terminals,
        updateId: BigInt(nextId),
      })
      if (s.closed) {
        handle.close()
        return
      }
      s.handle?.close()
      s.handle = handle

      handle.onEvent((resp) => {
        reconnectBackoff.reset(workerId)
        if (resp.event.case === 'updateAck') {
          // Only reset the rejection budget when this ack settles — a
          // LOOKUP_FAILED retry must keep counting toward the cap.
          if (!handleUpdateAck(workerId, resp))
            rejectionBackoff.reset(workerId)
        }
        else {
          // Do not reset rejectionBackoff on unrelated traffic — a busy
          // sibling entity would otherwise defeat the LOOKUP_FAILED cap.
          opts.onEvent(workerId, resp)
        }
      })
      handle.onEnd(() => {
        if (s.closed)
          return
        resetForReconnect(s)
        scheduleReconnect(workerId)
      })
      handle.onError((err) => {
        if (s.closed)
          return
        if (isTransportError(err))
          reportTransportLoss(workerId, err)
        else
          log.warn('[watchEvents] stream error, retrying:', err)
        // Do not reset backoff on application errors — let it climb.
        resetForReconnect(s)
        scheduleReconnect(workerId, err)
      })

      opts.onWorkerOnline(workerId, true)
    }
    catch (err) {
      log.debug('failed to open watch stream; will retry', { workerId, err })
      if (isTransportError(err))
        reportTransportLoss(workerId, err)
      s.inflightPlan = null
      s.inflightKey = ''
      scheduleReconnect(workerId, err)
    }
    finally {
      s.opening = false
      // A plan parked while we were opening must drain now.
      if (!s.closed && s.pendingPlan)
        scheduleDrain(workerId)
    }
  }

  function sendUpdate(workerId: string, plan: WatchPlan): void {
    const s = getOrCreate(workerId)
    const nextId = ++s.updateId
    s.inflightId = nextId
    s.inflightPlan = plan
    s.inflightKey = watchPlanKey(plan)
    try {
      s.handle!.update({
        agents: plan.agents,
        terminals: plan.terminals,
        updateId: BigInt(nextId),
      })
    }
    catch (err) {
      log.warn('[watchEvents] update send failed; will re-drain', { workerId, err })
      s.inflightPlan = null
      s.inflightKey = ''
      s.pendingPlan = opts.plans().get(workerId) ?? plan
      scheduleDrain(workerId)
    }
  }

  /**
   * Arm the next reopen for `workerId`, unless the failure that closed the
   * stream is one no reopen can clear.
   *
   * `err` is that failure, when there was one — a clean `onEnd` passes none. A
   * fatal ChannelError means ChannelRelay has latched: `ensureWebSocket`
   * rejects every later dial with the same error BEFORE it touches the network,
   * so the retry cycle closes on itself — timer fires, drain reopens, the dial
   * throws the same error, and this arms the timer again. `reconnectBackoff`
   * passes no `maxAttempts`, so that never ends; it is one live timer per
   * worker, forever, and it is the exact case retry.ts's `maxAttempts` doc
   * names. Park the worker instead: no handle and no armed timer.
   *
   * Parking is not a latch of its own. A later genuine interest change still
   * drains once through openStream, which is where a relay that recovered (a
   * fresh login clears ChannelRelay's latch) picks back up — one rejected
   * promise if it has not, rather than a wakeup every 30s.
   */
  function scheduleReconnect(workerId: string, err?: unknown): void {
    const s = streams.get(workerId)
    if (!s || s.closed)
      return
    // Ask the relay as well as this caller's error. onEnd has none to pass -- a
    // worker-side end can race the terminal close -- and a timer armed there
    // would wake 30s later only for openChannelUncached to throw the same
    // latched refusal. Asking the latch covers every caller, including the ones
    // that have no error in hand.
    if (isFatalTransportError(err) || channelManager.fatalCloseInfo())
      return
    if (!s.pendingPlan) {
      const current = opts.plans().get(workerId)
      if (current)
        s.pendingPlan = current
    }
    reconnectBackoff.schedule(workerId, () => {
      if (s.closed)
        return
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

  /** Synchronous enqueue — never awaits channel open. */
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
      // Keep pendingPlan; openStream's finally will re-drain.
      return
    }
    const plan = s.pendingPlan
    if (!plan) {
      if (!s.handle)
        return
      s.handle.close()
      resetForReconnect(s)
      return
    }
    s.pendingPlan = null
    const key = watchPlanKey(plan)
    // Already acked, or the same revision is already on the wire.
    if (s.handle && interestMatches(s, key))
      return
    if (!s.handle) {
      await openStream(workerId, plan)
      return
    }
    sendUpdate(workerId, plan)
  }

  function cancelWorker(workerId: string): void {
    const s = streams.get(workerId)
    if (!s)
      return
    s.closed = true
    s.pendingPlan = null
    // Per-worker reset — must not cancelAll, or a sibling worker's pending
    // reconnect timer is wiped when this worker's last tab leaves.
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
        cancelWorker(workerId)
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

  // Hard refresh: Solid onCleanup may not run; cancel open streams on unload.
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
