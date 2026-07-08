import type { SetStoreFunction } from 'solid-js/store'
import type { ChatStoreState } from './chat.store'
import type { LiveTailTracker } from './chatLiveTail'
import type { AgentChatMessage, TodoItem as ProtoTodoItem } from '~/generated/leapmux/v1/agent_pb'
import { listAgentMessages } from '~/api/workerRpc'
import { MessagePageAnchor } from '~/generated/leapmux/v1/agent_pb'

/** Per-page size for the windowed history fetch (the hub caps the page at 50). */
export const MESSAGE_PAGE_SIZE = 50
/**
 * Max forward-fill rounds before forwardFillToLiveTail settles regardless, so a
 * genuinely-vanished live seq stops the loop instead of spinning.
 */
const MAX_FORWARD_FILL_ATTEMPTS = 4
const MAX_INT64_SEQ = 9223372036854775807n

/**
 * Abort `controller` when the WatchEvents `watchSignal` fires, so a reconcile-driven
 * fetch dies with the subscription that spawned it (a workspace switch / worker change
 * tears the stream down -- the fetch must stop running against a worker the reader
 * navigated away from, instead of leaking until its pages reject). Aborts immediately
 * if the signal is already aborted. Shared by the two background forward-fill loops
 * (catchUpToTail, resumeDeferredTailFill) and the empty-window re-seat
 * (jumpToLatestMessages, via the store's beginHistoryFetch).
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

/**
 * One ascending page of messages with seq > cursorSeq -- the AFTER-anchored forward
 * pagination request shared by catchUpToTail, loadNewerPage, and
 * jumpToLatestMessages' fill/retry loops, so the four forward-fetch sites can't
 * drift on the anchor + page-size wiring.
 */
