import type { ChatRailData } from './chatMessageMarks'
import type { ToolProgressEntry, ToolProgressUpdate } from './chatToolProgress'
import type { CommandStreamSegment, SavedViewportScroll, SpanMessageRevision } from './chatTypes'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { toBinary } from '@bufbuild/protobuf'
import { untrack } from 'solid-js'
import { createStore, produce, unwrap } from 'solid-js/store'
import { getAgentMessage } from '~/api/workerRpc'
import { forgetMarkPreview } from '~/components/chat/chatMarkPreview'
import { invalidateMessageClassificationCache } from '~/components/chat/messageClassification'
import { AgentChatMessageSchema, MarkType } from '~/generated/proto/leapmux/v1/agent_pb'
import { lowerBoundBySeq } from '~/lib/binarySearch'
import { invalidateMessageParseCache } from '~/lib/messageParser'
import { createBackgroundTaskStore } from './chatBackgroundTaskStore'
import { createCommandStreamStore } from './chatCommandStreams'
import { createContentVersionStore } from './chatContentVersions'
import { createHistoryPaginator, linkWatchSignal, MESSAGE_PAGE_SIZE } from './chatHistoryPaginator'
import { createLiveTailTracker } from './chatLiveTail'
import { createMessageMarksStore, resolveRailRange } from './chatMessageMarks'
import { createMessageMarkSeeder } from './chatMessageMarkSeeder'
import { applyFreshMessage, firstMessageSeq, insertMessageBySeq, isReapablePhantom, lastMessageSeq, mergeWindow, prunableDroppedSpanIds, transcriptMessageEnd } from './chatMessageOrder'
import { createPerAgentStore } from './chatPerAgentStore'
import { createSpanIndex } from './chatSpanIndex'
import { createStreamingTextStore } from './chatStreamingText'
import { createTodoStore } from './chatTodoStore'
import { createToolProgressStore } from './chatToolProgress'

/** Max number of loaded messages to keep for the visible agent tab window. */
export const MAX_LOADED_CHAT_MESSAGES = 150
/**
 * Hard ceiling on the visible-tab window when a scrolled-up reader is being
 * protected from the live-tail trim (see trimOldestToViewport) AND when the
 * scroll hook is pre-fetching a visible-content buffer (loadOlderMessages /
 * loadNewerPage cap to this, not the base, so the window can hold ~3 screens of
 * VISIBLE rows beyond the viewport even when most messages are hidden -- hidden
 * rows have zero scroll height, so a hidden-heavy stretch needs far more RAW
 * messages loaded to stay smoothly scrollable). 8x the base bounds memory while
 * covering up to ~90%-hidden stretches; past that the "Show hidden" affordance is
 * the escape. The base (MAX_LOADED_CHAT_MESSAGES) still governs the live tail, so
 * a chat being followed at the tail stays lean -- the window only grows while
 * scrolled up.
 */
export const MAX_LOADED_CHAT_MESSAGES_CEILING = 8 * MAX_LOADED_CHAT_MESSAGES
/** Max number of loaded messages to keep for hidden/background agent tabs. */
export const MAX_BACKGROUND_CHAT_MESSAGES = 50

/**
 * Whether two messages are byte-identical across every field (incl. the `content`
 * payload). Used to short-circuit an identical same-seq re-delivery. Compares the
 * serialized forms rather than protobuf's `equals`, whose bytes compare relies on
 * `instanceof Uint8Array` and so silently under-reports equality whenever the two
 * arrays come from different JS realms (jsdom/SSR/worker boundaries) -- the binary
 * encoding is realm-independent and a future field can't bypass it.
 */
function sameAgentMessage(a: AgentChatMessage, b: AgentChatMessage): boolean {
  const ba = toBinary(AgentChatMessageSchema, a)
  const bb = toBinary(AgentChatMessageSchema, b)
  if (ba.length !== bb.length)
    return false
  for (let i = 0; i < ba.length; i++) {
    if (ba[i] !== bb[i])
      return false
  }
  return true
}
/**
 * The windowing core's reactive state. Orthogonal per-concern slices (streaming
 * text, command streams, to-dos, and saved
 * viewport scroll) live in their own composed sub-stores -- this holds only the
 * loaded message window and the pagination bookkeeping its invariants depend on.
 */
export interface ChatStoreState {
  messagesByAgent: Record<string, AgentChatMessage[]>
  loading: boolean
  /** Whether there are older messages available to fetch (per agent). */
  hasMoreOlder: Record<string, boolean>
  /**
   * Whether there are newer messages beyond the in-memory window (per agent).
   * Becomes true when we trim the newest end after loading older history;
   * cleared once a forward fetch reaches the live tail (has_more === false).
   */
  hasMoreNewer: Record<string, boolean>
  /**
   * Whether a forward fill PARKED with an exhaustion-forced gap (per agent): the
   * live tail outran the bounded fill (forwardFillToLiveTail's exhaustion branch),
   * leaving hasMoreNewer set with a still-REACHABLE gap. Distinct from a plain
   * scrolled-away hasMoreNewer: the continuous tail-reconcile resumes the fill for
   * this case so a FOLLOWING reader's gap self-heals without a user scroll/jump or a
   * reconnect. Cleared by any superseding user fetch (beginHistoryFetch) -- e.g. a
   * scroll-up -- so the auto-fill stops once the reader leaves the tail.
   */
  tailFillDeferred: Record<string, boolean>
  /**
   * Whether a reconnect catch-up is in flight for this agent (per agent): set when the
   * client (re)subscribes via WatchEvents, cleared at CatchUpComplete. During catch-up
   * the recorded live tail tracks the LOADED tail (the bounded replay's own bumps), not
   * the true server tail -- and an indeterminate (unset) tail never raises it -- so the
   * live-append guard falls back to seq-CONTIGUITY while this is set (a non-contiguous
   * frame is a live arrival past the unfilled replay gap; a contiguous one is the next
   * in-order replay page). See beyondUnloadedNewerTail.
   */
  catchingUp: Record<string, boolean>
  /** Whether a fetch for older messages is in progress (per agent). */
  fetchingOlder: Record<string, boolean>
  /** Whether a fetch for newer messages (or a jump-to-latest) is in progress. */
  fetchingNewer: Record<string, boolean>
  /** Whether initial load has completed for an agent. */
  initialLoadComplete: Record<string, boolean>
  /** Monotonic counter incremented on every addMessage (including notification updates). */
  messageVersion: Record<string, number>
}

