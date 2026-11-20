import type { SetStoreFunction } from 'solid-js/store'
import type { ChatStoreState } from './chat.store'
import type { LiveTailTracker } from './chatLiveTail'
import type { HistoryPagePurpose, LandingRefillPages } from './chatTypes'
import type { WatchEventsHandle } from '~/api/workerRpc'
import type { AgentChatMessage, ListAgentMessagesResponse, AgentGoal as ProtoAgentGoal, AgentGoalAction as ProtoAgentGoalAction, BackgroundTaskItem as ProtoBackgroundTaskItem, TodoItem as ProtoTodoItem } from '~/generated/proto/leapmux/v1/agent_pb'
import { batch, untrack } from 'solid-js'
import { workerAgent } from '~/api/workerRpc'
import { CATCH_UP_GAP_LIMIT, MESSAGE_PAGE_LIMIT } from '~/generated/contracts/chat-history'
import { MessagePageAnchor } from '~/generated/proto/leapmux/v1/agent_pb'
import { lowerBoundBySeq } from '~/lib/binarySearch'
import { clamp } from '~/lib/clamp'
import { exceedsCatchUpGapLimit } from './chatLiveTail'
import { isReapablePhantom, mergeWindow } from './chatMessageOrder'

/** Approximate the number of rows for a viewport. A landing can contain hidden rows. */
const ESTIMATED_LANDING_ROW_HEIGHT_PX = 48
const MIN_LANDING_PAGE_LIMIT = 8
const DEFAULT_LANDING_PAGE_LIMIT = 16

export function landingPageLimit(viewportHeight: number | undefined, pageLimit: number = MESSAGE_PAGE_LIMIT): number {
  const estimate = viewportHeight === undefined || !Number.isFinite(viewportHeight) || viewportHeight <= 0
    ? DEFAULT_LANDING_PAGE_LIMIT
    : Math.ceil(viewportHeight / ESTIMATED_LANDING_ROW_HEIGHT_PX)
  return clamp(estimate, Math.min(MIN_LANDING_PAGE_LIMIT, pageLimit), pageLimit)
}

/**
 * Max forward-fill rounds before forwardFillToLiveTail settles regardless, so a
 * genuinely-vanished live seq stops the loop instead of spinning.
 */
const MAX_FORWARD_FILL_PAGES = 4
const MAX_CATCH_UP_PAGES = Math.ceil(Number(CATCH_UP_GAP_LIMIT) / MESSAGE_PAGE_LIMIT)
const MAX_INT64_SEQ = 9223372036854775807n

/**
 * Abort `controller` when `watchSignal` fires. Aborts immediately if the signal is
 * already aborted. The signal has two sources:
 *
 * - The WatchEvents subscription, so a reconcile-driven fetch dies with the subscription
 *   that spawned it (a workspace switch / worker change tears the stream down -- the fetch
 *   must stop running against a worker the reader navigated away from, instead of leaking
 *   until its pages reject). The two background forward-fill loops (catchUpToTail,
 *   resumeDeferredTailFill) and the empty-window re-anchor (jumpToLatestMessages) pass it.
 * - A history jump of the reader, through beginHistoryFetch in createHistoryFetchRunner. The
 *   scroll hook aborts the jump when the reader gives another command or takes control of
 *   the view.
 */
export function linkWatchSignal(controller: AbortController, watchSignal?: AbortSignal): () => void {
  if (!watchSignal)
    return () => {}
  if (watchSignal.aborted) {
    controller.abort()
    return () => {}
  }
  const onWatchAbort = () => controller.abort()
  watchSignal.addEventListener('abort', onWatchAbort, { once: true })
  let removed = false
  const cleanup = () => {
    if (removed)
      return
    removed = true
    watchSignal.removeEventListener('abort', onWatchAbort)
  }
  // Drop the watchSignal listener once THIS controller settles by being aborted --
  // the common case, since a superseding fetch aborts the prior controller
  // (beginHistoryFetch / catchUpToTail both abort the one they replace). Without
  // this, every reconcile-driven fetch over one long-lived WatchEvents subscription
  // would leave its abort listener on the shared `watchSignal` until the subscription
  // tears down, accumulating one per fetch for the session. The `{ once: true }` on
  // the watchSignal side still covers the teardown case (the listener fires and
  // self-removes); this covers the supersede/abort case so only the CURRENT
  // controller's listener is ever live.
  controller.signal.addEventListener(
    'abort',
    cleanup,
    { once: true },
  )
  return cleanup
}

type SurvivesHistoryRead = (message: Pick<AgentChatMessage, 'seq'>) => boolean

interface PendingHistoryRead {
  retractedSeqs: Set<bigint>
  reapedRanges: Array<{ latestSeq: bigint, reapCeilingSeq: bigint }>
}

/**
 * Create the runner of the history fetches: the initial load, the pagination, and the
 * jumps. It owns the supersession for each agent. A new history fetch aborts the previous
 * history fetch and the background catch-up loop of the agent.
 *
 * The store builds one runner. It passes runHistoryFetch to createHistoryPaginator, and
 * calls forget when the agent closes. The paginator tests build the same runner, so they
 * run the supersession and the flag rules of production.
 */