function listMessagesAfter(workerId: string, agentId: string, cursorSeq: bigint) {
  return listAgentMessages(workerId, {
    agentId,
    anchor: MessagePageAnchor.AFTER,
    cursorSeq,
    limit: MESSAGE_PAGE_SIZE,
  })
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
   * `watchSignal` ties the fetch to the WatchEvents subscription (see the store's
   * beginHistoryFetch); a reconcile-driven fetch passes it, a user fetch omits it.
   */
  runHistoryFetch: (
    agentId: string,
    flag: 'fetchingOlder' | 'fetchingNewer',
    body: (signal: AbortSignal) => Promise<void>,
    watchSignal?: AbortSignal,
  ) => Promise<void>
  mergeFetchedMessages: (agentId: string, fetched: AgentChatMessage[], side: 'older' | 'newer') => void
  applyMessages: (agentId: string, messages: AgentChatMessage[], hasMore: boolean) => void
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
   * The window's first/last SERVER seq, or `undefined` when the window has none
   * (empty, or only optimistic locals). The undefined-returning honest signal the
   * re-anchor guards test, vs getFirstSeq/getLastSeq which collapse it to the magic
   * 0n a cursor reader wants.
   */
  getFirstServerSeq: (agentId: string) => bigint | undefined
  getLastServerSeq: (agentId: string) => bigint | undefined
  caughtUpToLiveTail: (agentId: string) => boolean
  addMessage: (agentId: string, message: AgentChatMessage) => void
  trimOldestEnd: (agentId: string, maxCount: number) => void
  trimNewestEnd: (agentId: string, maxCount: number) => void
  replaceTodos: (agentId: string, protoTodos: ProtoTodoItem[]) => void
  loadLocalMessages: (agentId: string) => void
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

  /**
   * Apply a LATEST page's body + its authoritative to-do list -- the shared tail of
   * loadInitialMessages and jumpToLatestMessages. Applies the messages, then the
   * to-do snapshot when todosLoaded is true. protobuf-es always materializes the
   * repeated `todos` field as [], so false says "the LoadTodos query failed" and
   * prevents wiping a populated list.
   */
  function applyLatestPage(
    agentId: string,
    resp: { messages: AgentChatMessage[], hasMore: boolean, todosLoaded: boolean, todos: ProtoTodoItem[] },
  ): void {
    deps.applyMessages(agentId, resp.messages, resp.hasMore)
    if (resp.todosLoaded)
      deps.replaceTodos(agentId, resp.todos)
  }

  /** Fetch the latest messages for an agent (initial page load). */
  async function loadInitialMessages(workerId: string, agentId: string): Promise<void> {
    if (state.initialLoadComplete[agentId])
      return
    // Route through the supersession machinery (not a bare fetchingOlder flag)
    // so a jump-to-latest fired mid-load aborts this one. Otherwise this fetch
    // could resolve last and clobber the jump's fresher window with the stale
    // initial page.
    await deps.runHistoryFetch(agentId, 'fetchingOlder', async (signal) => {
      const resp = await listAgentMessages(workerId, {
        agentId,
        anchor: MessagePageAnchor.LATEST,
        limit: MESSAGE_PAGE_SIZE,
      })
      if (signal.aborted)
        return
      // The server ships the authoritative to-do list on the cold-start (LATEST)
      // page; subsequent live updates arrive via AgentTodosChanged broadcasts.
      applyLatestPage(agentId, resp)
    })
    // Restore any local messages that were persisted to localStorage
    // (e.g. undelivered messages that survived a page refresh). Runs even when
    // a jump superseded the fetch above -- the jump replaces the server window
    // but never reloads locals.
    deps.loadLocalMessages(agentId)
  }

  /** Fetch older messages before the current window. */
  async function loadOlderMessages(workerId: string, agentId: string): Promise<void> {
    if (state.fetchingOlder[agentId])
      return
    if (!state.hasMoreOlder[agentId])
      return
    const messages = state.messagesByAgent[agentId]
    if (!messages || messages.length === 0)
      return

    // Page before the oldest SERVER message. getFirstServerSeq returns undefined
    // when the window has no server cursor to page BEFORE (only leading optimistic
    // locals), the honest signal -- no magic 0n the backend would resolve as
    // `seq < 0` (an empty page).
    const firstSeq = deps.getFirstServerSeq(agentId)
    if (firstSeq === undefined) {
      // The window holds only optimistic locals (no server cursor to page
      // BEFORE) yet hasMoreOlder is flagged true. Rather than no-op forever and
      // wedge the "load older" affordance, reconcile by loading the earliest
      // real page (anchor OLDEST), which resolves hasMoreOlder/hasMoreNewer from
      // the response and preserves the locals.
      await jumpToOldestMessages(workerId, agentId)
      return
    }
    await deps.runHistoryFetch(agentId, 'fetchingOlder', async (signal) => {
      const resp = await listAgentMessages(workerId, {
        agentId,
        anchor: MessagePageAnchor.BEFORE,
        cursorSeq: firstSeq,
        limit: MESSAGE_PAGE_SIZE,
      })
      if (signal.aborted)
        return
      deps.mergeFetchedMessages(agentId, resp.messages, 'older')
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
    body: (signal: AbortSignal, liveSeqAtEntry: bigint) => Promise<void>,
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
      await body(controller.signal, liveSeqAtEntry)
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
   * Fetch messages forward from a given seq, looping until all are retrieved.
   * Used after WatchEvents catch-up replay to fill any gap beyond the
   * 50-message replay limit.
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
   * Each page is trimmed as it lands -- trim the OLDEST end to the CEILING (not the
   * base), mirroring loadNewerPage -- so a large reconnect gap is bounded by the
   * ceiling rather than growing without limit. Capping to the ceiling (not the base)
   * is what spares a SCROLLED-UP reader: the loop runs while at the live tail
   * (hasMoreNewer false), but the reader can be scrolled up into a ceiling-grown older
   * buffer, and a base trim would reap that buffer (and rows below their anchor). The
   * per-append viewport trim (trimOldestToViewport) brings a FOLLOWED tail back to the
   * lean base after the replay; the scroll re-pin keeps a scrolled-up reader's anchored
   * row stationary throughout.
   */
  async function catchUpToTail(workerId: string, agentId: string, afterSeq: bigint, watchSignal?: AbortSignal): Promise<void> {
    // Idempotent under the continuous reconcile effect (runSingleFlightTailLoop no-ops
    // while a loop is draining this agent): the loop itself keeps fetching while
    // resp.hasMore, so a lag that grows mid-fill (a live arrival dropped beyond the
    // window) is closed by the running loop, not a re-kick. A user fetch still
    // supersedes via catchUpAbort/beginHistoryFetch.
    await runSingleFlightTailLoop(agentId, watchSignal, async (signal, liveSeqAtEntry) => {
      let cursor = afterSeq
      while (!signal.aborted && !state.hasMoreNewer[agentId]) {
        const resp = await listMessagesAfter(workerId, agentId, cursor)
        // Superseded mid-flight (a user jump/scroll, or subscription teardown):
        // discard this page so the superseding fetch owns the tail.
        if (signal.aborted)
          return
        for (const msg of resp.messages) {
          deps.addMessage(agentId, msg)
        }
        // Cap the window as we go: trim the oldest end (sets hasMoreOlder; leaves
        // hasMoreNewer false so the loop and the live-append guard keep working). Cap to
        // the CEILING, not the base -- mirroring loadNewerPage: this runs while the
        // window still holds the live tail (hasMoreNewer false), but the reader can be
        // SCROLLED UP into a ceiling-grown older buffer. Trimming to the base here would
        // reap that buffer (and, when anchor-to-tail exceeds the base, the reader's
        // VISIBLE rows below the anchor) on a background reconnect replay, then the
        // buffer-filler would refetch it. The per-append viewport trim
        // (trimOldestToViewport) brings a FOLLOWED tail back to the lean base; a
        // scrolled-up reader keeps their buffer up to the ceiling.
        deps.trimOldestEnd(agentId, deps.maxLoadedCeiling)
        if (!resp.hasMore || resp.messages.length === 0)
          break
        // The server returns each page ordered ascending by seq (ORDER BY seq
        // ASC), so the last element is always the highest seq -- a safe cursor
        // for the next forward page.
        cursor = resp.messages.at(-1)!.seq
      }
      // The loop drained the server's forward pages (reached has_more=false or an empty
      // page) while at the tail (hasMoreNewer stayed false). If the recorded live tail
      // STILL sits ahead of the loaded window, it points at a seq the server can no
      // longer give us -- e.g. a tail row deleted with an indeterminate broadcast that
      // couldn't lower the high-water (chatLiveTail.onDelete). Without clamping it,
      // caughtUpToLiveTail never resolves, so the continuous reconcile re-issues this
      // empty fetch on EVERY tick and the scroll-to-bottom affordance stays lit forever.
      // Clamp it to the window (mirrors loadNewerPage's dedup-stall settle); an empty
      // window resets to empty (mirrors jumpToLatestMessages' authoritative-empty path).
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
  async function loadNewerPage(workerId: string, agentId: string): Promise<void> {
    if (state.fetchingNewer[agentId])
      return
    if (!state.hasMoreNewer[agentId])
      return
    const lastSeq = deps.getLastServerSeq(agentId)
    if (lastSeq === undefined) {
      // The window has no SERVER cursor to page AFTER -- only optimistic locals
      // remain, or a messageDeleted broadcast emptied the server range -- yet
      // hasMoreNewer is set, so a plain return would wedge the scroll-down
      // affordance forever. Mirror loadOlderMessages' OLDEST fallback: re-anchor
      // on a fresh latest page (jumpToLatestMessages resolves hasMoreNewer from
      // the response and preserves locals).
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
    await deps.runHistoryFetch(agentId, 'fetchingNewer', async (signal) => {
      const resp = await listMessagesAfter(workerId, agentId, lastSeq)
      if (signal.aborted)
        return
      deps.mergeFetchedMessages(agentId, resp.messages, 'newer')
      // Reaching the SERVER tail (has_more false) does NOT prove we reached the
      // LIVE tail: a broadcast that arrived mid-fetch (seq beyond this page) was
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
      // the only thing that lowers the tail mid-fetch is a concurrent messageDeleted
      // removing the window's tail row. Clamping on a shrink would erase a still-
      // reachable recorded tail (the deleted row need not be the recorded one), so the
      // delete is left to onDelete + a later forward-fill to reconcile instead.
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

  /**
   * The gap-free forward fill that snaps the window to the live tail, shared as
   * jumpToLatestMessages' terminal. Pages AFTER the window tail until it reaches
   * the highest observed live seq (latestLiveSeq), retrying one dedup-stall per
   * round and re-attempting while each round still advances the window (a
   * message broadcast mid-fetch can raise latestLiveSeq after a round decided to
   * stop). Bounded by MAX_FORWARD_FILL_ATTEMPTS so a genuinely-vanished seq
   * still settles rather than spinning. `liveSeqAtEntry` is the recorded tail
   * snapshotted BEFORE re-anchoring, so the terminal clamp keeps a seq a
   * mid-fetch broadcast raised. Returns without settling when a superseding
   * fetch aborts `signal` (the superseding fetch owns the tail).
   */
  async function forwardFillToLiveTail(workerId: string, agentId: string, signal: AbortSignal, liveSeqAtEntry: bigint): Promise<void> {
    // Starting a fresh fill clears any prior exhaustion-forced deferral; the exhaustion
    // branch below re-arms it only if THIS fill also runs out of attempts while still
    // advancing. So a resumed fill that finally catches up (or stalls on a vanished seq)
    // leaves it cleared and the continuous reconcile stops resuming.
    setState('tailFillDeferred', agentId, false)
    // Merge a forward page at the tail, cap the window, and reaffirm we're at
    // the live tail. trimOldestEnd sets hasMoreOlder; the merge keeps us
    // pinned to the bottom, so hasMoreNewer is cleared each time.
    const appendNewerAtTail = (messages: AgentChatMessage[]) => {
      deps.mergeFetchedMessages(agentId, messages, 'newer')
      deps.trimOldestEnd(agentId, deps.maxLoaded)
      setState('hasMoreNewer', agentId, false)
    }

    // Page AFTER the window tail until we reach the live tail or a page stops
    // making progress (empty, server tail, or a dedup-stall that didn't
    // advance). Returns 'aborted' if a superseding fetch took over mid-flight.
    const fillRound = async (): Promise<'aborted' | 'settled'> => {
      while (!deps.caughtUpToLiveTail(agentId)) {
        // An all-locals / empty window has no SERVER cursor to page AFTER: getLastSeq
        // collapses it to 0n, and listMessagesAfter(0n) fetches the OLDEST page (seq > 0),
        // which appendNewerAtTail would splice in as the tail -- showing the head while
        // claiming to be at the live tail, gap unfilled. Break so retryDedupStall
        // re-anchors just below the recorded live seq (the real tail region), or the
        // terminal settle clamps a genuinely-vanished tail.
        if (deps.getLastServerSeq(agentId) === undefined)
          break
        const cursorSeq = deps.getLastSeq(agentId)
        const resp = await listMessagesAfter(workerId, agentId, cursorSeq)
        if (signal.aborted)
          return 'aborted'
        if (resp.messages.length === 0)
          break
        appendNewerAtTail(resp.messages)
        // A page the server marks has_more but that didn't advance the tail
        // (everything in it deduped against the window) is a dedup-stall:
        // looping from the same cursor would spin forever, so stop and let the
        // retry re-anchor below the recorded live seq.
        if (!resp.hasMore || deps.getLastSeq(agentId) <= cursorSeq)
          break
      }
      return 'settled'
    }

    // One direct attempt at the tail region after a dedup-stall: re-anchor
    // just below the highest observed live seq, rather than from a cursor we
    // already know stalls. No-op once caught up or when nothing sits below the
    // recorded tail. Reached only when !caughtUpToLiveTail, so latestLiveSeq >
    // getLastSeq >= 0n. The `> 0n` clamp makes the no-underflow guarantee
    // self-evident here rather than relying on caughtUpToLiveTail's formula two
    // calls away; a 0n recorded tail is caught up anyway, so this stays a no-op.
    const retryDedupStall = async (): Promise<'aborted' | 'settled'> => {
      if (deps.caughtUpToLiveTail(agentId))
        return 'settled'
      const recordedTail = deps.liveTail.get(agentId)
      const tailCursor = recordedTail > 0n ? recordedTail - 1n : 0n
      if (tailCursor <= deps.getLastSeq(agentId))
        return 'settled'
      const resp = await listMessagesAfter(workerId, agentId, tailCursor)
      if (signal.aborted)
        return 'aborted'
      if (resp.messages.length > 0)
        appendNewerAtTail(resp.messages)
      return 'settled'
    }

    // Re-attempt the fill+retry while each round still advances the window: a
    // message broadcast mid-fetch raises latestLiveSeq after a round decided to
    // stop, and the next round pulls it instead of stranding it. Bounded so a
    // genuinely-vanished seq still settles rather than spinning. `stalled` records
    // WHY the loop ended: a round that made no progress (the gap is unreachable)
    // vs. running out of attempts while still advancing (a broadcast storm outran
    // the bound) -- the two settle differently below.
    let stalled = false
    for (let attempt = 0; attempt < MAX_FORWARD_FILL_ATTEMPTS; attempt++) {
      const tailBefore = deps.getLastSeq(agentId)
      if (await fillRound() === 'aborted')
        return
      if (deps.caughtUpToLiveTail(agentId))
        break
      if (await retryDedupStall() === 'aborted')
        return
      // No progress this round: the remaining gap is unreachable right now (a seq
      // broadcast then deleted), so stop AND settle below. A round that DID advance
      // the tail loops again to chase a seq a mid-fetch broadcast may have raised
      // latestLiveSeq to.
      if (deps.getLastSeq(agentId) <= tailBefore) {
        stalled = true
        break
      }
    }
    if (deps.caughtUpToLiveTail(agentId)) {
      // Caught up -- appendNewerAtTail already left hasMoreNewer false. Done.
    }
    else if (stalled) {
      // A round made no progress: the remaining gap is genuinely unreachable (a seq
      // broadcast then deleted), so clamp the recorded tail to the window -- otherwise
      // caughtUp wedges false forever chasing it. Preserves a seq a mid-fetch broadcast
      // raised past liveSeqAtEntry for later recovery (settleToWindow's guard).
      deps.liveTail.settleToWindow(agentId, liveSeqAtEntry, deps.getLastSeq(agentId))
    }
    else {
      // Ran out of attempts while STILL advancing: a broadcast storm outran the bound,
      // so the gap is REACHABLE -- do NOT clamp it away. Re-flag hasMoreNewer (cleared
      // prematurely by appendNewerAtTail) so a beyond-window live frame is recorded
      // rather than appended into the gap as a hole. Mark the gap exhaustion-forced so
      // the continuous tail-reconcile RESUMES the fill (resumeDeferredTailFill) -- a
      // FOLLOWING reader's gap self-heals as the storm drains, instead of stranding the
      // streaming tail behind the scroll-to-bottom affordance until a user scroll/jump or
      // a reconnect. A user fetch (incl. a scroll-up) clears the flag via beginHistoryFetch.
      setState('hasMoreNewer', agentId, true)
      setState('tailFillDeferred', agentId, true)
    }
  }

  /**
   * Resume a forward fill that PARKED with an exhaustion-forced gap (see
   * forwardFillToLiveTail's exhaustion branch): the live tail outran the bounded fill,
   * leaving hasMoreNewer set and tailFillDeferred armed. Driven by the continuous
   * tail-reconcile so a FOLLOWING reader's gap self-heals without a user action. Modeled
   * on catchUpToTail -- BACKGROUND (a user fetch supersedes it via catchUpAbort), guarded
   * single-flight by catchingUp -- but it forward-fills through forwardFillToLiveTail's
   * mergeFetchedMessages path, NOT addMessage (whose live-append guard drops a beyond-tail
   * page while hasMoreNewer is set). Each resume re-runs the bounded fill: it catches up
   * (clearing the flag), stalls (clearing it), or re-parks (re-arming it) so the next
   * reconcile tick resumes again until the storm drains.
   */
  async function resumeDeferredTailFill(workerId: string, agentId: string, watchSignal?: AbortSignal): Promise<void> {
    // The `catchingUp.has` single-flight guard, the WatchEvents tie, and the abort-time
    // slot release all live in runSingleFlightTailLoop; only the deferral precondition is
    // bespoke here. The body is the bounded gap-free fill (vs catchUpToTail's looping
    // addMessage drain).
    if (!state.tailFillDeferred[agentId])
      return
    await runSingleFlightTailLoop(agentId, watchSignal, (signal, liveSeqAtEntry) =>
      forwardFillToLiveTail(workerId, agentId, signal, liveSeqAtEntry))
  }

  /**
   * Re-fetch the latest page and snap the window back to the live tail.
   * Gap-free: keeps forward-fetching until the server reports no more pages
   * AND we have caught up to the highest live seq observed (latestLiveSeq),
   * so a live message that arrives mid-fetch is never lost. Used by the
   * scroll-to-bottom button and send-while-scrolled-away.
   */
  async function jumpToLatestMessages(workerId: string, agentId: string, watchSignal?: AbortSignal): Promise<void> {
    // A jump supersedes any in-flight fetch (scroll-load or a prior jump) so
    // a stuck request can't block it -- runHistoryFetch aborts the prior one.
    // `watchSignal` ties the reconcile-driven empty-window re-seat to the
    // WatchEvents subscription so a workspace switch can't leak this fetch (and
    // its forwardFillToLiveTail loop) against a navigated-away worker -- the same
    // teardown guarantee catchUpToTail / resumeDeferredTailFill already carry.
    await deps.runHistoryFetch(agentId, 'fetchingNewer', async (signal) => {
      // Snapshot the recorded live tail BEFORE re-anchoring. The terminal clamp
      // settles latestLiveSeq down to the window tail, but a message broadcast
      // DURING this jump raises latestLiveSeq above the snapshot -- a genuinely
      // reachable seq that settleLiveTailToWindow must NOT discard.
      const liveSeqAtEntry = deps.liveTail.get(agentId)
      // Always re-anchor on a fresh latest page so the window is contiguous
      // up to the tail regardless of how far it had drifted.
      const latest = await listAgentMessages(workerId, { agentId, anchor: MessagePageAnchor.LATEST, limit: MESSAGE_PAGE_SIZE })
      if (signal.aborted)
        return
      applyLatestPage(agentId, latest)
      // An empty LATEST page with no more beyond it is the server's ground
      // truth that NO messages exist -- e.g. the whole history was deleted while
      // we were scrolled away, leaving the recorded live tail pointing at a vanished
      // tail (whose own messageDeleted broadcast was a no-op, since it wasn't loaded
      // and the broadcast carries no seq). Clear the recorded live tail so
      // caughtUpToLiveTail resolves and the forward-fill below stops chasing it.
      // settleToWindow can't do this -- it refuses to clamp to an empty window because
      // a TRANSIENT empty-during-fetch must not read as caught up -- but an
      // authoritative empty LATEST response is not transient. resetToEmptyIfStale skips
      // the clear if a message broadcast mid-fetch raised the tail past the snapshot
      // (that seq is genuinely reachable; forward-fill will pull it).
      if (latest.messages.length === 0 && !latest.hasMore)
        deps.liveTail.resetToEmptyIfStale(agentId, liveSeqAtEntry)
      // applyMessages set hasMoreNewer=false. If a live message landed beyond
      // the page we just fetched (race), forward-fill to the live tail and
      // settle (the shared gap-free terminal).
      await forwardFillToLiveTail(workerId, agentId, signal, liveSeqAtEntry)
    }, watchSignal)
  }

  /**
   * Jump to the very first message in history (the Home affordance). Replaces
   * the window with the earliest page in a single fetch (anchor OLDEST), so
   * hasMoreOlder becomes false (we are at the start) and hasMoreNewer reflects
   * whether more exist beyond it. Supersedes any in-flight fetch.
   */
  async function jumpToOldestMessages(workerId: string, agentId: string): Promise<void> {
    await deps.runHistoryFetch(agentId, 'fetchingOlder', async (signal) => {
      const oldest = await listAgentMessages(workerId, { agentId, anchor: MessagePageAnchor.OLDEST, limit: MESSAGE_PAGE_SIZE })
      if (signal.aborted)
        return
      // applyMessages preserves optimistic locals and sets hasMoreOlder from
      // its `hasMore` arg (false: nothing older than the first page) and
      // hasMoreNewer=false; override the latter since newer messages DO exist
      // beyond the earliest page.
      deps.applyMessages(agentId, oldest.messages, false)
      setState('hasMoreNewer', agentId, oldest.hasMore)
    })
  }

  /**
   * Replace the window with a page CENTERED on `seq` -- the scroll-rail's seek/jump
   * target. Two disjoint anchored fetches run in parallel: BEFORE cursor=seq+1n (rows
   * with seq <= seq, INCLUDING the target when it exists -- the bound is exclusive;
   * LATEST is used instead at int64 max because seq+1 is not a valid backend cursor) and
   * AFTER cursor=seq (rows with seq > seq). Both come back ascending, so their
   * concatenation is one contiguous ascending window with the target ~mid-window and
   * ~50 rows of padding on each side (under the 150 cap), so the buffer filler's first
   * pass fetches BACKGROUND pages rather than tripping the hard-edge stall indicators.
   *
   * A deleted target seq lands on the nearest survivors (BEFORE returns the highest
   * seq <= target); the scroll hook's nearest-row-by-seq landing does the rest. When
   * `seq` falls into a deleted prefix/suffix, one page is empty and the window snaps to
   * the surviving oldest/newest rows. Both empty means the history genuinely vanished
   * (mirrors jumpToLatestMessages' authoritative-empty reset).
   */
  async function jumpToMessagesAroundSeq(workerId: string, agentId: string, seq: bigint): Promise<void> {
    await deps.runHistoryFetch(agentId, 'fetchingNewer', async (signal) => {
      // Snapshot the recorded live tail before the swap: a message broadcast DURING the
      // fetch raises it past this, and hasMoreNewer/resetToEmptyIfStale must respect that.
      const liveSeqAtEntry = deps.liveTail.get(agentId)
      const beforeRequest = seq >= MAX_INT64_SEQ
        ? { agentId, anchor: MessagePageAnchor.LATEST, limit: MESSAGE_PAGE_SIZE }
        : { agentId, anchor: MessagePageAnchor.BEFORE, cursorSeq: seq + 1n, limit: MESSAGE_PAGE_SIZE }
      const [before, after] = await Promise.all([
        listAgentMessages(workerId, beforeRequest),
        listAgentMessages(workerId, { agentId, anchor: MessagePageAnchor.AFTER, cursorSeq: seq, limit: MESSAGE_PAGE_SIZE }),
      ])
      if (signal.aborted)
        return
      const rows = [...before.messages, ...after.messages]
      if (rows.length === 0) {
        // No rows on either side: the history around (and beyond) the target is gone.
        // Empty the window and clear a stale recorded tail (skipped if a mid-fetch
        // broadcast raised a genuinely-reachable seq -- forward-fill will pull it).
        deps.applyMessages(agentId, [], false)
        deps.liveTail.resetToEmptyIfStale(agentId, liveSeqAtEntry)
        return
      }
      // One window swap. applyMessages sets hasMoreOlder from before.hasMore (more
      // history exists below the loaded page) and defaults hasMoreNewer=false.
      deps.applyMessages(agentId, rows, before.hasMore)
      // Override hasMoreNewer: after.hasMore covers server rows past the page; the
      // !caughtUpToLiveTail term (mirroring loadNewerPage) covers a live message that
      // bumped the recorded tail during the fetch, so the window can't claim the tail.
      setState('hasMoreNewer', agentId, after.hasMore || !deps.caughtUpToLiveTail(agentId))
    })
  }

  return {
    loadInitialMessages,
    loadOlderMessages,
    catchUpToTail,
    loadNewerPage,
    forwardFillToLiveTail,
    resumeDeferredTailFill,
    jumpToLatestMessages,
    jumpToOldestMessages,
    jumpToMessagesAroundSeq,
  }
}