export function createChatStore() {
  const [state, setState] = createStore<ChatStoreState>({
    messagesByAgent: {},
    loading: false,
    hasMoreOlder: {},
    hasMoreNewer: {},
    tailFillDeferred: {},
    catchingUp: {},
    fetchingOlder: {},
    fetchingNewer: {},
    initialLoadComplete: {},
    messageVersion: {},
  })

  // Orthogonal per-concern slices, each its own composed sub-store. The window
  // The window core reaches into them only for shared window mutations.
  const streaming = createStreamingTextStore()
  const bumpMessageVersion = (agentId: string) => setState('messageVersion', agentId, (prev = 0) => prev + 1)
  const commandStreams = createCommandStreamStore({ onMutate: bumpMessageVersion })
  // No onMutate: unlike a command-stream delta, a tool-progress update changes
  // only a badge inside an already-laid-out header, so it must NOT bump the
  // message version. That bump wakes the auto-scroll effect and the
  // classified-entry cache, which would re-render the row -- the exact thing the
  // badge is built to avoid, since replacing a row's nodes drops a text
  // selection the user holds.
  const toolProgress = createToolProgressStore()
  const todos = createTodoStore()
  const backgroundTasks = createBackgroundTaskStore()
  // Saved per-agent scroll position for tab-switch viewport restore. A pure
  // get/set/clear slice with no domain logic, so it uses the per-agent spine
  // directly rather than through a dedicated wrapper module.
  const viewportScroll = createPerAgentStore<SavedViewportScroll | undefined>(undefined)
  // The "true tail + caught-up" invariant: highest server seq observed (incl. messages
  // dropped while scrolled away), with the bump/settle/delete rules in one tested unit.
  const liveTail = createLiveTailTracker()
  // Scroll-rail jump marks: the seqs of notable messages (user inputs, control
  // responses) + whole-history seq range. Seeded from ListMessageMarks, kept current
  // from live add/delete. Recorded even for messages dropped beyond the window.
  const messageMarks = createMessageMarksStore()
  // The seed-race machine that drives ListMessageMarks into `messageMarks`: epoch fencing,
  // the immediate-retry loop, and the bounded delayed-reschedule chain that heal a seed that
  // didn't stick. Its own tested unit (createMessageMarkSeeder), the sibling of the marks DATA
  // above; the store keeps the public loadMessageMarks entry (delegating to markSeeder.load)
  // and forgetAgent (calling markSeeder.forget).
  const markSeeder = createMessageMarkSeeder({ marks: messageMarks })

  /**
   * Non-reactive index linking each tool span's opener (tool_use) and result
   * (tool_result) by spanId, plus the shared per-message parse cache. Owned by a
   * dedicated module (createSpanIndex); the store only keeps it in step with the
   * in-memory window via reindexSpans.
   */
  const spanIdx = createSpanIndex()
  /**
   * In-flight history-fetch controller per agent. A new jump/load supersedes
   * the prior one: starting a fetch aborts the previous controller so a slow or
   * hung request can't leave fetchingOlder/fetchingNewer wedged and block all
   * further pagination. The underlying RPC may still complete; superseded
   * callers detect `signal.aborted` after their await and discard the result.
   */
  const fetchAbort = new Map<string, AbortController>()
  const fetchWatchCleanup = new Map<string, () => void>()

  /**
   * In-flight controller for the background reconnect catch-up loop
   * (catchUpToTail), kept SEPARATE from fetchAbort: a user-driven history fetch
   * aborts it (via beginHistoryFetch) so the two can't race on the window, but
   * starting catch-up must NOT abort an unrelated cold-start / user fetch -- so
   * catch-up never touches fetchAbort itself.
   */
  const catchUpAbort = new Map<string, AbortController>()

  /**
   * Begin a superseding history fetch for `agentId`: abort any in-flight one
   * (and any background catch-up loop), install a fresh controller, and reset
   * both direction flags so a hung prior fetch can't leave them stuck (the caller
   * sets its own flag immediately after). Returns the new controller's signal;
   * the caller bails if it sees `signal.aborted` after an await, and clears its
   * flag in `finally` only when NOT aborted (a superseding fetch already owns the
   * flags by then).
   *
   * `watchSignal` (when given) ties the fetch to the CURRENT WatchEvents
   * subscription, so a workspace switch / worker change that aborts the stream
   * also aborts this fetch -- used by the reconcile-driven empty-window re-seat
   * (jumpToLatestMessages) so it can't leak a LATEST page into a navigated-away
   * worker's window. A user-driven fetch omits it (already scoped to the active tab).
   */
  function beginHistoryFetch(agentId: string, watchSignal?: AbortSignal): AbortSignal {
    fetchAbort.get(agentId)?.abort()
    fetchWatchCleanup.get(agentId)?.()
    fetchWatchCleanup.delete(agentId)
    // A user-driven fetch also supersedes a background reconnect catch-up loop,
    // aborting its in-flight request so the user's jump/scroll owns the tail.
    catchUpAbort.get(agentId)?.abort()
    const controller = new AbortController()
    fetchAbort.set(agentId, controller)
    fetchWatchCleanup.set(agentId, linkWatchSignal(controller, watchSignal))
    setState('fetchingOlder', agentId, false)
    setState('fetchingNewer', agentId, false)
    // A superseding user fetch (jump/scroll, incl. a scroll-up loadOlderMessages) owns
    // the tail now, so cancel any pending exhaustion-forced auto-fill: the user's fetch
    // resolves the tail (or, on a scroll-up, the reader has left it and shouldn't be
    // auto-followed). forwardFillToLiveTail re-arms it only if its own fill re-parks.
    setState('tailFillDeferred', agentId, false)
    return controller.signal
  }

  /**
   * Run a superseding history fetch for `agentId`: begin (aborting any prior),
   * mark `flag` in-flight, run `body(signal)`, then clear `flag` in `finally`
   * only when this fetch wasn't itself superseded (a newer fetch already owns
   * the flags by then). The body must bail early when it observes
   * `signal.aborted` after an await, so a superseded result is discarded.
   */
  async function runHistoryFetch(
    agentId: string,
    flag: 'fetchingOlder' | 'fetchingNewer',
    body: (signal: AbortSignal) => Promise<void>,
    watchSignal?: AbortSignal,
  ): Promise<void> {
    const signal = beginHistoryFetch(agentId, watchSignal)
    setState(flag, agentId, true)
    try {
      await body(signal)
    }
    finally {
      // Clear the in-flight flag UNLESS a superseding fetch already owns it. A
      // superseding beginHistoryFetch installs a FRESH controller (and resets both
      // flags), so our controller no longer being the installed one means a newer
      // fetch is in charge -- leave its flag be. The earlier `!signal.aborted` guard
      // missed the case where `watchSignal` (the WatchEvents subscription) aborts us
      // with NO superseding fetch: a workspace switch / worker change that tears the
      // stream down mid-flight then stranded `fetchingNewer = true`, wedging
      // loadNewerPage (and the empty-window re-seat, both gated on the flag) for that
      // agent until an unrelated user fetch reset it. Our controller stays installed
      // in that case, so the identity check clears the flag.
      if (fetchAbort.get(agentId)?.signal === signal) {
        setState(flag, agentId, false)
        fetchWatchCleanup.get(agentId)?.()
        fetchWatchCleanup.delete(agentId)
      }
    }
  }
  /**
   * Rebuild the span index for an agent from its current in-memory window.
   * Span lookups are window-scoped, so any structural change that drops or
   * reorders messages (trim, prepend, window replace) must reindex: otherwise
   * trimmed-away messages leak into the index (growing it unbounded and
   * defeating the windowing's memory goal). createSpanIndex routes by message
   * classification, so a re-fetched opener can't be misfiled as a result.
   */
  function reindexSpans(agentId: string) {
    spanIdx.reindex(agentId, state.messagesByAgent[agentId] ?? [])
  }

  // Per-message content-version counters (chatContentVersions): bumped on the rare
  // in-place same-seq merge, which preserves the store proxy reference so neither <For>
  // nor the classified-entry cache (which keys freshness on seq) would otherwise see
  // the content swap. The entry cache folds the version into its freshness check and
  // the off-screen height estimate folds it into its key, both reading it reactively so
  // the bump wakes them to re-classify / re-estimate. See the slice for the full why.
  const contentVersions = createContentVersionStore()

  function spanRevisionOf(message: AgentChatMessage | undefined): SpanMessageRevision | undefined {
    return message === undefined
      ? undefined
      : { id: message.id, seq: message.seq, contentVersion: contentVersions.get(message.id) }
  }

  /** Remove content versions for rows that left the window. */
  function forgetContentVersions(droppedIds: Iterable<string>) {
    const ids = [...droppedIds]
    contentVersions.forget(ids)
  }

  /** Remove content versions and streams for rows that left the window. */
  function reclaimDroppedRows(agentId: string, prev: AgentChatMessage[], kept: AgentChatMessage[]) {
    const keptIds = new Set(kept.map(m => m.id))
    forgetContentVersions(prev.filter(m => !keptIds.has(m.id)).map(m => m.id))
    commandStreams.pruneSpans(agentId, prunableSpanIdsSparingBuffered(agentId, prev, kept))
  }

  /**
   * Reclaim ALL per-agent state when an agent is closed for good. The windowing
   * core and every composed sub-store only ever trim WITHIN a window or reclaim a
   * row as it leaves -- none of them reclaims on agent close, so without this a
   * long session that opens and closes many agents leaks one entry per agent
   * across messagesByAgent, the pagination flags, the live tail, the command
   * streams (incl. the non-reactive orphan set), the span index, and the four
   * per-agent sub-stores. Mirrors useAgentOperations.handleAgentClose's existing
   * controlStore/attachment cleanup for the chat slice it omitted.
   */
  function forgetAgent(agentId: string) {
    // Abort any in-flight history fetch / catch-up loop and drop their controllers
    // so a hung request can't write back into the just-cleared window.
    fetchAbort.get(agentId)?.abort()
    fetchAbort.delete(agentId)
    fetchWatchCleanup.get(agentId)?.()
    fetchWatchCleanup.delete(agentId)
    catchUpAbort.get(agentId)?.abort()
    catchUpAbort.delete(agentId)
    // Remove content versions, streams, and span indexes for the agent.
    const rows = state.messagesByAgent[agentId] ?? []
    forgetContentVersions(rows.map(m => m.id))
    commandStreams.forgetAgent(agentId)
    toolProgress.clearAgent(agentId)
    spanIdx.reindex(agentId, [])
    // Delete (not blank) every per-agent key in the window core's records so a
    // closed agent leaves no residue.
    setState(produce((s) => {
      delete s.messagesByAgent[agentId]
      delete s.hasMoreOlder[agentId]
      delete s.hasMoreNewer[agentId]
      delete s.tailFillDeferred[agentId]
      delete s.catchingUp[agentId]
      delete s.fetchingOlder[agentId]
      delete s.fetchingNewer[agentId]
      delete s.initialLoadComplete[agentId]
      delete s.messageVersion[agentId]
    }))
    // Drop the agent's entry in each composed per-agent sub-store.
    liveTail.forget(agentId)
    messageMarks.forget(agentId)
    markSeeder.forget(agentId)
    // The rail's hover-preview cache is module-global (survives rail remounts), so it
    // must be pruned explicitly here or it leaks -- and a stale entry would outlive a
    // close/reopen of the same agentId. See chatMarkPreview.forgetMarkPreview.
    forgetMarkPreview(agentId)
    streaming.remove(agentId)
    todos.remove(agentId)
    backgroundTasks.remove(agentId)
    viewportScroll.remove(agentId)
  }

  /**
   * Drop loaded transcript rows in the phantom band -- seq > latestSeq, except live arrivals
   * exempted above reapCeilingSeq (broadcast during catch-up, so post-replay, not a
   * deletion the client missed) -- reclaiming their per-id side-state and command
   * streams and re-indexing the smaller window, exactly as a trim / delete does, then
   * recompute hasMoreNewer against the surviving tail. The "drop rows past the
   * authoritative tail" half of reconcileAuthoritativeTail, split out so it can be
   * reasoned about and tested apart from the indeterminate-probe / setAuthoritative
   * decision. No-op when nothing loaded falls in the band.
   */
  function reapPhantomRows(agentId: string, latestSeq: bigint, reapCeilingSeq?: bigint) {
    const prev = state.messagesByAgent[agentId]
    if (!prev || prev.length === 0)
      return
    const survivors = prev.filter(m => !isReapablePhantom(m.seq, latestSeq, reapCeilingSeq))
    if (survivors.length === prev.length)
      return // nothing loaded in the phantom band -- window already consistent
    // Drop the scroll-rail marks of the reaped rows -- rows the client held but the
    // worker deleted while we were disconnected. Without this, a reaped marked row
    // (a USER_MESSAGE / CONTROL_RESPONSE) strands its dot: the mark's seq now exceeds
    // the lowered maxSeq, so the reseed's beyond-horizon preserve (seed's
    // freshBeyondSnapshot) keeps it -- indistinguishable from a live send racing the
    // reseed -- and it resurfaces as a ghost dot the moment a later append raises maxSeq
    // past it. The mark store bumps its revision on each real drop.
    for (const m of prev) {
      if (isReapablePhantom(m.seq, latestSeq, reapCeilingSeq))
        messageMarks.remove(agentId, m.seq)
    }
    reclaimDroppedRows(agentId, prev, survivors)
    spanIdx.reindex(agentId, survivors)
    setState('messagesByAgent', agentId, survivors)
    // After the reap the window holds rows <= latestSeq PLUS any live arrivals
    // exempted above reapCeilingSeq (whose seq can EXCEED latestSeq), so the
    // surviving server tail is not bounded by latestSeq. hasMoreNewer is true only
    // when that tail fell strictly BELOW the authoritative tail -- rows up to
    // latestSeq remain unloaded; a surviving live arrival (tail > latestSeq) is the
    // newest known row and correctly yields false.
    setState('hasMoreNewer', agentId, (lastMessageSeq(survivors) ?? 0n) < latestSeq)
  }

  /**
   * Reconcile the loaded window to the authoritative live-tail seq the worker reports
   * at catch-up (CatchUpStart/Complete.latest_seq). It drops a loaded row whose
   * sequence exceeds `latestSeq` and clamps its
   * recorded live-tail -- so the "new messages below" affordance can't stay stuck past a
   * now-shorter history. An UNSET (`undefined`) `latestSeq` means the worker couldn't
   * determine the tail (query error); skip rather than trim against a value we don't trust.
   *
   * `reapCeilingSeq` (CatchUpComplete.start_tail_seq -- the tail when replay BEGAN)
   * exempts live arrivals from the reap: a row ABOVE it was broadcast DURING catch-up
   * (its seq post-dates replay, so it can't be a deletion the client missed) and the
   * worker registers the watcher before reading the tail, so such a frame can land
   * BEFORE this one. Only the (latestSeq, reapCeilingSeq] band -- rows that existed at
   * catch-up start and were deleted during replay -- is reaped. Omitted at CatchUpStart
   * (no live arrival can be in the window yet), where every row beyond the tail is a
   * phantom.
   *
   * `probeIndeterminate` (true only at CatchUpComplete, when the bounded replay is DONE)
   * handles an indeterminate (unset) tail: the worker couldn't read its max seq, so liveTail
   * was never raised and the continuous reconcile would read the loaded tail as caught up
   * even though a bounded replay may have stopped short. Nudge the recorded live tail one
   * past the loaded tail so the reconcile PROBES (caughtUpToLiveTail reads false) and
   * catchUpToTail drains to the real tail; settleToWindow clamps the nudge back down if
   * nothing's there. Skipped at CatchUpStart (the replay hasn't run, so a probe would
   * race it).
   */
  function reconcileAuthoritativeTail(agentId: string, latestSeq: bigint | undefined, reapCeilingSeq?: bigint, probeIndeterminate = false) {
    if (latestSeq === undefined) {
      if (probeIndeterminate) {
        const windowTail = lastMessageSeq(state.messagesByAgent[agentId] ?? []) ?? 0n
        if (windowTail > 0n)
          liveTail.bump(agentId, windowTail + 1n)
      }
      return
    }
    liveTail.setAuthoritative(agentId, latestSeq, reapCeilingSeq)
    reapPhantomRows(agentId, latestSeq, reapCeilingSeq)
  }

  /**
   * Update a message already in the window (matched by id): a same-seq in-place
   * merge or a reseq reinsert. The same-seq path uses the index path-setter so
   * the store proxy reference is preserved. A new sequence removes the old entry
   * and reinserts it so the visible order follows the sequence.
   */
  function updateExistingMessage(agentId: string, prev: AgentChatMessage[], existingIdx: number, message: AgentChatMessage): boolean {
    if (prev[existingIdx].seq === message.seq) {
      const proxy = prev[existingIdx]
      // A duplicate/replayed broadcast can re-deliver a byte-identical row (same id,
      // same seq, same content) -- e.g. a reconnect replay or an at-least-once stream
      // dupe overlapping the loaded window. Skip the whole merge then: the setState,
      // the cache evictions, the version bump, AND the caller's O(window) reindexSpans
      // are pure churn (re-classify, re-parse, re-estimate, re-index, wake auto-scroll)
      // for content that didn't change. sameAgentMessage compares every field incl. the
      // bytes content, so a real same-seq body change still falls through. unwrap() drops
      // the solid store proxy first so the serializer reads the raw fields.
      if (sameAgentMessage(unwrap(proxy), message))
        return false
      setState('messagesByAgent', agentId, existingIdx, message)
      // The merge keeps the store-proxy reference and seq, so the by-reference
      // parse/classify caches (parseMessageContent, classifyAgentMessage, the span
      // index) -- all built on the "a message is immutable" assumption -- would keep
      // serving the pre-update derivation. Evict them for the mutated proxy, and bump
      // the content version so the classified-entry cache + height estimate rebuild
      // against the fresh content (seq alone can't reveal a same-seq body change).
      invalidateMessageParseCache(proxy)
      invalidateMessageClassificationCache(proxy)
      spanIdx.invalidate(proxy)
      contentVersions.bump(message.id)
      return true
    }
    const without = prev.filter((_, i) => i !== existingIdx)
    setState('messagesByAgent', agentId, insertMessageBySeq(without, message))
    return true
  }

  /** Whether any row currently in the window carries `spanId`. */
  function spanStillReferenced(agentId: string, spanId: string): boolean {
    return state.messagesByAgent[agentId]?.some(m => m.spanId === spanId) ?? false
  }

  /**
   * Clear a dropped row's live command stream, sparing + recording it as orphaned
   * when still mid-flight. The single-row analogue of prunableSpanIdsSparingBuffered;
   * the survivor check (does a surviving row still carry the span?) lives here
   * because it reads the window, and the spare-vs-clear policy lives in the
   * command-stream slice. A sequence change beyond the window uses this helper.
   */
  function clearDroppedSpanStreamIfUnreferenced(agentId: string, droppedSpanId: string | undefined) {
    if (!droppedSpanId)
      return
    commandStreams.spareOrClearDroppedSpan(agentId, droppedSpanId, spanStillReferenced(agentId, droppedSpanId))
  }

  /**
   * The span ids a structural drop may safely prune, given the dropped rows and the
   * survivors that remain. Applies the survivor rule (prunableDroppedSpanIds spares
   * any span a SURVIVING row still references -- e.g. a tool_use/tool_result pair
   * split across the boundary), then hands the survivor-filtered candidates to the
   * command-stream slice, which spares + records any still-buffered span (the
   * spare-vs-record policy lives next to the buffers it governs). Shared by both
   * trims, the merge, and the full-window replace so the rule has ONE home.
   */
  function prunableSpanIdsSparingBuffered(agentId: string, dropped: AgentChatMessage[], survivors: AgentChatMessage[]): string[] {
    return commandStreams.prunableSparingBuffered(agentId, prunableDroppedSpanIds(dropped, survivors))
  }

  /**
   * A reseq (notification consolidation assigns the next monotonic seq,
   * message_seq_hwm+1) moved an existing row to a seq beyond the scrolled-away
   * window tail. Reinserting it there would
   * tear a [oldTail..newSeq) hole AND advance getLastSeq to newSeq, making
   * caughtUpToLiveTail trivially true while history is still unloaded -- the
   * forward-fetch cursor would then skip the gap and the tail could never be
   * reached. Drop the moved row from its old position instead; latestLiveSeq
   * (bumped by the caller) records the new tail, so loadNewerPage /
   * jumpToLatestMessages re-fetch it contiguously when the user returns.
   */
  function handleReseqMovedBeyondWindow(agentId: string, prev: AgentChatMessage[], existingIdx: number) {
    const dropped = prev[existingIdx]
    setState('messagesByAgent', agentId, prev.filter((_, i) => i !== existingIdx))
    // Reclaim the content version when the row leaves the window. A notification
    // can receive an in-place update before it moves to a new sequence.
    forgetContentVersions([dropped.id])
    // The reseq'd row left the loaded window (it's re-fetched on return), so
    // prune its command stream to stay window-bounded -- subject to the shared
    // survivor rule, sparing AND recording a still-streaming span so its
    // mid-flight buffer survives but can't leak if the stream never ends.
    clearDroppedSpanStreamIfUnreferenced(agentId, dropped.spanId)
  }

  /**
   * Return the window and its end index when it exceeds `maxCount` messages.
   */
  function windowOverMessageCap(agentId: string, maxCount: number): { prev: AgentChatMessage[], messageEnd: number } | null {
    const prev = state.messagesByAgent[agentId]
    if (!prev || prev.length <= maxCount)
      return null
    const messageEnd = transcriptMessageEnd(prev)
    if (messageEnd <= maxCount)
      return null
    return { prev, messageEnd }
  }

  /**
   * Commit a trimmed window: install the kept rows, flag the side that now has
   * more beyond the window, re-index spans to the (smaller) window, and prune the
   * command streams of the dropped spans. The shared tail of trimNewestEnd /
   * trimOldestEnd; each computes its own kept rows + droppedSpanIds first (the
   * parts that genuinely differ) and hands them here.
   */
  function commitTrim(
    agentId: string,
    survivors: AgentChatMessage[],
    hasMoreField: 'hasMoreOlder' | 'hasMoreNewer',
    droppedSpanIds: string[],
  ) {
    setState('messagesByAgent', agentId, survivors)
    setState(hasMoreField, agentId, true)
    reindexSpans(agentId)
    commandStreams.pruneSpans(agentId, droppedSpanIds)
  }

  /**
   * Merge a fetched page into the in-memory window, deduped by seq, and index
   * its span ids:
   *  - 'older': prepend the page before the window (older history).
   *  - 'newer': insert the page in sequence order and advance the live tail.
   */
  function mergeFetchedMessages(agentId: string, fetched: AgentChatMessage[], side: 'older' | 'newer') {
    if (fetched.length === 0)
      return
    // Snapshot the pre-merge window so the merge can prune dropped streams.
    const prevWindow = state.messagesByAgent[agentId] ?? []
    // The pure window-merge (dedup / reseq-collision / seq-ordered insert / older
    // prepend vs newer splice) lives in chatMessageOrder.mergeWindow so its rules are
    // unit-testable on plain arrays; the reactive side effects below stay here.
    setState('messagesByAgent', agentId, (prev = []) =>
      mergeWindow(prev, fetched, side))
    // Rebuild the span index over the merged, seq-ascending window rather than
    // incrementally indexing only the fetched page: a prepended opener whose
    // result is already in the window would otherwise be misfiled, and the
    // 'older' prepend never re-establishes opener-first ordering on its own.
    reindexSpans(agentId)
    // Prune the command streams of spans the pre-merge window carried that the
    // The survivor filter keeps a span that any merged row still uses.
    const merged = state.messagesByAgent[agentId] ?? []
    // Reclaim content versions and command streams for rows that left the window.
    reclaimDroppedRows(agentId, prevWindow, merged)
    if (side === 'newer') {
      for (const msg of fetched)
        liveTail.bump(agentId, msg.seq)
    }
  }

  /** Shared implementation for setMessages / loadInitialMessages. */
  function applyMessages(agentId: string, messages: AgentChatMessage[], hasMore: boolean) {
    const prevRows = state.messagesByAgent[agentId] ?? []
    const finalMessages = messages
    // Reclaim content versions and command streams for rows that leave the window.
    // The survivor filter keeps a stream that a final row still uses.
    reclaimDroppedRows(agentId, prevRows, finalMessages)
    // Rebuild the span index before the update wakes reactive computations.
    spanIdx.reindex(agentId, finalMessages)
    setState('messagesByAgent', agentId, finalMessages)
    setState('hasMoreOlder', agentId, hasMore)
    // Default to "at the live tail": initial load, reconnect snapshot, and
    // jump-to-latest all land on the latest page, so there are no newer messages
    // beyond the window. The one caller that seeds a NON-tail window
    // (jumpToOldestMessages) overrides hasMoreNewer immediately after this returns.
    setState('hasMoreNewer', agentId, false)
    setState('initialLoadComplete', agentId, true)
    for (const msg of messages) {
      liveTail.bump(agentId, msg.seq)
    }
  }

  const baseStore = {
    state,

    getMessages(agentId: string): AgentChatMessage[] {
      return state.messagesByAgent[agentId] ?? []
    },

    /**
     * Return the parsed tool_use message for a spanId, or undefined when no
     * tool_use is indexed for it. The parse is cached per message instance.
     */
    getToolUseParsedBySpanId(agentId: string, spanId: string): ParsedMessageContent | undefined {
      return spanIdx.getOpenerParsed(agentId, spanId)
    },

    /**
     * The content version of the tool_use OPENER paired with `spanId` (0 when no
     * opener is indexed). A tool_result sizes its diff from the opener's parsed
     * input, but the opener is a DIFFERENT message: an in-place same-seq body
     * replacement of the opener bumps the OPENER's content version, not the
     * result's, and leaves the result's seq/id/proxy untouched. Read REACTIVELY
     * (it reads the messageContentVersions store) and folded into the result row's
     * classified-entry freshness check and height-estimate key, so an opener change
     * busts the off-screen result's stale classification / estimate.
     */
    getToolUseContentVersionBySpanId(agentId: string, spanId: string): number {
      const openerId = spanIdx.getOpenerId(agentId, spanId)
      return openerId !== undefined ? contentVersions.get(openerId) : 0
    },

    getToolUseRevisionBySpanId(agentId: string, spanId: string): SpanMessageRevision | undefined {
      return spanRevisionOf(spanIdx.getOpenerMessage(agentId, spanId))
    },

    /** Symmetric counterpart for the tool_result side. */
    getToolResultParsedBySpanId(agentId: string, spanId: string): ParsedMessageContent | undefined {
      return spanIdx.getResultParsed(agentId, spanId)
    },

    /**
     * The content version of the tool_result paired with `spanId` (0 when no
     * result is indexed). Some tool_use rows render from hidden result data, so a
     * result-side in-place body replacement must bust the opener row's cached
     * classification and measured height.
     */
    getToolResultContentVersionBySpanId(agentId: string, spanId: string): number {
      const resultId = spanIdx.getResultId(agentId, spanId)
      return resultId !== undefined ? contentVersions.get(resultId) : 0
    },

    getToolResultRevisionBySpanId(agentId: string, spanId: string): SpanMessageRevision | undefined {
      return spanRevisionOf(spanIdx.getResultMessage(agentId, spanId))
    },

    setMessages(agentId: string, messages: AgentChatMessage[], hasMore = false) {
      applyMessages(agentId, messages, hasMore)
    },

    /**
     * Whether `seq` would land beyond the loaded tail (getLastSeq) while NEWER history
     * is still unloaded there, so appending it would tear a gap. Three detectors:
     *  - hasMoreNewer: the reader scrolled away from the tail.
     *  - DURING a reconnect catch-up (state.catchingUp): seq-CONTIGUITY -- a frame more
     *    than one past the loaded tail (seq > lastSeq + 1) is a live arrival past the
     *    bounded replay's still-unfilled gap, so dropping it (recorded in liveTail) lets
     *    the continuous reconcile forward-fill (lastSeq, seq] contiguously rather than
     *    splice a hole; a CONTIGUOUS frame is the next in-order replay page, kept. This
     *    branch is what makes catch-up robust when the worker can't report its tail
     *    (latest_seq unset, a DB error): liveTail then only tracks the LOADED tail, so the
     *    live-tail comparison below can't see a beyond-tail live frame.
     *  - in the LIVE phase: the loaded tail provably lags a KNOWN higher live tail
     *    (lastSeq < recordedLiveTail) and `seq` is beyond it (seq > recordedLiveTail).
     *    Used here rather than contiguity so a message after a reconciled sequence gap
     *    can splice into the window instead of forcing a needless re-fetch.
     * An empty window has no sequence cursor and no gap to protect.
     *
     * `recordedLiveTail` is the live tail known BEFORE this message bumped it (so a live
     * arrival is measured against the tail seen so far, not against itself).
     */
    beyondUnloadedNewerTail(agentId: string, seq: bigint, recordedLiveTail: bigint): boolean {
      const lastSeq = this.getLastSeq(agentId)
      if (lastSeq === 0n || seq <= lastSeq)
        return false
      if (state.hasMoreNewer[agentId])
        return true
      if (state.catchingUp[agentId])
        return seq > lastSeq + 1n
      return lastSeq < recordedLiveTail && seq > recordedLiveTail
    },

    /**
     * Whether a fresh (not-yet-present) message would tear a gap into the loaded
     * window and so must be dropped rather than spliced in. True only for a
     * persisted message that lands outside the contiguous window on a
     * side that still has unloaded history:
     *  - past the loaded tail (seq > lastSeq) while NEWER history is unloaded -- the
     *    scrolled-away-from-tail case (hasMoreNewer) OR a bounded catch-up replay whose
     *    gap toward the live tail isn't filled yet (see beyondUnloadedNewerTail).
     *  - before the loaded head (seq < firstSeq) while OLDER history is unloaded
     *    (hasMoreOlder): e.g. a connect-time WatchEvents replay of the OLDEST page
     *    arriving in front of a freshly-loaded LATEST page -- this is what produced
     *    the [seq 1 ... gap ... latest] window.
     * An in-range gap-fill (firstSeq <= seq <= lastSeq) is allowed through.
     * Dropped messages are not lost: latestLiveSeq
     * records them and paging toward the edge (loadOlder/loadNewer/jump) re-fetches
     * the range contiguously. An empty window has no gap to protect.
     *
     * `recordedLiveTail` is the live tail known BEFORE this message bumped it (passed
     * through to beyondUnloadedNewerTail's live-phase comparison).
     */
    shouldDropBeyondWindow(agentId: string, message: AgentChatMessage, recordedLiveTail: bigint): boolean {
      const firstSeq = this.getFirstSeq(agentId)
      const beyondTail = this.beyondUnloadedNewerTail(agentId, message.seq, recordedLiveTail)
      const beforeHead = !!state.hasMoreOlder[agentId] && firstSeq !== 0n && message.seq < firstSeq
      return beyondTail || beforeHead
    },

    /**
     * Whether an EXISTING-row update is a reseq broadcast (notification consolidation
     * marks the moved row with previous_seq > 0) whose NEW seq lands beyond the
     * scrolled-away window's still-unfilled newer gap. Such a row must be DROPPED from
     * its old position rather than reinserted at the new seq (handleReseqMovedBeyondWindow),
     * which would tear a [oldTail..newSeq) hole and falsely advance getLastSeq. The third
     * sibling of the beyond-window predicate family (beyondUnloadedNewerTail /
     * shouldDropBeyondWindow): a method because it reads beyondUnloadedNewerTail.
     */
    isReseqMovedBeyondWindow(agentId: string, message: AgentChatMessage, recordedLiveTail: bigint): boolean {
      return message.previousSeq > 0n
        && this.beyondUnloadedNewerTail(agentId, message.seq, recordedLiveTail)
    },

    /**
     * The EXISTING-ROW arm of addMessage: a message whose id is already in the window
     * (existingIdx). A reseq (notification consolidation assigns the next monotonic seq,
     * message_seq_hwm+1) moves the row to the live tail, marked EXPLICITLY with
     * previous_seq > 0 (the old seq). When the new seq lands in an unloaded gap beyond
     * the window -- the reader scrolled away, OR a bounded catch-up replay hasn't filled
     * the gap yet (beyondUnloadedNewerTail) -- drop the moved row from its old position
     * (handleReseqMovedBeyondWindow; latestLiveSeq, bumped by addMessage, records the new
     * tail). Otherwise update in place / reseq-reinsert (updateExistingMessage). Returns
     * whether the id ends up in the window and whether the call mutated it (changed=false
     * is an identical same-seq re-delivery -- a true no-op).
     */
    updateExistingArm(agentId: string, messages: AgentChatMessage[], existingIdx: number, message: AgentChatMessage, recordedLiveTail: bigint): { inWindow: boolean, changed: boolean } {
      const reseqMovedBeyondWindow = this.isReseqMovedBeyondWindow(agentId, message, recordedLiveTail)
      let inWindow: boolean
      let changed = true
      if (reseqMovedBeyondWindow) {
        handleReseqMovedBeyondWindow(agentId, messages, existingIdx)
        inWindow = false
      }
      else {
        changed = updateExistingMessage(agentId, messages, existingIdx, message)
        inWindow = true
      }
      // An in-place merge, reseq-reinsert, or beyond-window drop can leave a stale or
      // misordered span entry, so rebuild from the seq-ascending window -- unless nothing
      // changed (an identical same-seq re-delivery), where it's pure churn.
      if (changed)
        reindexSpans(agentId)
      return { inWindow, changed }
    },

    /**
     * The FRESH-INSERT arm of addMessage: a message whose id is NOT in the window.
     * Inserts by sequence or discards a pure same-sequence duplicate
     * under a different id) -- and incrementally indexes its span. applyFreshMessage
     * returns the SAME array reference on a pure dedup-discard (nothing inserted, no
     * dropped), so `changed` is false there; any real change yields a new array.
     * Returns whether the id was actually inserted (inWindow) and whether the window
     * mutated (changed).
     */
    freshInsertArm(agentId: string, messages: AgentChatMessage[], message: AgentChatMessage): { inWindow: boolean, changed: boolean } {
      const { next, inserted } = applyFreshMessage(messages, message)
      const changed = next !== messages
      setState('messagesByAgent', agentId, next)
      // Index only a message that was actually inserted: the seq dedup can DISCARD it,
      // and indexing a discarded message would point a span slot at a row absent from the
      // window. The incremental index also falls back to a full rebuild when it would
      // reassign a spanId to a different message id (a re-broadcast under a new id, with
      // the old instance still in the window).
      if (inserted && spanIdx.index(agentId, message))
        reindexSpans(agentId)
      return { inWindow: inserted, changed }
    },

    addMessage(agentId: string, message: AgentChatMessage): boolean {
      // The live tail known BEFORE this message bumps it: the live-append guard's
      // live-phase comparison measures a beyond-tail arrival against the tail seen so far,
      // not against its own seq -- see beyondUnloadedNewerTail.
      const recordedLiveTail = liveTail.get(agentId)
      // Track the live tail seq even for messages we're about to drop, so
      // jumpToLatestMessages knows where the true tail is.
      liveTail.bump(agentId, message.seq)

      // Record the scroll-rail mark BEFORE any beyond-window drop, so a message the
      // reader scrolled away from still gets its jump dot. The mark rides the proto
      // set at write time by the worker.
      // A reseq MOVE (previousSeq set) carries the mark to its new seq, so drop the
      // stale mark at the vacated old seq first, or it strands a ghost dot. (Threaded
      // rows are unmarked today, so this is latent -- but the worker now carries
      // mark_type on the MOVE broadcast, so keep the two ends symmetric.) noteMark/remove
      // bump the marks store's own seed-race revision only on a real change: an unmarked
      // reseq MOVE (remove of a never-marked seq) or a re-broadcast of an already-noted
      // mark are no-ops that must not perturb a concurrent loadMessageMarks.
      if (message.previousSeq !== 0n)
        messageMarks.remove(agentId, message.previousSeq)
      if (message.markType !== MarkType.UNSPECIFIED)
        messageMarks.noteMark(agentId, message.seq, message.markType)

      // Notification thread update: LEAPMUX notification messages can be updated
      // in-place when consolidating. Check if a message with this ID exists.
      const messages = state.messagesByAgent[agentId] ?? []
      const existingIdx = messages.findLastIndex(m => m.id === message.id)

      // Live-append guard: drop a fresh transcript message that would tear a gap into the
      // loaded window (see shouldDropBeyondWindow). In-place updates (existingIdx !== -1)
      // are never dropped here.
      if (existingIdx === -1 && this.shouldDropBeyondWindow(agentId, message, recordedLiveTail))
        return false

      // Dispatch to the existing-row or fresh-insert path. `inWindow` is whether
      // message.id actually ends up loaded (a reseq-beyond-window drop / seq-dedup
      // discard leave it absent); `changed` is whether the window mutated (false on a
      // pure dedup-discard or an identical same-seq re-delivery, where a version bump
      // would only re-run the auto-scroll effect + entry cache for nothing).
      const { inWindow, changed } = existingIdx !== -1
        ? this.updateExistingArm(agentId, messages, existingIdx, message, recordedLiveTail)
        : this.freshInsertArm(agentId, messages, message)

      if (changed)
        bumpMessageVersion(agentId)
      return inWindow
    },

    getLastSeq(agentId: string): bigint {
      // The store uses 0n as the empty-window sentinel.
      return lastMessageSeq(state.messagesByAgent[agentId] ?? []) ?? 0n
    },

    appendCommandStream(agentId: string, spanId: string, segmentKind: CommandStreamSegment['kind'], text: string) {
      commandStreams.append(agentId, spanId, segmentKind, text)
    },

    getCommandStream(agentId: string, spanId: string): CommandStreamSegment[] {
      return commandStreams.get(agentId, spanId)
    },

    /** Every span's live segments for an agent ({} when none). */
    getAgentCommandStreams(agentId: string): Record<string, CommandStreamSegment[]> {
      return commandStreams.getByAgent(agentId)
    },

    /**
     * Whether `spanId`'s command stream has renderable content to show (reactive).
     * The classified-entry cache reads it to flip a row hidden<->visible the moment
     * its span first has renderable stream content OR is cleared -- a presence bit
     * that changes only on those two events, so subscribing to it (unlike the
     * per-delta segment array) doesn't re-classify the window on every chunk. NOT
     * "actively streaming right now": a span stays renderable after its producer
     * goes quiet, until its stream ends or its buffer is pruned.
     */
    hasRenderableCommandStream(agentId: string, spanId: string): boolean {
      return commandStreams.hasRenderableContent(agentId, spanId)
    },

    /**
     * The row's content version (see messageContentVersions): 0 until its first
     * in-place same-seq body replacement, then incremented per replacement. Read
     * REACTIVELY by the classified-entry cache (and folded into the height estimate
     * key) so a same-seq content swap -- which keeps the id, seq, and proxy identity,
     * and so wouldn't otherwise wake the cache's memo -- still invalidates them.
     */
    getMessageContentVersion(id: string): number {
      return contentVersions.get(id)
    },

    clearCommandStream(agentId: string, spanId: string) {
      // clear forgets any orphan record in lockstep (via the slice's dropSpan), so a
      // normal stream-end reclaims an orphaned-on-drop span without a separate call.
      commandStreams.clear(agentId, spanId)
    },

    /** Merge one `running_tool` broadcast into its span's live progress. */
    applyToolProgress(agentId: string, update: ToolProgressUpdate) {
      toolProgress.apply(agentId, update)
    },

    /**
     * One span's live tool progress, or undefined when nothing is running there.
     * Read through the row's render context, by a leaf component that subscribes
     * on its own -- see ToolRunningBadge.
     */
    getToolProgress(agentId: string, spanId: string): ToolProgressEntry | undefined {
      return toolProgress.get(agentId, spanId)
    },

    /** Drop one span's tool progress -- its result row landed. */
    dropToolProgress(agentId: string, spanId: string) {
      toolProgress.drop(agentId, spanId)
    },

    /**
     * Drop every span's tool progress for an agent. Called at the boundaries the
     * WORKER cannot observe (turn end, agent inactive, context cleared), which is
     * why no provider sends an end message for a running tool.
     */
    clearToolProgress(agentId: string) {
      toolProgress.clearAgent(agentId)
    },

    /**
     * Reclaim command streams orphaned by a spared mid-stream drop (delete /
     * beyond-window reseq / trim) that never received their own stream-end. Called
     * at a turn boundary (and the catch-up -> live transition). Delegates to the
     * command-stream slice, supplying the window-state predicate it needs (a span a
     * surviving row still carries is left both buffered and recorded for a later
     * sweep).
     */
    sweepOrphanedBufferedSpans(agentId: string) {
      commandStreams.sweepOrphans(agentId, spanId => spanStillReferenced(agentId, spanId))
    },

    setLoading(loading: boolean) {
      setState('loading', loading)
    },

    /**
     * Cap the window after prepending older history. Keep the oldest
     * `maxCount` messages and flag newer history.
     */
    trimNewestEnd(agentId: string, maxCount: number) {
      const cap = windowOverMessageCap(agentId, maxCount)
      if (!cap)
        return
      const { prev } = cap
      const kept = prev.slice(0, maxCount)
      const dropped = prev.slice(maxCount)
      const survivors = kept
      // Prune the dropped NEWEST spans' command streams so the segment buffers
      // stay bounded by the window. The PRIMARY guard is hasBufferedSegments: a
      // newest-end trim (scroll-up while the agent works) is the one trim that can
      // drop the live tail span, whose buffer is mid-flight -- clearing it would
      // lose the in-progress segments and re-vivify from empty. Buffered (not just
      // renderable) so a span holding only a content-less reasoning_summary_break -- a
      // recorded part boundary that hasRenderableContent deliberately ignores -- is spared too.
      // prunableDroppedSpanIds additionally spares any span a SURVIVING row still
      // references (a tool_use/tool_result pair sharing one spanId, split across the
      // boundary by a sequence change). Both trim paths apply this guard.
      //
      // Record every spared span as orphaned (exactly like trimOldestEnd): the live
      // tail span normally re-fetches and ends on scroll-back, clearing its own
      // record -- but a buffered span that NEVER ends (a content-less
      // reasoning_summary_break for a reasoning item abandoned before completion)
      // would otherwise leak for the session, since no stream-end clears it and the
      // sweep only touches RECORDED orphans. Recording it lets the turn-end /
      // catch-up sweep reclaim it once no surviving row references it.
      const droppedSpanIds = prunableSpanIdsSparingBuffered(agentId, dropped, survivors)
      // Reclaim the content versions of the dropped rows.
      forgetContentVersions(dropped.map(m => m.id))
      commitTrim(agentId, survivors, 'hasMoreNewer', droppedSpanIds)
    },

    /**
     * Cap the window after appending newer history. Keep the newest
     * `maxCount` messages and flag older history.
     */
    trimOldestEnd(agentId: string, maxCount: number) {
      const cap = windowOverMessageCap(agentId, maxCount)
      if (!cap)
        return
      const { prev, messageEnd } = cap
      // The slice start is positive because messageEnd exceeds maxCount.
      const keptMessages = prev.slice(messageEnd - maxCount, messageEnd)
      const survivors = keptMessages
      const droppedOldest = prev.slice(0, messageEnd - maxCount)
      // These oldest messages are historical. Thus,
      // prune their live command streams to keep the segment buffers bounded by
      // the window -- but SPARE any span a surviving row still references: a
      // tool_use opener (oldest, dropped) and its tool_result (kept) can share one
      // spanId, and wiping the dropped opener's stream would blank the kept
      // result's output. A buffered span among the OLDEST rows is usually stale (a
      // still-streaming span normally lives at the tail), but that is NOT an
      // enforced invariant: a long-running tool whose whole exchange is the oldest
      // content while it still streams would lose its in-flight segments if cleared.
      // So mirror trimNewestEnd -- spare any span with buffered segments (renderable
      // or a content-less reasoning_summary_break alike) -- and record it as orphaned
      // so the turn-end or catch-up sweep reclaims a
      // genuinely-stale buffer instead of leaking it, while a real mid-flight stream
      // keeps its segments until it ends.
      const droppedSpanIds = prunableSpanIdsSparingBuffered(agentId, droppedOldest, survivors)
      // Reclaim the content versions of the dropped oldest rows.
      forgetContentVersions(droppedOldest.map(m => m.id))
      commitTrim(agentId, survivors, 'hasMoreOlder', droppedSpanIds)
    },

    /**
     * Live-tail trim that protects a scrolled-up reader's viewport. Keeps at least
     * `minKeepNewest` newest messages -- the rows from the reader's viewport-top
     * anchor down to the tail, which the scroll hook computes so the oldest-end
     * trim never drops a row the reader can see (the cause of a mid-read jump).
     * Clamped to [MAX_LOADED_CHAT_MESSAGES, MAX_LOADED_CHAT_MESSAGES_CEILING]:
     *  - while following the tail the hook passes 0, so this is the normal cap;
     *  - scrolled up it floats the cap up to protect the viewport;
     *  - past the ceiling memory wins and the oldest rows trim regardless, so a
     *    reader pinned to the very oldest rows through a stream that long sees the
     *    same jump strict bounding always had (now only in that extreme).
     */
    trimOldestToViewport(agentId: string, minKeepNewest: number) {
      const target = Math.min(
        MAX_LOADED_CHAT_MESSAGES_CEILING,
        Math.max(MAX_LOADED_CHAT_MESSAGES, minKeepNewest),
      )
      this.trimOldestEnd(agentId, target)
    },

    /**
     * Whether the loaded window has reached the highest live seq ever observed
     * (latestLiveSeq), including messages dropped by the live-append guard while
     * scrolled away. Reaching the persisted tail (has_more false) does not imply
     * this -- a broadcast that landed mid-fetch can sit beyond the window -- so
     * the forward-fetch paths gate the "at the live tail" decision on this, not
     * on has_more alone. `0n` default means "no live seq seen", which any
     * non-negative getLastSeq trivially satisfies.
     */
    caughtUpToLiveTail(agentId: string): boolean {
      return liveTail.caughtUp(agentId, this.getLastSeq(agentId))
    },

    /**
     * The seq to resume a WatchEvents subscription from: the highest seq the
     * client has observed, INCLUDING live messages dropped by the windowed
     * live-append guard (latestLiveSeq). While scrolled away from the tail
     * (hasMoreNewer) the window tail (getLastSeq) lags the live tail, so resuming
     * from getLastSeq would make the worker replay the whole window->live gap --
     * up to a full page of messages the live-append guard immediately drops
     * again. Resuming from the live tail replays only genuinely-new messages; the
     * skipped gap is re-fetched contiguously by loadNewerPage /
     * jumpToLatestMessages when the user returns to the tail.
     */
    getResumeAfterSeq(agentId: string): bigint {
      const lastSeq = this.getLastSeq(agentId)
      const liveSeq = liveTail.get(agentId)
      return liveSeq > lastSeq ? liveSeq : lastSeq
    },

    /** Get the first sequence in the current window. */
    getFirstSeq(agentId: string): bigint {
      // The store uses zero for an empty window.
      return firstMessageSeq(state.messagesByAgent[agentId] ?? []) ?? 0n
    },

    hasOlderMessages(agentId: string): boolean {
      return state.hasMoreOlder[agentId] ?? false
    },

    hasNewerMessages(agentId: string): boolean {
      return state.hasMoreNewer[agentId] ?? false
    },

    /**
     * Whether a forward fill parked with an exhaustion-forced (still-reachable) gap, so
     * the continuous tail-reconcile should RESUME it rather than treat hasMoreNewer as a
     * settled scrolled-away wall (see ChatStoreState.tailFillDeferred / resumeDeferredTailFill).
     */
    isTailFillDeferred(agentId: string): boolean {
      return state.tailFillDeferred[agentId] ?? false
    },

    /**
     * Mark a reconnect catch-up as in flight (true on WatchEvents (re)subscribe) or done
     * (false at CatchUpComplete). While set, the live-append guard uses seq-contiguity
     * instead of the recorded-live-tail comparison, so a beyond-tail live frame that
     * races in during the bounded replay is dropped (and forward-filled) rather than
     * spliced past the unfilled gap -- robust even when the worker's tail is indeterminate
     * (see ChatStoreState.catchingUp / beyondUnloadedNewerTail).
     */
    setCatchingUp(agentId: string, value: boolean) {
      setState('catchingUp', agentId, value)
    },

    /**
     * Whether the in-memory window is within one page of the row ceiling.
     * rows, so the buffer-filler must stop pre-fetching: a further page only SHUFFLES
     * the window (a prepend forces an opposite-end trim and vice versa), reaping the
     * other side's buffer (a ceiling ping-pong) or, at the live tail, the pinned tail.
     *
     * The threshold is CEILING - MESSAGE_PAGE_SIZE, not the ceiling itself, on purpose:
     * a single 50-row page can take serverEnd from just under the ceiling (e.g. 1199)
     * to just over it (1249), and trimNewestEnd only flips hasMoreNewer / drops the
     * live tail once serverEnd EXCEEDS the ceiling. Stopping a full page early keeps the
     * filler's last allowed fetch from crossing the ceiling and dropping the live tail.
     */
    atWindowCeiling(agentId: string): boolean {
      const msgs = state.messagesByAgent[agentId]
      return !!msgs && transcriptMessageEnd(msgs) >= MAX_LOADED_CHAT_MESSAGES_CEILING - MESSAGE_PAGE_SIZE
    },

    isFetchingOlder(agentId: string): boolean {
      return state.fetchingOlder[agentId] ?? false
    },

    isFetchingNewer(agentId: string): boolean {
      return state.fetchingNewer[agentId] ?? false
    },

    isInitialLoadComplete(agentId: string): boolean {
      return state.initialLoadComplete[agentId] ?? false
    },

    getMessageVersion(agentId: string): number {
      return state.messageVersion[agentId] ?? 0
    },
  }

  // Wire the history paginator AFTER baseStore exists, threading its dependencies
  // explicitly (the windowing-core closures + the cross-method store API) rather
  // than via a `this`-bound spread. The store-method deps are arrow-wrapped so each
  // runs with baseStore as its receiver (those methods use `this` to reach
  // siblings). The paginator's methods are plain closures, so merging them in adds
  // no `this` coupling of their own.
  const paginator = createHistoryPaginator({
    state,
    setState,
    catchUpAbort,
    runHistoryFetch,
    mergeFetchedMessages,
    applyMessages,
    liveTail,
    maxLoaded: MAX_LOADED_CHAT_MESSAGES,
    maxLoadedCeiling: MAX_LOADED_CHAT_MESSAGES_CEILING,
    getFirstSeq: agentId => baseStore.getFirstSeq(agentId),
    getLastSeq: agentId => baseStore.getLastSeq(agentId),
    getFirstMessageSeq: agentId => firstMessageSeq(state.messagesByAgent[agentId] ?? []),
    getLastMessageSeq: agentId => lastMessageSeq(state.messagesByAgent[agentId] ?? []),
    caughtUpToLiveTail: agentId => baseStore.caughtUpToLiveTail(agentId),
    addMessage: (agentId, message) => baseStore.addMessage(agentId, message),
    trimOldestEnd: (agentId, maxCount) => baseStore.trimOldestEnd(agentId, maxCount),
    trimNewestEnd: (agentId, maxCount) => baseStore.trimNewestEnd(agentId, maxCount),
    replaceTodos: todos.replace,
    replaceBackgroundTasks: backgroundTasks.replace,
    markBackgroundTasksLoadFailed: backgroundTasks.markLoadFailed,
  })

  // Expose the composed sub-stores directly so consumers reach a slice's own
  // methods (chatStore.todos.replace, chatStore.streamingText.set, ...) instead of
  // a wall of one-line forwarders re-spelling each slice's API on the window store.
  // liveTail is exposed the same way for the recorded-tail reads the store and its
  // tests need. The window core still owns message CRUD / windowing / annotations.
  return Object.assign(baseStore, paginator, {
    forgetAgent,
    reconcileAuthoritativeTail,
    liveTail,
    messageMarks,
    todos,
    backgroundTasks,
    streamingText: streaming,
    viewportScroll,
    /**
     * Reactive scroll-rail data for an agent: the marked seqs, the window-aware whole-history
     * seq range, and the loaded window's bounds. The range rule (seed vs live tail vs window
     * head/tail) lives in the pure {@link resolveRailRange} so it is testable and can't drift
     * from a second copy -- this selector just wires the reactive reads to it. Read inside a
     * memo/JSX for reactivity (it tracks the marks store, the live tail, and the window).
     */
    getRailData(agentId: string): ChatRailData {
      const marks = messageMarks.get(agentId)
      const messages = state.messagesByAgent[agentId] ?? []
      const windowFirstSeq = firstMessageSeq(messages)
      const windowLastSeq = lastMessageSeq(messages)
      const { minSeq, maxSeq } = resolveRailRange({
        seededMinSeq: marks.minSeq,
        seedMaxSeq: marks.seedMaxSeq,
        liveMaxSeq: liveTail.get(agentId),
        windowFirstSeq,
        windowLastSeq,
        hasOlderMessages: state.hasMoreOlder[agentId] ?? false,
      })
      return { loaded: marks.loaded, minSeq, maxSeq, marks: marks.marks, windowFirstSeq, windowLastSeq }
    },
    /**
     * Seed (or re-seed) an agent's scroll-rail marks from the worker. The public entry to
     * the seed-race machine, which lives in its own tested unit (createMessageMarkSeeder);
     * this just delegates. `watchSignal` ties the seed to the current WatchEvents
     * subscription -- see markSeeder.load for the full fire-and-forget / retry / fencing
     * contract. Kept as a method name here because the connection hook and the store tests
     * drive the seed through `store.loadMessageMarks`.
     */
    loadMessageMarks: markSeeder.load,
    /**
     * The loaded message at `seq`, or undefined when it isn't in the current window.
     * Used by the scroll rail's hover preview to extract a mark's preview WITHOUT a
     * fetch when the marked message is already loaded.
     *
     * Non-reactive, and `untrack` is what MAKES it so rather than asking callers to
     * be careful. The body walks `messagesByAgent`, so a caller inside a tracking
     * scope would subscribe to every message of that agent and re-run on each row
     * the agent appends. Both callers read imperatively -- the scroll rail's hover
     * preview from an event handler, an IMAGE tab's resolve from an `on()` effect --
     * but the second one reached here through an async function whose body runs
     * synchronously as far as its first `await`, which is exactly how a "call it
     * imperatively" contract gets broken without anybody writing a store read.
     */
    getLoadedMessageBySeq(agentId: string, seq: bigint): AgentChatMessage | undefined {
      return untrack(() => {
        const messages = state.messagesByAgent[agentId]
        if (!messages)
          return undefined
        // Rows are ascending by unique sequence, so binary-search the window
        // instead of a linear search. A marked message is usually outside the loaded window, so the
        // old scan traversed the whole ~1200-row window fruitlessly for every hovered/scrubbed dot.
        const end = transcriptMessageEnd(messages)
        const idx = lowerBoundBySeq(messages, seq, end)
        const hit = messages[idx]
        return hit?.seq === seq ? hit : undefined
      })
    },
    /**
     * Fetch a SINGLE message by its per-agent seq, for a reader that addresses a
     * message outside the loaded window. Two callers: the scroll rail's hover preview
     * of a mark, and an IMAGE tab resolving its `(agentId, seq, imageIndex)` reference
     * back to pixels.
     *
     * Resolves undefined ONLY for a definitive absence -- the agent has no message at
     * that seq (deleted/reseq'd since the reference was recorded), which the worker
     * returns as an unset message. A transient RPC failure RETHROWS (rather than
     * collapsing to undefined) so the caller can tell a real absence -- permanent,
     * stop -- from a blip it should retry, instead of poisoning that reference for
     * the rest of the session. Does NOT touch the loaded window; it is a read-only
     * lookup.
     */
    async fetchMessageBySeq(workerId: string, agentId: string, seq: bigint): Promise<AgentChatMessage | undefined> {
      try {
        const resp = await getAgentMessage(workerId, { agentId, seq })
        return resp.message
      }
      catch (err) {
        console.warn('failed to fetch message by seq', { agentId, seq, err })
        throw err
      }
    },
    /**
     * Persist an agent's viewport scroll for the NEXT mount of the same chat window (a
     * tile split/merge or workspace switch recreates ChatView over the still-live store
     * -- see ChatView.onSaveViewportScroll -> restoreOnMount), but ONLY while the agent's
     * chat window is still live. The unmount-save fires AFTER forgetAgent on an agent
     * close (handleAgentClose reaps the store, removes the tab, THEN the tile teardown
     * unmounts ChatView), so an unguarded set would resurrect a viewportScroll entry for
     * a dead agent -- the very per-agent leak forgetAgent exists to prevent.
     *
     * Gate on the SAME store's load-state (initialLoadComplete, which forgetAgent
     * clears) rather than a tab-liveness check. The two answer different questions:
     * a tab outlives the chat window it was read in, so "the tab still exists" does
     * not mean "this reader's position is still meaningful", and only forgetAgent
     * knows the window is gone. (The tab check was also wrong for a second reason
     * that no longer applies: tabs used to be scoped out of the active store on a
     * workspace switch, so it dropped the switch-away save entirely. The join now
     * spans every workspace and would answer -- the load-state gate is the right
     * question either way.)
     */
    saveViewportScrollForRemount(agentId: string, scroll: SavedViewportScroll) {
      if (state.initialLoadComplete[agentId])
        viewportScroll.set(agentId, scroll)
    },
  })
}
