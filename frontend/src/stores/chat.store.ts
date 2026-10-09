import type { ChatRailData } from './chatMessageMarks'
import type { ToolProgressEntry, ToolProgressUpdate } from './chatToolProgress'
import type { SavedViewportScroll } from './chatTypes'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { MessageSpanIdentity, ToolSpanSide } from '~/lib/messageSpan'
import { toBinary } from '@bufbuild/protobuf'
import { batch, untrack } from 'solid-js'
import { createStore, produce, unwrap } from 'solid-js/store'
import { forgetMarkPreview } from '~/components/chat/chatMarkPreview'
import { invalidateMessageClassificationCache } from '~/components/chat/messageClassifier'
import { CATCH_UP_GAP_LIMIT, MESSAGE_PAGE_LIMIT } from '~/generated/contracts/chat-history'
import { AgentChatMessageSchema, MarkType } from '~/generated/proto/leapmux/v1/agent_pb'
import { lowerBoundBySeq } from '~/lib/binarySearch'
import { invalidateMessageParseCache } from '~/lib/messageParser'
import { createBackgroundTaskStore } from './chatBackgroundTaskStore'
import { createContentVersionStore } from './chatContentVersions'
import { createGoalStore } from './chatGoalStore'
import { createHistoryPaginator, linkWatchSignal } from './chatHistoryPaginator'
import { createLiveTailTracker } from './chatLiveTail'
import { createMessageMarksStore, resolveRailRange } from './chatMessageMarks'
import { createMessageMarkSeeder } from './chatMessageMarkSeeder'
import { applyFreshMessage, firstMessageSeq, insertMessageBySeq, isReapablePhantom, lastMessageSeq, mergeWindow, preferNewerSupplement } from './chatMessageOrder'
import { createPerAgentStore } from './chatPerAgentStore'
import { createSpanIndex } from './chatSpanIndex'
import { createTodoStore } from './chatTodoStore'
import { createToolProgressStore } from './chatToolProgress'

/**
 * Limit the loaded messages in a visible agent window.
 * CATCH_UP_GAP_LIMIT supplies this limit and controls when reconciliation loads the latest page instead of draining a large gap.
 * The window removes older rows while following the live tail. See contracts/chat-history.json.
 * This value also sets the eight-times ceiling, so a catch-up limit change changes the maximum memory for each tab.
 */
export const MAX_LOADED_CHAT_MESSAGES = Number(CATCH_UP_GAP_LIMIT)
/**
 * Limit the loaded window while the reader scrolls above the live tail or the scroll hook fetches visible content.
 * loadOlderMessages and loadNewerPage use this ceiling instead of the base limit.
 * Hidden rows have zero scroll height, so three screens of visible content can require many more received rows.
 * Eight times the base limit accommodates stretches with approximately 90 percent hidden rows.
 * The Show hidden control exposes the remaining content after this ceiling applies.
 *
 * The base limit still controls a window that follows the live tail.
 * The larger window serves the reader above the tail.
 */
export const MAX_LOADED_CHAT_MESSAGES_CEILING = 8 * MAX_LOADED_CHAT_MESSAGES
/** Max number of loaded messages to keep for hidden/background agent tabs. */
export const MAX_BACKGROUND_CHAT_MESSAGES = 50

/**
 * Compare every serialized message field, including its content bytes.
 * An identical delivery requires no merge.
 * The protobuf equals helper checks bytes through instanceof Uint8Array.
 * That check can refuse equal arrays from different JavaScript execution environments, including jsdom and browser workers.
 * Binary serialization compares those arrays without that type check and includes future schema fields.
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
 * Keep the loaded window and its pagination state reactive.
 * Separate stores own to-dos and saved viewport positions.
 * This interface contains only the window and the flags that its rules require.
 */
export interface ChatStoreState {
  messagesByAgent: Record<string, AgentChatMessage[]>
  loading: boolean
  /** Whether there are older messages available to fetch (per agent). */
  hasMoreOlder: Record<string, boolean>
  /**
   * Record whether newer messages remain outside each agent window.
   * Trimming the newest end sets this flag.
   * loadNewerPage clears it only when the response reports no newer history and the window reaches the recorded live tail.
   * A has_more=false response alone cannot establish that condition.
   * Other window operations set it for their selected range or temporary fill state.
   */
  hasMoreNewer: Record<string, boolean>
  /**
   * Record a reachable gap after forwardFillToLiveTail reaches its maximum attempt count.
   * The fill still advances, so hasMoreNewer stays true and the recorded tail remains unchanged.
   * Continuous tail reconciliation resumes this fill without a user action or reconnect.
   * A history window that the user selects has no such deferral.
   * beginHistoryFetch clears the deferral when a new user fetch supersedes the fill, including after the user scrolls upward.
   */
  tailFillDeferred: Record<string, boolean>
  /**
   * Record an active WatchEvents replay for each agent.
   * Subscription sets this flag and CatchUpComplete clears it.
   * CatchUpStart normally supplies the authoritative tail. An absent tail leaves liveTail dependent on the received messages.
   * The append guard therefore checks sequence contiguity while this flag is true.
   * When hasMoreNewer is false, this replay check refuses a skipped sequence and accepts the next contiguous frame.
   * See beyondUnloadedNewerTail.
   */
  catchingUp: Record<string, boolean>
  /** Whether a fetch for older messages is in progress (per agent). */
  fetchingOlder: Record<string, boolean>
  /** Whether a fetch for newer messages (or a jump-to-latest) is in progress. */
  fetchingNewer: Record<string, boolean>
  /** Whether initial load has completed for an agent. */
  initialLoadComplete: Record<string, boolean>
  /**
   * Increase the counter when addMessage changes the loaded window, including a notification update.
   * An identical or discarded duplicate leaves the counter unchanged.
   */
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