export function createHistoryFetchRunner(deps: Pick<HistoryPaginatorDeps, 'setState' | 'catchUpAbort'>) {
  const { setState, catchUpAbort } = deps

  /**
   * Retain one controller for each agent's active history fetch.
   * A new fetch aborts the previous controller so a delayed request cannot prevent further pagination.
   * The remote procedure call can still finish. Its caller checks signal.aborted after awaiting the result and discards a superseded response.
   */
  const fetchAbort = new Map<string, AbortController>()
  const fetchWatchCleanup = new Map<string, () => void>()

  // An aborted request can still resolve. Each pending read keeps its fence
  // until its own body finishes, independently of its supersession controller.
  const pendingReads = new Map<string, Set<PendingHistoryRead>>()

  async function duringHistoryRead<T>(agentId: string, read: (survives: SurvivesHistoryRead) => Promise<T>): Promise<T> {
    let reads = pendingReads.get(agentId)
    if (!reads) {
      reads = new Set()
      pendingReads.set(agentId, reads)
    }
    const pending: PendingHistoryRead = {
      retractedSeqs: new Set(),
      reapedRanges: [],
    }
    reads.add(pending)
    try {
      return await read(({ seq }) => !pending.retractedSeqs.has(seq)
        && !pending.reapedRanges.some(range => isReapablePhantom(seq, range.latestSeq, range.reapCeilingSeq)))
    }
    finally {
      reads.delete(pending)
      if (reads.size === 0 && pendingReads.get(agentId) === reads)
        pendingReads.delete(agentId)
    }
  }

  function noteRetractions(agentId: string, sequences: ReadonlySet<bigint>): void {
    for (const pending of pendingReads.get(agentId) ?? []) {
      for (const seq of sequences)
        pending.retractedSeqs.add(seq)
    }
  }

  /** Fence an authoritative absence range even when the window holds none of its rows. */
  function noteReapedRange(agentId: string, latestSeq: bigint, reapCeilingSeq: bigint): void {
    if (latestSeq >= reapCeilingSeq)
      return
    const range = { latestSeq, reapCeilingSeq }
    for (const pending of pendingReads.get(agentId) ?? [])
      pending.reapedRanges.push(range)
  }

  /**
   * Start a new history fetch for agentId.
   * Abort the previous history fetch and background catch-up loop.
   * Install a new controller and reset both fetch-direction flags. The caller then sets its own flag.
   * Return the new signal. The caller must discard a result after that signal aborts.
   * runHistoryFetch clears the flag only while its controller still owns the fetch.
   *
   * An optional abortSignal ends this fetch from outside. It has two sources:
   * - The current WatchEvents subscription, for the reconciliation request for the latest page.
   *   A workspace switch or worker change then aborts that request.
   * - A history jump of the reader (End, Home, a seek, a send). The scroll hook aborts it
   *   when the reader gives another command or takes control of the view.
   * A fetch that pagination starts passes none, because its active tab already supplies its scope.
   */
  function beginHistoryFetch(agentId: string, abortSignal?: AbortSignal): AbortSignal {
    fetchAbort.get(agentId)?.abort()
    fetchWatchCleanup.get(agentId)?.()
    fetchWatchCleanup.delete(agentId)
    // A user fetch supersedes background catch-up.
    // Abort its pending request before the user's jump or scroll changes the window.
    catchUpAbort.get(agentId)?.abort()
    const controller = new AbortController()
    fetchAbort.set(agentId, controller)
    fetchWatchCleanup.set(agentId, linkWatchSignal(controller, abortSignal))
    setState('fetchingOlder', agentId, false)
    setState('fetchingNewer', agentId, false)
    // A user fetch clears any deferred automatic fill.
    // Its result selects the window, or the user scrolls away from the tail.
    // forwardFillToLiveTail sets the deferral again only if its own fill reaches the maximum attempt count while still advancing.
    setState('tailFillDeferred', agentId, false)
    return controller.signal
  }

  /**
   * Run a new history fetch for agentId and set its direction flag.
   * Pass the new signal to body. The body must discard a result after signal.aborted becomes true.
   * In finally, clear the flag only while this controller still owns the fetch.
   * A superseding fetch owns its own flags.
   * Resolve what body resolved.
   */
  async function runHistoryFetch<T>(
    agentId: string,
    flag: 'fetchingOlder' | 'fetchingNewer',
    body: (signal: AbortSignal, survives: SurvivesHistoryRead) => Promise<T>,
    abortSignal?: AbortSignal,
  ): Promise<T> {
    const signal = batch(() => {
      const signal = beginHistoryFetch(agentId, abortSignal)
      setState(flag, agentId, true)
      return signal
    })
    try {
      return await duringHistoryRead(agentId, survives => body(signal, survives))
    }
    catch (error) {
      // A failed split seek can still own a sibling request.
      const controller = fetchAbort.get(agentId)
      if (controller?.signal === signal)
        controller.abort()
      throw error
    }
    finally {
      // Clear the flag only while this controller still owns the fetch.
      // A newer fetch installs another controller and resets both flags, so this request must leave those newer flags unchanged.
      // An aborted abortSignal can end a fetch without starting another fetch.
      // This controller still owns the flag in that case, so clear it.
      // Otherwise, fetchingNewer could remain true and prevent all newer pagination until another user fetch resets it.
      if (fetchAbort.get(agentId)?.signal === signal) {
        fetchWatchCleanup.get(agentId)?.()
        fetchWatchCleanup.delete(agentId)
        fetchAbort.delete(agentId)
        setState(flag, agentId, false)
      }
    }
  }

  /**
   * Abort the history fetch of agentId, and remove its controller and its link to the
   * abort signal. A delayed response then cannot change the window of a closed agent.
   * When the aborted fetch settles, it leaves the flags unchanged, because its controller
   * no longer owns the fetch. The store deletes the flags of a closed agent.
   * The catch-up controllers belong to the caller, so the caller aborts them.
   */
  function forget(agentId: string): void {
    fetchAbort.get(agentId)?.abort()
    fetchAbort.delete(agentId)
    fetchWatchCleanup.get(agentId)?.()
    fetchWatchCleanup.delete(agentId)
  }

  return {
    runHistoryFetch,
    duringHistoryRead,
    noteRetractions,
    noteReapedRange,
    forget,
  }
}

/**
 * One ascending page of messages with seq > cursorSeq -- the AFTER-anchored forward
 * pagination request shared by catchUpToTail, loadNewerPage, and
 * jumpToLatestMessages' fill/retry loops, so the four forward-fetch sites can't
 * drift on the anchor + page-size wiring.
 */
function listMessagesAfter(workerId: string, agentId: string, cursorSeq: bigint, signal: AbortSignal, limit: number = MESSAGE_PAGE_LIMIT) {
  return workerAgent.listAgentMessages(workerId, {
    agentId,
    anchor: MessagePageAnchor.AFTER,
    cursorSeq,
    limit,
  }, { signal })
}

/**
 * The store surface the history paginator reaches into, threaded explicitly rather
 * than via a `this`-bound store object. Splits into three groups: the reactive
 * state + setter, the windowing-core fetch helpers (captured closures in the
 * store), and the cross-method store API (getLast/FirstSeq, the trims, addMessage,
 * ...). Listed here so the paginator's coupling to the rest of the store is visible
 * and a new dependency is a deliberate addition, not a silent reach.
 */