  // Each composed store owns one concern.
  // The window uses those stores only for shared window changes.
  const bumpMessageVersion = (agentId: string) => setState('messageVersion', agentId, (prev = 0) => prev + 1)
  const messageObservers = new Map<string, Set<(message: AgentChatMessage) => void>>()
  const notifyMessageObservers = (agentId: string, message: AgentChatMessage) => {
    for (const observer of messageObservers.get(agentId) ?? []) {
      try {
        observer(message)
      }
      catch (error) {
        console.warn('Message context observer failed', { agentId, error })
      }
    }
  }
  // A tool-progress update changes a badge in an existing header without increasing the message version.
  // A version change would wake automatic scrolling and the classified-entry cache.
  // Replacing the row could then discard the user's text selection.
  const toolProgress = createToolProgressStore()
  const todos = createTodoStore()
  const backgroundTasks = createBackgroundTaskStore()
  const goal = createGoalStore()
  // Retain the viewport position for each agent across tab switches.
  // The value needs these methods:
  // - get.
  // - set.
  // - clear.
  // It has no additional domain logic, so use createPerAgentStore directly.
  const viewportScroll = createPerAgentStore<SavedViewportScroll | undefined>(undefined)
  // Track the highest observed server sequence, including messages outside the loaded window.
  // createLiveTailTracker owns its increase, reconciliation, and removal rules.
  const liveTail = createLiveTailTracker()
  // Retain scroll-rail marks for notable messages and the complete history range.
  // ListMessageMarks supplies the initial values. Live additions and removals update them.
  // Record a mark even when its message stays outside the loaded window.
  const messageMarks = createMessageMarksStore()
  // createMessageMarkSeeder reconciles ListMessageMarks responses with live mark changes.
  // It checks epochs and controls immediate and delayed retries, each with a maximum count.
  // loadMessageMarks delegates to its load method. forgetAgent calls its forget method.
  // The separate messageMarks store owns the mark data.
  const markSeeder = createMessageMarkSeeder({ marks: messageMarks })

  /**
   * createSpanIndex links each tool span's request and result through their span identity.
   * It also uses the shared parse cache.
   * reindexSpans keeps that non-reactive index consistent with the loaded window.
   */
  const spanIdx = createSpanIndex()
  /**
   * Retain one controller for each agent's active history fetch.
   * A new fetch aborts the previous controller so a delayed request cannot prevent further pagination.
   * The remote procedure call can still finish. Its caller checks signal.aborted after awaiting the result and discards a superseded response.
   */
  const fetchAbort = new Map<string, AbortController>()
  const fetchWatchCleanup = new Map<string, () => void>()

  /**
   * Retain a separate controller for the background catchUpToTail loop.
   * beginHistoryFetch aborts that loop when a user fetch takes control of the window.
   * A background loop must not abort an unrelated initial or user fetch, so it does not change fetchAbort.
   */
  const catchUpAbort = new Map<string, AbortController>()

  /**
   * Start a new history fetch for agentId.
   * Abort the previous history fetch and background catch-up loop.
   * Install a new controller and reset both fetch-direction flags. The caller then sets its own flag.
   * Return the new signal. The caller must discard a result after that signal aborts.
   * runHistoryFetch clears the flag only while its controller still owns the fetch.
   *
   * An optional watchSignal ties this fetch to the current WatchEvents subscription.
   * A workspace switch or worker change can then abort the reconciliation request for the latest page.
   * A user fetch omits watchSignal because its active tab already supplies its scope.
   */
  function beginHistoryFetch(agentId: string, watchSignal?: AbortSignal): AbortSignal {
    fetchAbort.get(agentId)?.abort()
    fetchWatchCleanup.get(agentId)?.()
    fetchWatchCleanup.delete(agentId)
    // A user fetch supersedes background catch-up.
    // Abort its pending request before the user's jump or scroll changes the window.
    catchUpAbort.get(agentId)?.abort()
    const controller = new AbortController()
    fetchAbort.set(agentId, controller)
    fetchWatchCleanup.set(agentId, linkWatchSignal(controller, watchSignal))
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
      // Clear the flag only while this controller still owns the fetch.
      // A newer fetch installs another controller and resets both flags, so this request must leave those newer flags unchanged.
      // An aborted watch signal can end a fetch without starting another fetch.
      // This controller still owns the flag in that case, so clear it.
      // Otherwise, fetchingNewer could remain true and prevent all newer pagination until another user fetch resets it.
      if (fetchAbort.get(agentId)?.signal === signal) {
        setState(flag, agentId, false)
        fetchWatchCleanup.get(agentId)?.()
        fetchWatchCleanup.delete(agentId)
      }
    }
  }
  /**
   * Rebuild the span index from the current loaded window.
   * Rebuild after each change that removes or reorders rows:
   * - A trim.
   * - A prepend.
   * - A window replacement.
   * Otherwise, the index could retain removed rows and grow beyond the window's memory limit.
   * createSpanIndex uses message classification so a fetched request cannot occupy the result position.
   */
  function reindexSpans(agentId: string) {
    spanIdx.reindex(agentId, state.messagesByAgent[agentId] ?? [])
  }

  // chatContentVersions tracks same-sequence content changes that preserve the store proxy.
  // A sequence-based cache cannot detect that change through the sequence alone.
  // The classified-entry cache and height estimate read the content version reactively.
  // An increased version makes both readers calculate their values from the new content.
  const contentVersions = createContentVersionStore()

  /** Remove content versions for rows that left the window. */
  function forgetContentVersions(droppedIds: Iterable<string>) {
    const ids = [...droppedIds]
    contentVersions.forget(ids)
  }

  /** Remove content versions for rows that left the window. */
  function reclaimDroppedRows(prev: AgentChatMessage[], kept: AgentChatMessage[]) {
    const keptIds = new Set(kept.map(m => m.id))
    forgetContentVersions(prev.filter(m => !keptIds.has(m.id)).map(m => m.id))
  }

  /**
   * Remove all chat state when the agent closes.
   * Window trims remove rows, but they do not remove the complete agent entry.
   * Without this cleanup, repeated agent opens and closes would retain state in every composed store.
   * Remove these entries together:
   * - The loaded messages and pagination flags.
   * - The live tail and span index.
   * - Each composed store's agent value.
   * useAgentOperations.handleAgentClose supplies the separate control and attachment cleanup.
   */
  function forgetAgent(agentId: string) {
    messageObservers.delete(agentId)
    // Abort history fetches and background catch-up, then remove their controllers.
    // A delayed response must not change the removed window.
    fetchAbort.get(agentId)?.abort()
    fetchAbort.delete(agentId)
    fetchWatchCleanup.get(agentId)?.()
    fetchWatchCleanup.delete(agentId)
    catchUpAbort.get(agentId)?.abort()
    catchUpAbort.delete(agentId)
    // Remove the agent's row state:
    // - Content versions.
    // - Tool progress.
    // - Span indexes.
    const rows = state.messagesByAgent[agentId] ?? []
    forgetContentVersions(rows.map(m => m.id))
    toolProgress.clearAgent(agentId)
    spanIdx.reindex(agentId, [])
    // Delete every agent key from the window records.
    // An empty value would still retain an entry after the agent closes.
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
    // The hover-preview cache survives scroll-rail remounts because it belongs to the module.
    // Remove the agent's entry explicitly so it cannot survive a close and reopen of that agent.
    // See chatMarkPreview.forgetMarkPreview.
    forgetMarkPreview(agentId)
    todos.remove(agentId)
    backgroundTasks.remove(agentId)
    goal.remove(agentId)
    viewportScroll.remove(agentId)
  }

  /**
   * Remove loaded phantom rows above latestSeq, except rows above a supplied reapCeilingSeq.
   * Those exempt rows can arrive live during replay.
   * Reclaim each removed row's state and rebuild the span index, as a trim or delete does.
   * Then compare the surviving window tail with latestSeq to set hasMoreNewer.
   * This helper applies the row removal that reconcileAuthoritativeTail selects.
   * Return without changes when no loaded row qualifies.
   */
  function reapPhantomRows(agentId: string, latestSeq: bigint, reapCeilingSeq?: bigint) {
    const prev = state.messagesByAgent[agentId]
    if (!prev || prev.length === 0)
      return
    const survivors = prev.filter(m => !isReapablePhantom(m.seq, latestSeq, reapCeilingSeq))
    if (survivors.length === prev.length)
      return // No loaded row qualifies for removal.
    // Remove each deleted row's scroll-rail mark also.
    // A stale mark above the new maximum sequence could otherwise survive a concurrent marks response.
    // A later append would then display a mark for a deleted row.
    // The mark store increases its revision only after an actual removal.
    for (const m of prev) {
      if (isReapablePhantom(m.seq, latestSeq, reapCeilingSeq))
        messageMarks.remove(agentId, m.seq)
    }
    reclaimDroppedRows(prev, survivors)
    spanIdx.reindex(agentId, survivors)
    setState('messagesByAgent', agentId, survivors)
    // The window retains rows at or below latestSeq and any live rows above reapCeilingSeq.
    // A surviving live row can therefore exceed latestSeq.
    // Set hasMoreNewer only when the surviving window tail remains below the authoritative tail.
    setState('hasMoreNewer', agentId, (lastMessageSeq(survivors) ?? 0n) < latestSeq)
  }

  /**
   * Reconcile the loaded window with the tail that CatchUpStart or CatchUpComplete reports.
   * Remove rows above latestSeq, except rows above a supplied reapCeilingSeq.
   * Reconcile the recorded live tail with the same exemption so the newer-message control cannot point past deleted history.
   * An absent latestSeq means that the worker could not read the tail. Do not remove rows against that absent value.
   *
   * The receipt cursor supplies reapCeilingSeq at CatchUpStart.
   * At CatchUpComplete, the dispatcher uses start_tail_seq or falls back to that receipt cursor.
   * A live frame can arrive before either baseline because the worker registers interest before replay.
   * Only rows in the interval (latestSeq, reapCeilingSeq] qualify for removal when a ceiling exists.
   * An omitted ceiling supplies no exemption.
   *
   * probeIndeterminate applies only after replay completes with an absent authoritative tail.
   * Increase the recorded tail to one sequence beyond a nonempty window so continuous reconciliation requests another page.
   * catchUpToTail then reads the actual tail. settleToWindow removes that increase when the server supplies no newer row.
   * CatchUpStart does not request this probe because it would race the active replay.
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
   * Update the loaded message that matches this ID.
   * A same-sequence update uses the indexed setter and preserves the store proxy.
   * A new sequence removes the old row and inserts the new row in sequence order.
   */
  function updateExistingMessage(agentId: string, prev: AgentChatMessage[], existingIdx: number, message: AgentChatMessage): boolean {
    const existing = prev[existingIdx]
    if (existing === undefined)
      return false
    if (existing.seq === message.seq) {
      const proxy = existing
      if (preferNewerSupplement(proxy, message) === proxy)
        return false
      // An identical delivery needs no merge.
      // Skip its state write and cache invalidation. The caller then skips the version increase and span-index rebuild also.
      // Those operations would recalculate unchanged content and wake automatic scrolling.
      // sameAgentMessage compares every serialized field, including content bytes. A changed body must still update.
      // unwrap removes the Solid proxy before serialization reads the fields.
      if (sameAgentMessage(unwrap(proxy), message))
        return false
      setState('messagesByAgent', agentId, existingIdx, message)
      // A same-sequence merge preserves the proxy, so caches that use its reference can retain old content.
      // Invalidate the parse and classification caches for that proxy.
      // Increase its content version so the classified-entry cache and height estimate read the new content.
      invalidateRenderedMessage(proxy)
      return true
    }
    const without = prev.filter((_, i) => i !== existingIdx)
    setState('messagesByAgent', agentId, insertMessageBySeq(without, message))
    return true
  }

  function invalidateRenderedMessage(message: AgentChatMessage): void {
    invalidateMessageParseCache(message)
    invalidateMessageClassificationCache(message)
    contentVersions.bump(message.id)
  }

  function invalidateNewSupplements(previous: AgentChatMessage[], next: AgentChatMessage[]): void {
    const previousById = new Map(previous.map(message => [message.id, message]))
    for (const message of next) {
      const old = previousById.get(message.id)
      if (old && old.seq === message.seq && message.supplementalRevision > old.supplementalRevision)
        invalidateRenderedMessage(old)
    }
  }

  /**
   * A notification consolidation can move an existing row beyond the loaded tail.
   * Inserting that new sequence across unloaded history would create a gap and advance the forward-paging cursor past it.
   * The window could then incorrectly appear caught up.
   * Remove the row from its old position instead.
   * The caller records the new sequence in liveTail.
   * loadNewerPage or jumpToLatestMessages later reads the missing range in sequence order.
   */
  function handleReseqMovedBeyondWindow(agentId: string, prev: AgentChatMessage[], existingIdx: number) {
    const dropped = prev[existingIdx]
    if (dropped === undefined)
      return
    setState('messagesByAgent', agentId, prev.filter((_, i) => i !== existingIdx))
    // Reclaim the content version when the row leaves the window. A notification
    // can receive an in-place update before it moves to a new sequence.
    forgetContentVersions([dropped.id])
  }

  /** Return the window when it holds more than `maxCount` messages, else null. */
  function windowOverMessageCap(agentId: string, maxCount: number): AgentChatMessage[] | null {
    const prev = state.messagesByAgent[agentId]
    if (!prev || prev.length <= maxCount)
      return null
    return prev
  }

  /**
   * Install the retained rows after a trim.
   * Set the flag for the unloaded history on that side and rebuild the span index.
   */
  function commitTrim(
    agentId: string,
    survivors: AgentChatMessage[],
    hasMoreField: 'hasMoreOlder' | 'hasMoreNewer',
  ) {
    setState('messagesByAgent', agentId, survivors)
    setState(hasMoreField, agentId, true)
    reindexSpans(agentId)
  }

  /**
   * Merge a fetched page into the window and remove duplicate sequences.
   * Rebuild its span index.
   * An older page prepends history. A newer page inserts in sequence order and increases the recorded live tail.
   */
  function mergeFetchedMessages(agentId: string, fetched: AgentChatMessage[], side: 'older' | 'newer') {
    if (fetched.length === 0)
      return
    return batch(() => {
      // Snapshot the previous window so the merge can reclaim dropped row state.
      const prevWindow = state.messagesByAgent[agentId] ?? []
      // chatMessageOrder.mergeWindow owns the pure ordering rules for plain arrays.
      // This store applies the reactive effects after that merge.
      const next = mergeWindow(prevWindow, fetched, side)
      invalidateNewSupplements(prevWindow, next)
      setState('messagesByAgent', agentId, next)
      // Rebuild the span index from the entire merged window in sequence order.
      // Indexing only the older page could assign a prepended request to the wrong position when its result already exists.
      reindexSpans(agentId)
      const merged = state.messagesByAgent[agentId] ?? []
      for (const message of fetched)
        notifyMessageObservers(agentId, message)
      // Reclaim content versions for rows that left the window.
      reclaimDroppedRows(prevWindow, merged)
      if (side === 'newer') {
        for (const msg of fetched)
          liveTail.bump(agentId, msg.seq)
      }
    })
  }

  /**
   * Supply the shared window replacement for setMessages and loadInitialMessages.
   */
  function applyMessages(agentId: string, messages: AgentChatMessage[], hasMore: boolean) {
    return batch(() => {
      const prevRows = state.messagesByAgent[agentId] ?? []
      const previousById = new Map(prevRows.map(message => [message.id, message]))
      const finalMessages = messages.map((message) => {
        const previous = previousById.get(message.id)
        return previous ? preferNewerSupplement(previous, message) : message
      })
      invalidateNewSupplements(prevRows, finalMessages)
      // Reclaim content versions for rows that leave the window.
      reclaimDroppedRows(prevRows, finalMessages)
      // Rebuild the span index before the update wakes reactive computations.
      spanIdx.reindex(agentId, finalMessages)
      setState('messagesByAgent', agentId, finalMessages)
      for (const message of finalMessages)
        notifyMessageObservers(agentId, message)
      setState('hasMoreOlder', agentId, hasMore)
      // Default the replacement to the latest page, with no newer history outside the window.
      // Initial load and a jump to the latest page use that default.
      // jumpToOldestMessages immediately replaces hasMoreNewer with its response because it selects an older window.
      setState('hasMoreNewer', agentId, false)
      setState('initialLoadComplete', agentId, true)
      for (const msg of messages) {
        liveTail.bump(agentId, msg.seq)
      }
    })
  }

  const baseStore = {
    state,

    subscribeMessages(agentId: string, observer: (message: AgentChatMessage) => void): () => void {
      let observers = messageObservers.get(agentId)
      if (!observers) {
        observers = new Set()
        messageObservers.set(agentId, observers)
      }
      observers.add(observer)
      return () => {
        observers.delete(observer)
        if (observers.size === 0 && messageObservers.get(agentId) === observers)
          messageObservers.delete(agentId)
      }
    },

    getSpanMessage(agentId: string, identity: MessageSpanIdentity, side: ToolSpanSide): AgentChatMessage | undefined {
      void state.messageVersion[agentId]
      return side === 'request' ? spanIdx.getRequestMessage(agentId, identity) : spanIdx.getResultMessage(agentId, identity)
    },

    getMessages(agentId: string): AgentChatMessage[] {
      return state.messagesByAgent[agentId] ?? []
    },

    setMessages(agentId: string, messages: AgentChatMessage[], hasMore = false) {
      applyMessages(agentId, messages, hasMore)
    },

    /**
     * Decide whether seq would cross unloaded newer history beyond the current window.
     * Return false for an empty window or a sequence at or below its loaded tail.
     * Then apply these conditions in order:
     * - hasMoreNewer requires refusal beyond that loaded tail.
     * - Otherwise, during replay, seq > lastSeq + 1 requires refusal.
     * - Otherwise, require both lastSeq < recordedLiveTail and seq > recordedLiveTail for refusal.
     * When hasMoreNewer is false, replay accepts the next contiguous frame even when the worker supplies no authoritative tail.
     * After replay, use the recorded tail instead of contiguity so a deleted sequence does not require an unnecessary fetch.
     * recordedLiveTail identifies the observed tail before this message increases it.
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
     * Refuse a new message outside the loaded window when unloaded history separates it from that window.
     * Check both sides:
     * - beyondUnloadedNewerTail checks a newer message against the current replay or recorded tail.
     * - hasMoreOlder and message.seq < firstSeq identify a message before unloaded older history.
     * Accept a missing sequence within the loaded range. An empty window has no gap to protect.
     * liveTail still records a refused message's sequence. Paging or a jump can later read its range contiguously.
     * Pass recordedLiveTail from before this message increases the tail.
     */
    shouldDropBeyondWindow(agentId: string, message: AgentChatMessage, recordedLiveTail: bigint): boolean {
      const firstSeq = this.getFirstSeq(agentId)
      const beyondTail = this.beyondUnloadedNewerTail(agentId, message.seq, recordedLiveTail)
      const beforeHead = !!state.hasMoreOlder[agentId] && firstSeq !== 0n && message.seq < firstSeq
      return beyondTail || beforeHead
    },

    /**
     * Identify an existing row that moves across an unloaded newer gap.
     * previousSeq > 0 identifies the worker's explicit move. beyondUnloadedNewerTail checks the new position.
     * The caller removes the old row instead of advancing the loaded tail past missing history.
     */
    isReseqMovedBeyondWindow(agentId: string, message: AgentChatMessage, recordedLiveTail: bigint): boolean {
      return message.previousSeq > 0n
        && this.beyondUnloadedNewerTail(agentId, message.seq, recordedLiveTail)
    },

    /**
     * Process a message whose ID already exists at existingIdx.
     * previousSeq identifies a notification consolidation that moves the row to a new sequence.
     * If that sequence crosses an unloaded newer gap, remove the old row through handleReseqMovedBeyondWindow.
     * addMessage already records the new sequence in liveTail.
     * Otherwise, update the row in place or insert it at its new sequence through updateExistingMessage.
     *
     * Return whether the row remains loaded and whether the window changes.
     * An identical same-sequence delivery leaves changed false.
     */
    applyExistingMessage(agentId: string, messages: AgentChatMessage[], existingIdx: number, message: AgentChatMessage, recordedLiveTail: bigint): { inWindow: boolean, changed: boolean } {
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
      // Rebuild the span index after a changed row updates or leaves the window.
      // A content or sequence change can invalidate the index. An unchanged identical delivery requires no rebuild.
      if (changed)
        reindexSpans(agentId)
      return { inWindow, changed }
    },

    /**
     * Process a message whose ID does not exist in the window.
     * applyFreshMessage inserts it in sequence order or refuses a duplicate sequence under a different ID.
     * It returns the same array for an unchanged duplicate, so changed remains false.
     * A real window change returns a new array.
     * Return whether the message enters the window and whether the window changes.
     */
    insertFreshMessage(agentId: string, messages: AgentChatMessage[], message: AgentChatMessage): { inWindow: boolean, changed: boolean } {
      const { next, inserted } = applyFreshMessage(messages, message)
      const changed = next !== messages
      setState('messagesByAgent', agentId, next)
      // Index only an inserted message. A discarded duplicate has no loaded row for its span entry.
      // The incremental index requests a full rebuild if a span position would move to another message ID.
      if (inserted && spanIdx.index(agentId, message))
        reindexSpans(agentId)
      return { inWindow: inserted, changed }
    },

    addMessage(agentId: string, message: AgentChatMessage): boolean {
      return batch(() => {
        notifyMessageObservers(agentId, message)
        // Read the recorded live tail before this message increases it.
        // beyondUnloadedNewerTail compares a live arrival against that earlier tail.
        const recordedLiveTail = liveTail.get(agentId)
        // Record the live sequence even when the loaded window refuses the message.
        // jumpToLatestMessages still needs that recorded tail.
        liveTail.bump(agentId, message.seq)

        // Record the scroll-rail mark before the window can refuse the message.
        // The worker supplies markType in the protobuf message.
        // A previousSeq move removes the old mark before recording the new one.
        // Otherwise, the old sequence could retain a mark for a moved row.
        // The mark store increases its revision only for a real change.
        // An unmarked move or repeated identical mark therefore preserves a concurrent loadMessageMarks request.
        if (message.previousSeq !== 0n)
          messageMarks.remove(agentId, message.previousSeq)
        if (message.markType !== MarkType.UNSPECIFIED)
          messageMarks.noteMark(agentId, message.seq, message.markType)

        // Look for a loaded row with this ID before processing the message.
        // A LEAPMUX notification can update or move its existing row during consolidation.
        const messages = state.messagesByAgent[agentId] ?? []
        const existingIdx = messages.findLastIndex(m => m.id === message.id)

        // Refuse a new row that would cross unloaded history.
        // An existing row uses its separate update path instead. See shouldDropBeyondWindow.
        if (existingIdx === -1 && this.shouldDropBeyondWindow(agentId, message, recordedLiveTail))
          return false

        // Select the existing-row or new-row method.
        // inWindow reports whether message.id remains loaded. changed reports an actual window change.
        // An unchanged duplicate requires no message-version increase or automatic-scroll update.
        const { inWindow, changed } = existingIdx !== -1
          ? this.applyExistingMessage(agentId, messages, existingIdx, message, recordedLiveTail)
          : this.insertFreshMessage(agentId, messages, message)

        if (changed)
          bumpMessageVersion(agentId)
        return inWindow
      })
    },

    getLastSeq(agentId: string): bigint {
      // The store uses 0n as the empty-window sentinel.
      return lastMessageSeq(state.messagesByAgent[agentId] ?? []) ?? 0n
    },

    /**
     * Return the row's content version, initially zero. See chatContentVersions.
     * A same-sequence content replacement increases it while retaining the row's ID and proxy.
     * The classified-entry cache and height estimate read this value reactively to detect that replacement.
     */
    getMessageContentVersion(id: string): number {
      return contentVersions.get(id)
    },

    /** Merge one `running_tool` broadcast into its span's live progress. */
    applyToolProgress(agentId: string, update: ToolProgressUpdate) {
      toolProgress.apply(agentId, update)
    },

    /**
     * Return the span's live tool progress, or undefined when no progress exists.
     * ToolRunningBadge reads it through its row context and subscribes independently.
     */
    getToolProgress(agentId: string, identity: MessageSpanIdentity): ToolProgressEntry | undefined {
      return toolProgress.get(agentId, identity)
    },

    /**
     * Remove the span's progress after its result row arrives.
     */
    dropToolProgress(agentId: string, identity: MessageSpanIdentity) {
      toolProgress.drop(agentId, identity)
    },

    /**
     * Remove every live tool-progress entry for the agent.
     * A lifecycle event or lost connection can omit a tool's result row.
     * The frontend then removes its remaining badges.
     */
    clearToolProgress(agentId: string) {
      toolProgress.clearAgent(agentId)
    },

    setLoading(loading: boolean) {
      setState('loading', loading)
    },

    /**
     * Cap the window after prepending older history. Keep the oldest
     * `maxCount` messages and flag newer history.
     */
    trimNewestEnd(agentId: string, maxCount: number) {
      const prev = windowOverMessageCap(agentId, maxCount)
      if (!prev)
        return
      const survivors = prev.slice(0, maxCount)
      const dropped = prev.slice(maxCount)
      // Reclaim the content versions of the dropped rows.
      forgetContentVersions(dropped.map(m => m.id))
      commitTrim(agentId, survivors, 'hasMoreNewer')
    },

    /**
     * Cap the window after appending newer history. Keep the newest
     * `maxCount` messages and flag older history.
     */
    trimOldestEnd(agentId: string, maxCount: number) {
      const prev = windowOverMessageCap(agentId, maxCount)
      if (!prev)
        return
      // The slice start is positive because the window length exceeds maxCount.
      const survivors = prev.slice(prev.length - maxCount)
      const droppedOldest = prev.slice(0, prev.length - maxCount)
      // Reclaim the content versions of the dropped oldest rows.
      forgetContentVersions(droppedOldest.map(m => m.id))
      commitTrim(agentId, survivors, 'hasMoreOlder')
    },

    /**
     * Trim the oldest rows while protecting the reader's current viewport.
     * minKeepNewest counts the rows from the viewport's top anchor through the tail.
     * The scroll hook calculates it so a normal trim preserves visible rows.
     * Use these limits:
     * - At the tail, zero selects MAX_LOADED_CHAT_MESSAGES.
     * - Above the tail, a larger count protects the viewport.
     * - MAX_LOADED_CHAT_MESSAGES_CEILING remains the maximum even when the viewport needs more rows.
     * At that maximum, the trim can remove visible oldest rows and move the reader's position.
     */
    trimOldestToViewport(agentId: string, minKeepNewest: number) {
      const target = Math.min(
        MAX_LOADED_CHAT_MESSAGES_CEILING,
        Math.max(MAX_LOADED_CHAT_MESSAGES, minKeepNewest),
      )
      this.trimOldestEnd(agentId, target)
    },

    /**
     * Compare the window tail with the highest observed live sequence, including refused window messages.
     * A has_more=false response does not prove that a broadcast during the fetch also entered the window.
     * Forward paging therefore checks this value before reporting the live tail.
     * The default zero means that no live sequence exists and any non-negative window tail satisfies it.
     */
    caughtUpToLiveTail(agentId: string): boolean {
      return liveTail.caughtUp(agentId, this.getLastSeq(agentId))
    },

    /**
     * Resume WatchEvents after the highest sequence that the client observes.
     * Include live messages that the loaded window refuses.
     * While the reader stays away from the tail, getLastSeq can remain below liveTail.
     * Resuming from that window position would repeat a page that the append guard refuses again.
     * Resume after the recorded live tail instead.
     * loadNewerPage or jumpToLatestMessages later reads the skipped range contiguously when the reader returns.
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
     * Report a reachable deferred gap after forward fill reaches its maximum attempt count.
     * Continuous reconciliation resumes that fill. It preserves a history window that the user selects without this flag.
     * See tailFillDeferred and resumeDeferredTailFill.
     */
    isTailFillDeferred(agentId: string): boolean {
      return state.tailFillDeferred[agentId] ?? false
    },

    /**
     * Record whether the WatchEvents replay remains active.
     * Subscription sets true and CatchUpComplete sets false.
     * During replay, the append guard uses sequence contiguity instead of the recorded-tail comparison.
     * A frame beyond an unloaded gap stays outside the window and remains available through later paging.
     * This check works even when the worker cannot report its tail.
     */
    setCatchingUp(agentId: string, value: boolean) {
      setState('catchingUp', agentId, value)
    },

    /**
     * Stop visible-buffer fetches when the window comes within one page of its maximum row count.
     * A further page could force a trim on the opposite side and remove the visible buffer or live tail.
     * Use MAX_LOADED_CHAT_MESSAGES_CEILING - MESSAGE_PAGE_LIMIT as the threshold.
     * Stopping at the ceiling itself could permit a final page to exceed it before trimNewestEnd sets hasMoreNewer.
     * The one-page margin prevents that final fetch from removing the live tail.
     */
    atWindowCeiling(agentId: string): boolean {
      const msgs = state.messagesByAgent[agentId]
      return !!msgs && msgs.length >= MAX_LOADED_CHAT_MESSAGES_CEILING - MESSAGE_PAGE_LIMIT
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

  // Create the paginator after baseStore exists and supply its dependencies explicitly.
  // Wrap each store method so baseStore remains its receiver for calls through this.
  // The paginator supplies closures and adds no receiver dependency of its own.
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
    replaceGoal: goal.replace,
    markBackgroundTasksLoadFailed: backgroundTasks.markLoadFailed,
  })

  // Expose each composed store directly, including liveTail for recorded-tail reads.
  // Consumers call that store's canonical methods without forwarding aliases.
  // The window core still owns message changes and window rules. It also supplies annotations.
  return Object.assign(baseStore, paginator, {
    forgetAgent,
    reconcileAuthoritativeTail,
    liveTail,
    messageMarks,
    todos,
    backgroundTasks,
    goal,
    viewportScroll,
    /**
     * Return reactive scroll-rail data for this agent:
     * - The marked sequences.
     * - The complete history range for the current window.
     * - The loaded window's first and last sequences.
     * resolveRailRange owns the pure range rule. This selector supplies its reactive inputs.
     * Read it inside a memo or JSX to track every supplied input.
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
     * Load the agent's scroll-rail marks through createMessageMarkSeeder.
     * watchSignal ties that request to the current WatchEvents subscription.
     * markSeeder.load owns cancellation and retry rules.
     * The connection hook and store tests use this canonical loadMessageMarks entry.
     */
    loadMessageMarks: markSeeder.load,
    /**
     * Return the loaded message at seq, or undefined when it stays outside the window.
     * The scroll-rail hover preview can extract it without a fetch.
     * untrack prevents this lookup from adding message subscriptions to a reactive caller.
     * The image-tab resolver can reach it synchronously before its first await, so an imperative-call convention alone is insufficient.
     */
    getLoadedMessageBySeq(agentId: string, seq: bigint): AgentChatMessage | undefined {
      return untrack(() => {
        const messages = state.messagesByAgent[agentId]
        if (!messages)
          return undefined
        // Binary-search the window because its rows have unique ascending sequences.
        // A missing mark message requires no scan through every loaded row.
        const idx = lowerBoundBySeq(messages, seq)
        const hit = messages[idx]
        return hit?.seq === seq ? hit : undefined
      })
    },
    /**
     * Retain the viewport position for the next mount of this agent's live chat window.
     * A tile change or workspace switch can recreate ChatView over that same store.
     * Agent close calls forgetAgent before ChatView saves its position during unmount.
     * An unconditional save could recreate state for the removed agent.
     * Require this store's initialLoadComplete flag, which forgetAgent removes.
     * A tab can outlive the particular chat window that supplied the position, so tab existence does not establish this condition.
     */
    saveViewportScrollForRemount(agentId: string, scroll: SavedViewportScroll) {
      if (state.initialLoadComplete[agentId])
        viewportScroll.set(agentId, scroll)
    },
  })
}