export interface HistoryPaginatorDeps {
  state: ChatStoreState
  setState: SetStoreFunction<ChatStoreState>
  /** Per-agent catch-up abort controllers (separate from the user-fetch fetchAbort). */
  catchUpAbort: Map<string, AbortController>
  /**
   * Run a body under the supersession machinery, toggling the fetching flag.
   * `abortSignal` ends the fetch from outside (see beginHistoryFetch in
   * createHistoryFetchRunner): the WatchEvents subscription for a reconcile-driven fetch,
   * or the history jump of the reader. A fetch that pagination starts omits it. Resolves
   * what the body resolved.
   */
  runHistoryFetch: <T>(
    agentId: string,
    flag: 'fetchingOlder' | 'fetchingNewer',
    body: (signal: AbortSignal, survives: SurvivesHistoryRead) => Promise<T>,
    abortSignal?: AbortSignal,
  ) => Promise<T>
  duringHistoryRead: <T>(agentId: string, read: (survives: SurvivesHistoryRead) => Promise<T>) => Promise<T>
  retractMessages: (agentId: string, sequences: readonly bigint[], handle?: WatchEventsHandle) => boolean
  needsLatestAnchor: (agentId: string) => boolean
  completeLatestAnchor: (agentId: string) => void
  mergeFetchedMessages: (agentId: string, fetched: AgentChatMessage[], side: 'older' | 'newer') => void
  applyMessages: (agentId: string, messages: AgentChatMessage[], hasMore: boolean) => void
  setLandingRefillPages: (agentId: string, pages: LandingRefillPages) => void
  /** The live-tail tracker: recorded-tail reads + the settle/empty-reset clamps. */
  liveTail: LiveTailTracker
  /** Window cap for the live-tail-following state (MAX_LOADED_CHAT_MESSAGES). */
  maxLoaded: number
  /** Raised window cap for a scrolled-away reader's buffer (MAX_LOADED_CHAT_MESSAGES_CEILING). */
  maxLoadedCeiling: number
  // --- cross-method store API ---
  getFirstSeq: (agentId: string) => bigint
  getLastSeq: (agentId: string) => bigint
  /**
   * The window's first and last persisted sequence, or `undefined` when the window
   * is empty. The re-anchor guards read this honest pair, because getFirstSeq and
   * getLastSeq above collapse an empty window to the 0n that a cursor reader wants.
   */
  getFirstMessageSeq: (agentId: string) => bigint | undefined
  getLastMessageSeq: (agentId: string) => bigint | undefined
  caughtUpToLiveTail: (agentId: string) => boolean
  getViewportHeight: (agentId: string) => number | undefined
  addMessagePage: (agentId: string, messages: readonly AgentChatMessage[]) => void
  trimOldestEnd: (agentId: string, maxCount: number) => void
  trimNewestEnd: (agentId: string, maxCount: number) => void
  replaceTodos: (agentId: string, protoTodos: ProtoTodoItem[]) => void
  replaceBackgroundTasks: (agentId: string, protoTasks: ProtoBackgroundTaskItem[]) => void
  replaceGoal: (agentId: string, goal: ProtoAgentGoal | undefined, supportedActions: ProtoAgentGoalAction[], updatedAt: string) => void
  markBackgroundTasksLoadFailed: (agentId: string) => void
}

/**
 * History paginator: the windowed fetch + jump methods (initial load, older/newer
 * pages, catch-up, forward-fill, jump-to-latest/oldest). Extracted from the store
 * into its own factory so the fetch/abort/merge state machine can be reasoned about
 * and tested without standing up the whole reactive store; everything it touches is
 * the explicit `deps` object (see HistoryPaginatorDeps) rather than a `this`-bound
 * store. The sibling jump/fill methods call each other directly as closures, so they
 * are NOT part of deps -- only genuinely-external store methods are.
 */
export function createHistoryPaginator(deps: HistoryPaginatorDeps) {
  const { state, setState } = deps

  // Agents whose tail-reconcile (catchUpToTail) is currently draining the window->live
  // gap. The continuous reconcile effect can re-kick on every window/live-tail mutation
  // (including catchUpToTail's own per-page addMessage writes); this in-flight guard
  // makes a re-kick a no-op so the loop is never aborted and its in-flight page re-issued
  // (RPC thrash). A user fetch still supersedes the loop via catchUpAbort/beginHistoryFetch.
  const catchingUp = new Set<string>()

  function snapshotLoadedIdentities(agentId: string): ReadonlyMap<string, bigint> {
    return untrack(() => new Map((state.messagesByAgent[agentId] ?? []).map(message => [message.id, message.seq])))
  }

  /**
   * Apply a LATEST page's body + its authoritative to-do list + background-task
   * registry + goal -- the shared tail of loadInitialMessages and
   * jumpToLatestMessages. Applies the messages, then each snapshot the page
   * carries. The worker omits a snapshot it could not read, so an absent one
   * leaves the current list, registry or goal alone instead of wiping it.
   *
   * A failed REGISTRY load is also recorded, because its section is hidden when
   * it is empty: without the flag a worker that cannot answer looks exactly like
   * an agent that has run no background tasks, and the section vanishes with
   * nothing on screen to say why. The to-do list needs no equivalent -- an
   * empty to-do list does not hide its section.
   */
  function applyLatestPage(
    agentId: string,
    resp: ListAgentMessagesResponse,
    survives: SurvivesHistoryRead,
    previous?: ReadonlyMap<string, bigint>,
  ): void {
    const messages = resp.messages.filter(survives)
    // A cold load or deletion re-anchor can run while the watch adds rows.
    // The page may predate those arrivals, including a row's latest sequence.
    const arrivals = previous === undefined
      ? []
      : (state.messagesByAgent[agentId] ?? []).filter(message =>
          message.seq > 0n && previous.get(message.id) !== message.seq && survives(message))
    deps.completeLatestAnchor(agentId)
    deps.applyMessages(agentId, mergeWindow(messages, arrivals, 'newer'), resp.hasMore)
    if (resp.todos)
      deps.replaceTodos(agentId, resp.todos.todos)
    if (resp.backgroundTasks)
      deps.replaceBackgroundTasks(agentId, resp.backgroundTasks.tasks)
    else
      deps.markBackgroundTasksLoadFailed(agentId)
    // Only on a successful read. A CHILD agent answers a present snapshot with
    // an ABSENT goal -- a subagent never owns one -- so this write is what
    // clears a goal a tab inherited from a previous root, and skipping it on a
    // DB error is what stops a transient failure from blanking a live card.
    if (resp.goal)
      deps.replaceGoal(agentId, resp.goal.goal, resp.goal.supportedActions, resp.goal.goalUpdatedAt)
  }

  /** Fetch the latest messages for an agent (initial page load). */
  async function loadInitialMessages(workerId: string, agentId: string): Promise<void> {
    if (untrack(() => state.initialLoadComplete[agentId] || state.fetchingOlder[agentId] || state.fetchingNewer[agentId]))
      return
    // Route through the supersession machinery (not a bare fetchingOlder flag)
    // so a jump-to-latest fired mid-load aborts this one. Otherwise this fetch
    // could resolve last and clobber the jump's fresher window with the stale
    // initial page.
    await deps.runHistoryFetch(agentId, 'fetchingOlder', async (signal, survives) => {
      const previous = snapshotLoadedIdentities(agentId)
      let resp
      try {
        resp = await workerAgent.listAgentMessages(workerId, {
          agentId,
          anchor: MessagePageAnchor.LATEST,
          limit: MESSAGE_PAGE_LIMIT,
        }, { signal })
      }
      catch (err) {
        // A REJECTED cold start is the other way the registry cannot be read,
        // and `applyLatestPage` never runs for it. Without this the flag stays
        // false and the Background tasks section leaves the screen with nothing
        // to say why -- the same failure an absent in-band registry snapshot
        // reports, arriving through the door next to it.
        //
        // Not for an ABORTED fetch: that tab is going away or a fresher fetch
        // superseded this one, and the superseding fetch writes the real answer.
        if (signal.aborted)
          return
        deps.markBackgroundTasksLoadFailed(agentId)
        // Re-thrown, so the caller's own "Failed to load chat history" toast
        // still fires. This records a fact; it does not handle the error.
        throw err
      }
      if (signal.aborted)
        return
      // The server ships the authoritative to-do list on the cold-start (LATEST)
      // page; subsequent live updates arrive via todos_changed broadcasts.
      applyLatestPage(agentId, resp, survives, previous)
    })
  }

  /**
   * Reconcile loaded rows after a period without FULL history delivery. Each
   * valid page proves that any missing sequence in its covered range is absent.
   * The watch owns this read independently of the reader's paging.
   */
  async function refreshLoadedMessages(
    workerId: string,
    agentId: string,
    signal: AbortSignal,
    handle?: WatchEventsHandle,
    recovery?: { mayReplace: () => boolean, pageLimit?: number | undefined },
  ): Promise<void> {
    if (signal.aborted)
      return
    const captured = (state.messagesByAgent[agentId] ?? []).filter(message => message.seq > 0n).map(({ id, seq }) => ({ id, seq }))
    const first = captured[0]
    const last = captured.at(-1)
    if (!deps.needsLatestAnchor(agentId) && first && last) {
      let cursor = first.seq - 1n
      let nextCaptured = 0
      try {
        while (!signal.aborted && !deps.needsLatestAnchor(agentId)) {
          const firstLoaded = deps.getFirstMessageSeq(agentId)
          const lastLoaded = deps.getLastMessageSeq(agentId)
          if (firstLoaded === undefined || lastLoaded === undefined)
            break
          while (nextCaptured < captured.length && captured[nextCaptured]!.seq < firstLoaded)
            nextCaptured++
          const next = captured[nextCaptured]
          if (!next || next.seq > lastLoaded)
            break
          const coveredStart = next.seq - 1n > cursor
            ? next.seq - 1n
            : cursor
          cursor = coveredStart
          const done = await deps.duringHistoryRead(agentId, async (survives) => {
            const limit = Math.min(MESSAGE_PAGE_LIMIT, captured.length - nextCaptured)
            const resp = await listMessagesAfter(workerId, agentId, coveredStart, signal, limit)
            if (signal.aborted)
              return true
            const bySeq = new Map<bigint, AgentChatMessage>()
            for (const message of resp.messages) {
              if (message.seq <= cursor)
                throw new Error('The message revision page repeats or misorders a sequence.')
              cursor = message.seq
              bySeq.set(message.seq, message)
            }
            if (resp.hasMore && cursor === coveredStart)
              throw new Error('The message revision page does not advance its cursor.')
            const coveredEnd = resp.hasMore && cursor < last.seq
              ? cursor
              : last.seq
            const current = state.messagesByAgent[agentId] ?? []
            const removals: bigint[] = []
            const updates: AgentChatMessage[] = []
            while (nextCaptured < captured.length) {
              const snapshot = captured[nextCaptured]!
              if (snapshot.seq > coveredEnd)
                break
              nextCaptured++
              if (snapshot.seq <= coveredStart || !survives(snapshot))
                continue
              const loaded = current[lowerBoundBySeq(current, snapshot.seq)]
              const currentId = loaded?.seq === snapshot.seq
                ? loaded.id
                : undefined
              if (currentId !== undefined && currentId !== snapshot.id)
                continue
              const stored = bySeq.get(snapshot.seq)
              if (stored === undefined)
                removals.push(snapshot.seq)
              else if (currentId === stored.id)
                updates.push(stored)
            }
            batch(() => {
              deps.retractMessages(agentId, removals, handle)
              deps.mergeFetchedMessages(agentId, updates, 'newer')
            })
            return !resp.hasMore || cursor >= last.seq
          })
          if (done)
            break
        }
      }
      catch (err) {
        if (!signal.aborted)
          throw err
      }
    }
    if (!signal.aborted && deps.needsLatestAnchor(agentId)) {
      await jumpToLatestMessages(workerId, agentId, signal, recovery ?? {
        mayReplace: () => true,
      })
    }
  }

  /** Fetch older messages before the current window. */
  async function loadOlderMessages(workerId: string, agentId: string, purpose?: HistoryPagePurpose): Promise<void> {
    if (state.fetchingOlder[agentId])
      return
    if (!state.hasMoreOlder[agentId])
      return
    const messages = state.messagesByAgent[agentId]
    if (!messages || messages.length === 0)
      return

    // Page before the oldest persisted message. A missing cursor violates the
    // nonempty-window invariant, so reload from the start.
    const firstSeq = deps.getFirstMessageSeq(agentId)
    if (firstSeq === undefined) {
      // Reload the earliest page so an invalid cursor cannot keep the control active.
      await jumpToOldestMessages(workerId, agentId)
      return
    }
    await deps.runHistoryFetch(agentId, 'fetchingOlder', async (signal, survives) => {
      const resp = await workerAgent.listAgentMessages(workerId, {
        agentId,
        anchor: MessagePageAnchor.BEFORE,
        cursorSeq: firstSeq,
        limit: paginationLimit(agentId, purpose),
      }, { signal }).catch((error: unknown) => {
        if (!signal.aborted)
          throw error
        return undefined
      })
      if (!resp || signal.aborted)
        return
      deps.mergeFetchedMessages(agentId, resp.messages.filter(survives), 'older')
      setState('hasMoreOlder', agentId, resp.hasMore)
      // We just prepended older history; cap the window by trimming the newest
      // end so deep scroll-up doesn't grow memory unbounded. Cap to the CEILING,
      // not the base: a scrolled-up reader is pre-fetching a visible buffer above,
      // and in a hidden-heavy stretch that needs many raw rows -- the viewport is
      // near the oldest end here, so the kept oldest-CEILING rows are exactly the
      // reader's buffer; the far tail (re-fetched by loadNewerPage on scroll-down)
      // is what trims. Returning to the live tail trims back to the base.
      //
      // At the live tail (hasMoreNewer false) the far tail being trimmed IS the live
      // tail, so this flips hasMoreNewer=true -- correct for an EXPLICIT older-load
      // (the scroll-to-bottom affordance then surfaces a clear path back to live). The
      // dangerous case -- the buffer-filler PRE-FETCHING older at the ceiling and
      // silently dropping the live tail -- is headed off upstream: the filler stops
      // paging older once the window is at the ceiling on the live tail (atWindowCeiling
      // gating in createScrollBufferFiller), so only a deliberate user action reaches
      // here at the ceiling.
      deps.trimNewestEnd(agentId, deps.maxLoadedCeiling)
    })
  }

  /**
   * Run a single-flight background tail loop for `agentId` under a dedicated, abortable
   * controller -- the scaffolding catchUpToTail and resumeDeferredTailFill share. No-op
   * when a loop is already draining this agent (the continuous reconcile is idempotent;
   * a running loop keeps fetching while the gap grows, so a re-kick would only re-issue
   * the in-flight page). Otherwise it supersedes any prior controller, ties the new one
   * to the WatchEvents `watchSignal` (so a workspace switch / worker change aborts it
   * rather than leaking a fetch against a navigated-away worker), and frees the
   * single-flight slot the INSTANT it aborts -- not only when the aborted await later
   * resumes into the finally -- so a reconcile re-kick fired in that window isn't dropped
   * (it self-heals next tick, but needlessly). The finally releases the slot + controller
   * on the normal completion path (delete is idempotent), and only clears catchUpAbort
   * when this controller is still the current one.
   *
   * `body` receives the controller's signal and the recorded live tail snapshotted BEFORE
   * it runs, so an exhaustion settle can clamp a STRANDED tail to the window while still
   * preserving a seq a broadcast raised mid-loop (settleToWindow's guard skips a tail that
   * advanced past the entry snapshot). The one home for the abort / slot / teardown
   * contract, so it can't drift between the two loops.
   */
  async function runSingleFlightTailLoop(
    agentId: string,
    watchSignal: AbortSignal | undefined,
    body: (signal: AbortSignal, liveSeqAtEntry: bigint, survives: SurvivesHistoryRead) => Promise<void>,
  ): Promise<void> {
    if (catchingUp.has(agentId))
      return
    catchingUp.add(agentId)
    deps.catchUpAbort.get(agentId)?.abort()
    const controller = new AbortController()
    deps.catchUpAbort.set(agentId, controller)
    const cleanupWatchSignal = linkWatchSignal(controller, watchSignal)
    controller.signal.addEventListener('abort', () => catchingUp.delete(agentId), { once: true })
    // Snapshot the recorded live tail BEFORE the body (no await precedes this), so the
    // exhaustion settle can clamp a stranded tail while preserving a mid-loop broadcast.
    const liveSeqAtEntry = deps.liveTail.get(agentId)
    try {
      await deps.duringHistoryRead(agentId, survives => body(controller.signal, liveSeqAtEntry, survives))
    }
    catch (error) {
      if (!controller.signal.aborted)
        throw error
    }
    finally {
      cleanupWatchSignal()
      // Release the single-flight slot AND the controller together, but only if a superseding
      // loop hasn't already replaced this controller. On an abort-driven supersession the abort
      // listener above frees the `catchingUp` slot INSTANTLY (so a reconcile re-kick fired in that
      // window isn't dropped), and a newer loop may already own the slot by the time this aborted
      // body resumes into the finally. Deleting `catchingUp` unconditionally here would clobber
      // that newer loop's slot -- the next reconcile tick then passes the `catchingUp.has` guard,
      // starts a THIRD loop, and aborts the newer loop's in-flight page: exactly the abort-and-
      // re-issue RPC thrash the single-flight guard exists to prevent. Gating the slot release on
      // still owning the controller keeps the two in lock-step.
      if (deps.catchUpAbort.get(agentId) === controller) {
        catchingUp.delete(agentId)
        deps.catchUpAbort.delete(agentId)
      }
    }
  }

  /**
   * Fetch messages forward from a given sequence while the authoritative gap
   * fits in the live-tail window. A response that identifies a larger gap stops
   * the drain and records the tail. The scheduler chooses whether to retain the window or re-anchor.
   *
   * No-op when the window is scrolled away from the live tail (hasMoreNewer):
   * refilling the tail would defeat the bidirectional window. The live deltas
   * are recovered later via loadNewerPage on scroll-down or
   * jumpToLatestMessages, both of which forward-fetch to the true tail.
   *
   * hasMoreNewer is re-checked every iteration, not just on entry: a concurrent
   * older-history load can trim the newest end and flip it mid-loop, after which
   * addMessage drops these forward messages anyway (the live-append guard) -- so
   * stop and let the scroll-down / jump-to-latest paths re-fetch the tail.
   *
   * Supersession: the loop runs under a dedicated catchUpAbort controller (NOT
   * fetchAbort), linked to the WatchEvents `watchSignal`. A user-driven forward
   * fetch (jumpToLatestMessages / loadNewerPage) aborts it via beginHistoryFetch,
   * so an in-flight page is discarded the moment the user takes over the tail --
   * the two never race on the window.
   *
   * Trim each page as it lands: the OLDEST end to the CEILING, not the
   * base, to match loadNewerPage. Capping to the ceiling instead of the base
   * is what spares a SCROLLED-UP reader: the loop runs while at the live tail
   * (hasMoreNewer false), but the reader can be scrolled up into a ceiling-grown older
   * buffer, and a base trim would reap that buffer (and rows below their anchor). The
   * per-append viewport trim (trimOldestToViewport) brings a FOLLOWED tail back to the
   * lean base after the replay; the layout shift of the virtualizer keeps a scrolled-up
   * reader's row stationary throughout.
   */
  async function catchUpToTail(
    workerId: string,
    agentId: string,
    afterSeq: bigint,
    watchSignal?: AbortSignal,
    replay?: { pageLimit: number | undefined, skipped: boolean },
  ): Promise<void> {
    // Idempotent under the continuous reconcile effect (runSingleFlightTailLoop no-ops
    // while a loop drains this agent). The loop fetches while resp.hasMore and
    // the authoritative gap fits in the live-tail window. A user fetch still
    // supersedes it through catchUpAbort and beginHistoryFetch.
    await runSingleFlightTailLoop(agentId, watchSignal, async (signal, liveSeqAtEntry, survives) => {
      let cursor = afterSeq
      // A sized WatchEvents replay that sent transcript rows spent the first page.
      const replayPageLimit = replay?.pageLimit
      const pages = replayPageLimit === undefined || replay?.skipped
        ? MAX_CATCH_UP_PAGES
        : 1
      for (let count = 0; count < pages && !signal.aborted && !state.hasMoreNewer[agentId]; count++) {
        const resp = await listMessagesAfter(workerId, agentId, cursor, signal, replayPageLimit)
        // Superseded mid-flight (a user jump/scroll, or subscription teardown):
        // discard this page so the superseding fetch owns the tail.
        if (signal.aborted)
          return
        const latestSeq = resp.latestSeq
        // Hand an over-limit gap to the reconciler before applying an intermediate page.
        if (state.hasMoreNewer[agentId] || (latestSeq !== undefined && exceedsCatchUpGapLimit(latestSeq, deps.getLastSeq(agentId), replayPageLimit))) {
          if (latestSeq !== undefined)
            deps.liveTail.bump(agentId, latestSeq)
          return
        }
        batch(() => {
          deps.addMessagePage(agentId, resp.messages.filter(survives))
          if (latestSeq !== undefined)
            deps.liveTail.bump(agentId, latestSeq)
          // A reader above the tail keeps the older buffer up to the window ceiling.
          deps.trimOldestEnd(agentId, deps.maxLoadedCeiling)
        })
        if (!resp.hasMore || resp.messages.length === 0)
          break
        if (count === pages - 1) {
          finishTailFill(agentId, true, true)
          return
        }
        cursor = resp.messages.at(-1)!.seq
      }
      // The loop drained the forward pages. A remaining gap points at a sequence
      // that the server cannot return. Clamp that stale tail to the window, or
      // reset it when the window is empty. This prevents repeated empty fetches.
      if (!signal.aborted && !state.hasMoreNewer[agentId] && !deps.caughtUpToLiveTail(agentId)) {
        const windowTail = deps.getLastSeq(agentId)
        if (windowTail > 0n)
          deps.liveTail.settleToWindow(agentId, liveSeqAtEntry, windowTail)
        else
          deps.liveTail.resetToEmptyIfStale(agentId, liveSeqAtEntry)
      }
    })
  }

  /**
   * Fetch a single page of newer messages (scroll-down pagination). No-op
   * unless the window is scrolled away from the tail (hasMoreNewer). Appends
   * the page, clears hasMoreNewer when the tail is reached (has_more false),
   * then trims the oldest end to cap the window. The single-page sibling of the
   * looping catchUpToTail; named ...Page to keep the two from being confused.
   */
  async function loadNewerPage(workerId: string, agentId: string, purpose?: HistoryPagePurpose): Promise<void> {
    if (state.fetchingNewer[agentId])
      return
    if (!state.hasMoreNewer[agentId])
      return
    const lastSeq = deps.getLastMessageSeq(agentId)
    if (lastSeq === undefined) {
      // Reload the latest page so an empty window cannot keep the control active.
      await jumpToLatestMessages(workerId, agentId)
      return
    }
    // Snapshot the recorded live tail BEFORE the await. The dedup-stall clamp
    // below settles latestLiveSeq down to the window tail, but a live message
    // broadcast DURING our fetch raises latestLiveSeq (bumpLatestLiveSeq runs in
    // addMessage before the beyondTail drop) to a seq the server genuinely has.
    // Clamping that away would strand the message with the view claiming to be
    // at the tail, so the clamp only fires when latestLiveSeq has NOT advanced
    // since entry -- a still-recorded seq the server can no longer give us.
    const liveSeqAtEntry = deps.liveTail.get(agentId)
    await deps.runHistoryFetch(agentId, 'fetchingNewer', async (signal, survives) => {
      const resp = await listMessagesAfter(workerId, agentId, lastSeq, signal, paginationLimit(agentId, purpose)).catch((error: unknown) => {
        if (!signal.aborted)
          throw error
        return undefined
      })
      if (!resp || signal.aborted)
        return
      deps.mergeFetchedMessages(agentId, resp.messages.filter(survives), 'newer')
      // Reaching the persisted tail (has_more false) does not prove we reached the
      // live tail. A broadcast that arrived mid-fetch (seq beyond this page) was
      // dropped by addMessage's live-append guard and only recorded in
      // latestLiveSeq. Keep hasMoreNewer set until the window actually reaches
      // that observed seq, so a further scroll-down (or jump-to-latest) fetches
      // the gap instead of stranding the missed message with the view claiming
      // to be at the tail (mirrors jumpToLatestMessages' gap-free terminal).
      //
      // BUT a page that reaches the server tail (has_more false) WITHOUT
      // advancing the window (everything in it already present -- a dedup-stall)
      // means latestLiveSeq records a seq the server can no longer give us: a
      // message broadcast then deleted, or an otherwise vanished gap. Left alone,
      // caughtUpToLiveTail can never become true and hasMoreNewer wedges on
      // forever -- the scroll-to-bottom affordance stays lit and the streaming
      // tail stays hidden. settleLiveTailToWindow clamps latestLiveSeq down to
      // the real tail so we settle -- but skips a seq that advanced mid-fetch
      // (a genuinely-reachable broadcast). A genuinely-reachable live message
      // otherwise either keeps has_more true or advances the window, so it is
      // untouched by this.
      //
      // The dedup-stall is EXACTLY `getLastSeq === lastSeq` (the window neither
      // advanced nor shrank). A `< lastSeq` is NOT a dedup-stall but a window that
      // SHRANK during the fetch: mergeFetchedMessages('newer') only ever appends, so
      // a concurrent window replacement can also lower the tail. Clamping on a shrink
      // would erase a still-reachable recorded tail, so a later forward fill reconciles it.
      if (!resp.hasMore && deps.getLastSeq(agentId) === lastSeq)
        deps.liveTail.settleToWindow(agentId, liveSeqAtEntry, deps.getLastSeq(agentId))
      setState('hasMoreNewer', agentId, resp.hasMore || !deps.caughtUpToLiveTail(agentId))
      // We appended newer messages (scroll-DOWN, still away from the live tail);
      // cap the oldest end to the CEILING, not the base, so the reader keeps a
      // visible buffer ABOVE while paging down through a hidden-heavy stretch. The
      // far-oldest (re-fetched by loadOlderMessages on scroll-up) is what trims.
      // forwardFillToLiveTail's tail-snap stays at the base, so reaching the live
      // tail trims the window back to lean.
      deps.trimOldestEnd(agentId, deps.maxLoadedCeiling)
    })
  }

  function paginationLimit(agentId: string, purpose: HistoryPagePurpose | undefined): number {
    return purpose === 'landing-refill'
      ? landingPageLimit(deps.getViewportHeight(agentId))
      : MESSAGE_PAGE_LIMIT
  }

  /** Publish evidence of remaining history before releasing the fetch owner. */
  function finishTailFill(agentId: string, hasMore: boolean, resume: boolean): void {
    batch(() => {
      const tail = deps.getLastSeq(agentId)
      // hasMore proves that at least one later sequence exists, even without latestSeq.
      if (hasMore && tail < MAX_INT64_SEQ)
        deps.liveTail.bump(agentId, tail + 1n)
      const behind = hasMore || !deps.caughtUpToLiveTail(agentId)
      setState('hasMoreNewer', agentId, behind)
      setState('tailFillDeferred', agentId, behind && resume)
    })
  }

  /**
   * Extend the newest window from its actual loaded cursor. Each read spends one page.
   * An explicit landing can spend one read here. The background reconciler owns any
   * remaining gap, and stops if the reader has moved into history.
   */
  async function forwardFillToLiveTail(
    workerId: string,
    agentId: string,
    signal: AbortSignal,
    liveSeqAtEntry: bigint,
    survives: SurvivesHistoryRead,
    options: { pages?: number, limit?: number | undefined, mayContinue?: (() => boolean) | undefined } = {},
  ): Promise<void> {
    const pages = options.pages ?? MAX_FORWARD_FILL_PAGES
    batch(() => {
      setState('hasMoreNewer', agentId, !deps.caughtUpToLiveTail(agentId))
      setState('tailFillDeferred', agentId, false)
    })
    let hasMore = false
    for (let count = 0; count < pages && (hasMore || !deps.caughtUpToLiveTail(agentId)); count++) {
      if (signal.aborted)
        return
      const cursor = deps.getLastMessageSeq(agentId)
      if (cursor === undefined || options.mayContinue?.() === false)
        break

      const reply = await listMessagesAfter(workerId, agentId, cursor, signal, options.limit).catch((error: unknown) => {
        if (!signal.aborted)
          throw error
        return undefined
      })
      if (!reply || signal.aborted)
        return
      hasMore = reply.hasMore
      if (reply.latestSeq !== undefined)
        deps.liveTail.bump(agentId, reply.latestSeq)
      if (options.mayContinue?.() === false)
        break

      // Only an ascending range after the loaded cursor can extend the window.
      const rows = reply.messages.filter(row => row.seq > cursor && survives(row))
      if (rows.length === 0) {
        if (!hasMore)
          deps.liveTail.settleToWindow(agentId, liveSeqAtEntry, cursor)
        finishTailFill(agentId, hasMore, !hasMore && options.mayContinue?.() !== false)
        return
      }

      batch(() => {
        deps.mergeFetchedMessages(agentId, rows, 'newer')
        deps.trimOldestEnd(agentId, deps.maxLoaded)
      })
      hasMore = hasMore && deps.getLastSeq(agentId) <= rows.at(-1)!.seq
    }
    finishTailFill(agentId, hasMore, options.mayContinue?.() !== false)
  }

  /** Resume remaining tail work under a fresh fetch owner. */
  async function resumeDeferredTailFill(
    workerId: string,
    agentId: string,
    watchSignal?: AbortSignal,
    recovery?: { mayContinue: () => boolean, pageLimit?: number | undefined },
  ): Promise<void> {
    const mayContinue = recovery?.mayContinue
    if (!state.tailFillDeferred[agentId] || state.fetchingOlder[agentId] || state.fetchingNewer[agentId] || mayContinue?.() === false)
      return
    const liveSeqAtEntry = deps.liveTail.get(agentId)
    await deps.runHistoryFetch(agentId, 'fetchingNewer', (signal, survives) =>
      forwardFillToLiveTail(workerId, agentId, signal, liveSeqAtEntry, survives, { mayContinue, limit: recovery?.pageLimit }), watchSignal)
  }

  /**
   * Run the body of a history jump under the supersession machinery (see
   * `deps.runHistoryFetch`). The body resolves `true` when it replaced the window with
   * its page, and `false` when its signal aborted before that.
   *
   * Each request of a jump carries its signal, so an abort ends the browser's wait
   * and prevents further retries. An abort is how a jump is designed to end
   * early: a later history fetch replaced it, or the reader abandoned it. So a rejection
   * after the abort is no failure to report: the jump resolves `false`, and the window
   * stays as it is. Any other rejection passes through to the caller.
   */
  function runHistoryJump(
    agentId: string,
    flag: 'fetchingOlder' | 'fetchingNewer',
    body: (signal: AbortSignal, limit: number, survives: SurvivesHistoryRead) => Promise<boolean>,
    abortSignal?: AbortSignal,
    pageLimit?: number,
  ): Promise<boolean> {
    return deps.runHistoryFetch(agentId, flag, async (signal, survives) => {
      try {
        return await body(signal, pageLimit ?? landingPageLimit(deps.getViewportHeight(agentId)), survives)
      }
      catch (err) {
        if (signal.aborted)
          return false
        throw err
      }
    }, abortSignal)
  }

  /**
   * Re-fetch the latest page and snap the window back to the live tail.
   * Use up to one additional page for messages arriving during the request.
   * The background reconciler owns any gap that remains.
   *
   * Resolves `true` when it replaced the window with the latest page. Resolves `false`
   * when its signal aborted before that, or when another history fetch replaced it.
   */
  async function jumpToLatestMessages(
    workerId: string,
    agentId: string,
    abortSignal?: AbortSignal,
    recovery?: { mayReplace: () => boolean, pageLimit?: number | undefined },
  ): Promise<boolean> {
    const mayReplace = recovery?.mayReplace
    if (mayReplace && (state.fetchingOlder[agentId] || state.fetchingNewer[agentId] || !mayReplace()))
      return false
    // A jump supersedes any in-flight fetch (scroll-load or a prior jump) so
    // a stuck request can't block it -- runHistoryFetch aborts the prior one.
    // `abortSignal` has two sources. The reconcile-driven empty-window re-anchor passes
    // the signal of the WatchEvents subscription, so a workspace switch can't leak this
    // fetch (and its forwardFillToLiveTail loop) against a navigated-away worker -- the
    // same teardown guarantee catchUpToTail / resumeDeferredTailFill already carry. The
    // scroll hook passes the signal of the reader's jump, which it aborts when the reader
    // gives another command or takes control of the view.
    return runHistoryJump(agentId, 'fetchingNewer', async (signal, limit, survives) => {
      // Snapshot the recorded live tail BEFORE re-anchoring. The terminal clamp
      // settles latestLiveSeq down to the window tail, but a message broadcast
      // DURING this jump raises latestLiveSeq above the snapshot -- a genuinely
      // reachable seq that settleLiveTailToWindow must NOT discard.
      const liveSeqAtEntry = deps.liveTail.get(agentId)
      const previous = snapshotLoadedIdentities(agentId)
      // Always re-anchor on a fresh latest page so the window is contiguous
      // up to the tail regardless of how far it had drifted. The request carries
      // `signal`, so an abort releases the browser's wait.
      const latest = await workerAgent.listAgentMessages(workerId, { agentId, anchor: MessagePageAnchor.LATEST, limit }, { signal })
      if (signal.aborted || mayReplace?.() === false)
        return false
      batch(() => {
        const observedTail = deps.liveTail.get(agentId)
        // A lower tail observed during the request is newer deletion evidence.
        const latestSeq = observedTail < liveSeqAtEntry && (latest.latestSeq === undefined || observedTail < latest.latestSeq)
          ? observedTail
          : latest.latestSeq
        const messages = latestSeq === undefined
          ? latest.messages
          : latest.messages.filter(message => message.seq <= latestSeq)
        if (latestSeq !== undefined)
          deps.liveTail.setAuthoritative(agentId, latestSeq, liveSeqAtEntry)
        else if (messages.length === 0 && !latest.hasMore)
          deps.liveTail.resetToEmptyIfStale(agentId, liveSeqAtEntry)
        applyLatestPage(agentId, { ...latest, messages, hasMore: latestSeq !== 0n && latest.hasMore }, survives, deps.needsLatestAnchor(agentId)
          ? previous
          : undefined)
        const caughtUp = deps.caughtUpToLiveTail(agentId)
        setState('hasMoreNewer', agentId, !caughtUp)
        const remainingPages = caughtUp
          ? 1
          : 0
        deps.setLandingRefillPages(agentId, remainingPages)
      })
      await forwardFillToLiveTail(workerId, agentId, signal, liveSeqAtEntry, survives, { pages: 1, limit, mayContinue: mayReplace })
      return true
    }, abortSignal, recovery?.pageLimit)
  }

  /**
   * Jump to the very first message in history (the Home affordance). Replaces
   * the window with the earliest page in a single fetch (anchor OLDEST), so
   * hasMoreOlder becomes false (we are at the start) and hasMoreNewer reflects
   * whether more exist beyond it. Supersedes any in-flight fetch.
   *
   * `abortSignal` abandons the fetch: the reader gave another command, or took control
   * of the view, so the page must not arrive and replace the window. Resolves `true`
   * when it replaced the window with the oldest page, and `false` when its signal
   * aborted or another history fetch replaced it.
   */
  async function jumpToOldestMessages(workerId: string, agentId: string, abortSignal?: AbortSignal): Promise<boolean> {
    return runHistoryJump(agentId, 'fetchingOlder', async (signal, limit, survives) => {
      const oldest = await workerAgent.listAgentMessages(workerId, { agentId, anchor: MessagePageAnchor.OLDEST, limit }, { signal })
      if (signal.aborted)
        return false
      // applyMessages sets hasMoreOlder from its `hasMore` argument and
      // hasMoreNewer=false; override the latter since newer messages DO exist
      // beyond the earliest page.
      batch(() => {
        deps.setLandingRefillPages(agentId, 1)
        deps.completeLatestAnchor(agentId)
        deps.applyMessages(agentId, oldest.messages.filter(survives), false)
        setState('hasMoreNewer', agentId, oldest.hasMore)
      })
      return true
    }, abortSignal)
  }

  /**
   * Replace the window with a small page around the rail's target sequence. BEFORE
   * seq + 1 includes the target and gives the reader some older context. AFTER seq
   * supplies the newer rows that fill most of the viewport. At int64 max, LATEST
   * supplies the target because seq + 1 exceeds the Worker's cursor range.
   *
   * A deleted target seq lands on the nearest survivors (BEFORE returns the highest
   * seq <= target); the scroll hook's nearest-row-by-seq landing does the rest. When
   * `seq` falls into a deleted prefix/suffix, one page is empty and the window snaps to
   * the surviving oldest/newest rows. Both empty means the history genuinely vanished
   * (mirrors jumpToLatestMessages' authoritative-empty reset).
   *
   * Resolves `true` when it replaced the window with its page, also with the empty
   * window. Resolves `false` when its signal aborted, or when another history fetch
   * replaced it.
   */
  async function jumpToMessagesAroundSeq(workerId: string, agentId: string, seq: bigint, abortSignal?: AbortSignal): Promise<boolean> {
    return runHistoryJump(agentId, 'fetchingNewer', async (signal, limit, survives) => {
      // Snapshot the recorded live tail before the swap: a message broadcast DURING the
      // fetch raises it past this, and hasMoreNewer/resetToEmptyIfStale must respect that.
      const liveSeqAtEntry = deps.liveTail.get(agentId)
      const atSequenceLimit = seq >= MAX_INT64_SEQ
      const beforeRequest = atSequenceLimit
        ? { agentId, anchor: MessagePageAnchor.LATEST, limit }
        : {
            agentId,
            anchor: MessagePageAnchor.BEFORE,
            cursorSeq: seq + 1n,
            limit: Math.ceil(limit / 2),
          }
      // Both reads share the wait owned by this seek.
      const [before, after] = await Promise.all([
        workerAgent.listAgentMessages(workerId, beforeRequest, { signal }),
        atSequenceLimit
          ? undefined
          : listMessagesAfter(workerId, agentId, seq, signal, limit),
      ])
      if (signal.aborted)
        return false
      const newerIds = new Set(after?.messages.map(row => row.id))
      const rows = [
        // A message can move from BEFORE to AFTER between the two queries.
        ...before.messages.filter(row => !newerIds.has(row.id)),
        ...(after?.messages ?? []),
      ].filter(survives)
      batch(() => {
        deps.completeLatestAnchor(agentId)
        deps.setLandingRefillPages(agentId, atSequenceLimit ? 1 : 0)
        if (rows.length === 0) {
          // Clear the stale tail unless a broadcast raised it during the fetch.
          deps.applyMessages(agentId, [], false)
          deps.liveTail.resetToEmptyIfStale(agentId, liveSeqAtEntry)
          return
        }
        deps.applyMessages(agentId, rows, before.hasMore)
        // The recorded tail can advance during the fetch.
        setState('hasMoreNewer', agentId, after?.hasMore === true || !deps.caughtUpToLiveTail(agentId))
      })
      return true
    }, abortSignal)
  }

  return {
    loadInitialMessages,
    refreshLoadedMessages,
    loadOlderMessages,
    catchUpToTail,
    loadNewerPage,
    forwardFillToLiveTail: (workerId: string, agentId: string, signal: AbortSignal, liveSeqAtEntry: bigint) =>
      deps.duringHistoryRead(agentId, survives => forwardFillToLiveTail(workerId, agentId, signal, liveSeqAtEntry, survives)),
    resumeDeferredTailFill,
    jumpToLatestMessages,
    jumpToOldestMessages,
    jumpToMessagesAroundSeq,
  }
}
