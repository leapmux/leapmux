import type { ChatStoreState } from './chat.store'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { createStore } from 'solid-js/store'
import { beforeEach, describe, expect, it, vi } from 'vitest'

// listAgentMessages is the only external dependency; hoist the mock so the factory
// can reference it (vi.mock is hoisted above imports).
const { listAgentMessages } = vi.hoisted(() => ({ listAgentMessages: vi.fn() }))
vi.mock('~/api/workerRpc', () => ({ listAgentMessages }))

const { createHistoryPaginator, linkWatchSignal, MESSAGE_PAGE_SIZE } = await import('./chatHistoryPaginator')
const { MessagePageAnchor, MessageSource } = await import('~/generated/proto/leapmux/v1/agent_pb')

// The harness's cap/ceiling, distinct so an assertion can prove WHICH was used. The
// production wiring passes MAX_LOADED_CHAT_MESSAGES / MAX_LOADED_CHAT_MESSAGES_CEILING.
const BASE = 150
const CEILING = 1200

function makeMsg(seq: bigint, id?: string): AgentChatMessage {
  return { seq, id: id ?? `m${seq}`, source: MessageSource.AGENT } as AgentChatMessage
}

function page(messages: AgentChatMessage[], hasMore: boolean) {
  return { messages, hasMore, todos: [], todosLoaded: false }
}

function harness(init: {
  messages: AgentChatMessage[]
  hasMoreOlder?: boolean
  hasMoreNewer?: boolean
  caughtUp?: () => boolean
  liveGet?: () => bigint
}) {
  const [state, setState] = createStore<ChatStoreState>({
    messagesByAgent: { a: init.messages },
    loading: false,
    hasMoreOlder: { a: init.hasMoreOlder ?? true },
    hasMoreNewer: { a: init.hasMoreNewer ?? false },
    tailFillDeferred: {},
    catchingUp: {},
    fetchingOlder: {},
    fetchingNewer: {},
    initialLoadComplete: {},
    messageVersion: {},
  })

  const trimNewestEnd = vi.fn()
  const trimOldestEnd = vi.fn()
  const addMessage = vi.fn((agentId: string, msg: AgentChatMessage) => {
    setState('messagesByAgent', agentId, prev => [...(prev ?? []), msg])
  })
  const mergeFetchedMessages = vi.fn(
    (agentId: string, fetched: AgentChatMessage[], side: 'older' | 'newer') => {
      setState('messagesByAgent', agentId, prev =>
        side === 'older' ? [...fetched, ...(prev ?? [])] : [...(prev ?? []), ...fetched])
    },
  )

  const serverRows = (agentId: string) =>
    (state.messagesByAgent[agentId] ?? []).filter(m => m.seq !== 0n)
  const firstServer = (agentId: string) => serverRows(agentId)[0]?.seq
  const lastServer = (agentId: string) => serverRows(agentId).at(-1)?.seq

  const settleToWindow = vi.fn()
  const resetToEmptyIfStale = vi.fn()
  const applyMessages = vi.fn()
  const replaceBackgroundTasks = vi.fn()
  const markBackgroundTasksLoadFailed = vi.fn()
  const replaceGoal = vi.fn()

  const paginator = createHistoryPaginator({
    state,
    setState,
    catchUpAbort: new Map(),
    // Mirror the store's beginHistoryFetch: a fresh controller per fetch, linked to
    // the watch signal so a reconcile-driven fetch aborts on a workspace switch.
    runHistoryFetch: async (agentId, flag, body, watchSignal) => {
      const controller = new AbortController()
      const cleanupWatchSignal = linkWatchSignal(controller, watchSignal)
      setState(flag, agentId, true)
      try {
        await body(controller.signal)
      }
      finally {
        cleanupWatchSignal()
        if (!controller.signal.aborted)
          setState(flag, agentId, false)
      }
    },
    mergeFetchedMessages,
    applyMessages,
    liveTail: {
      get: init.liveGet ?? (() => 0n),
      bump: vi.fn(),
      caughtUp: () => true,
      settleToWindow,
      resetToEmptyIfStale,
    } as never,
    maxLoaded: BASE,
    maxLoadedCeiling: CEILING,
    getFirstSeq: agentId => firstServer(agentId) ?? 0n,
    getLastSeq: agentId => lastServer(agentId) ?? 0n,
    getFirstServerSeq: firstServer,
    getLastServerSeq: lastServer,
    caughtUpToLiveTail: init.caughtUp ?? (() => true),
    addMessage,
    trimOldestEnd,
    trimNewestEnd,
    replaceTodos: vi.fn(),
    replaceBackgroundTasks,
    markBackgroundTasksLoadFailed,
    replaceGoal,
    loadLocalMessages: vi.fn(),
  })

  return { state, setState, paginator, trimNewestEnd, trimOldestEnd, addMessage, applyMessages, settleToWindow, resetToEmptyIfStale, replaceBackgroundTasks, markBackgroundTasksLoadFailed, replaceGoal }
}

describe('chathistorypaginator', () => {
  beforeEach(() => {
    listAgentMessages.mockReset()
  })

  describe('catchuptotail is viewport-aware', () => {
    it('caps the oldest end to the CEILING (not the base), preserving a scrolled-up buffer', async () => {
      const h = harness({ messages: [makeMsg(1n)], hasMoreNewer: false })
      listAgentMessages.mockResolvedValue(page([makeMsg(2n)], false))

      await h.paginator.catchUpToTail('w', 'a', 1n)

      expect(h.trimOldestEnd).toHaveBeenCalledWith('a', CEILING)
      expect(h.trimOldestEnd).not.toHaveBeenCalledWith('a', BASE)
    })
  })

  describe('catchuptotail settles a stranded live tail when the server is drained', () => {
    it('clamps the recorded tail to the window when an empty page leaves it short (no re-issue wedge)', async () => {
      // The reader is at the tail (hasMoreNewer false) but the recorded live tail (5)
      // sits ahead of the loaded window (1) -- a tail row deleted with an indeterminate
      // broadcast that couldn't lower the high-water. The server has nothing more, so
      // the loop drains to an empty page; without a settle, caughtUp never resolves and
      // the continuous reconcile re-fires this empty fetch forever.
      const h = harness({ messages: [makeMsg(1n)], hasMoreNewer: false, caughtUp: () => false, liveGet: () => 5n })
      listAgentMessages.mockResolvedValue(page([], false))

      await h.paginator.catchUpToTail('w', 'a', 1n)

      expect(h.settleToWindow).toHaveBeenCalledWith('a', 5n, 1n)
      expect(h.resetToEmptyIfStale).not.toHaveBeenCalled()
    })

    it('resets to empty when the whole window emptied (server has nothing)', async () => {
      // No server rows remain (the entire history was deleted) yet the recorded tail is
      // still positive: settleToWindow refuses an empty window, so an empty window must
      // route to resetToEmptyIfStale instead.
      const h = harness({ messages: [], hasMoreNewer: false, caughtUp: () => false, liveGet: () => 5n })
      listAgentMessages.mockResolvedValue(page([], false))

      await h.paginator.catchUpToTail('w', 'a', 0n)

      expect(h.resetToEmptyIfStale).toHaveBeenCalledWith('a', 5n)
      expect(h.settleToWindow).not.toHaveBeenCalled()
    })

    it('does NOT settle when the drain caught up (recorded tail reached)', async () => {
      const h = harness({ messages: [makeMsg(1n)], hasMoreNewer: false, caughtUp: () => true, liveGet: () => 1n })
      listAgentMessages.mockResolvedValue(page([], false))

      await h.paginator.catchUpToTail('w', 'a', 1n)

      expect(h.settleToWindow).not.toHaveBeenCalled()
      expect(h.resetToEmptyIfStale).not.toHaveBeenCalled()
    })
  })

  describe('jumptolatestmessages ties its re-seat fetch to the watch signal', () => {
    it('does NOT apply the latest page when the watch signal aborts mid-fetch (workspace switch)', async () => {
      // The empty-window re-seat fires for a backgrounded agent; the user switches
      // workspaces mid-fetch, aborting the WatchEvents subscription. The fetch must
      // discard its result rather than write a LATEST page into the navigated-away
      // agent's window (the leak this signal threading closes).
      const h = harness({ messages: [], hasMoreNewer: false, caughtUp: () => false, liveGet: () => 5n })
      const watch = new AbortController()
      listAgentMessages.mockImplementation(async () => {
        watch.abort() // the subscription tore down while the request was in flight
        return page([makeMsg(10n)], false)
      })

      await h.paginator.jumpToLatestMessages('w', 'a', watch.signal)

      expect(h.applyMessages).not.toHaveBeenCalled()
    })

    it('applies the latest page on a normal re-seat (no watch abort)', async () => {
      const h = harness({ messages: [], hasMoreNewer: false, caughtUp: () => true, liveGet: () => 0n })
      listAgentMessages.mockResolvedValue(page([makeMsg(10n)], false))

      await h.paginator.jumpToLatestMessages('w', 'a', new AbortController().signal)

      expect(h.applyMessages).toHaveBeenCalledWith('a', [makeMsg(10n)], false)
    })
  })

  describe('jumptomessagesaroundseq centers the window on a seq', () => {
    // Route the two parallel fetches by anchor so a test can return distinct
    // before/after pages and assert the cursor values.
    function routeByAnchor(before: ReturnType<typeof page>, after: ReturnType<typeof page>) {
      listAgentMessages.mockImplementation(async (_w: string, req: { anchor: number }) =>
        req.anchor === MessagePageAnchor.BEFORE ? before : after)
    }

    it('fetches BEFORE seq+1 and AFTER seq, applies the concatenated window, and sets flags', async () => {
      const h = harness({ messages: [makeMsg(1n)], caughtUp: () => true })
      routeByAnchor(
        page([makeMsg(4n), makeMsg(5n)], true), // more older history exists
        page([makeMsg(6n), makeMsg(7n)], false),
      )

      await h.paginator.jumpToMessagesAroundSeq('w', 'a', 5n)

      // Cursor values: BEFORE is exclusive at seq+1 (includes the target), AFTER at seq.
      expect(listAgentMessages).toHaveBeenCalledWith('w', expect.objectContaining({ anchor: MessagePageAnchor.BEFORE, cursorSeq: 6n }), expect.anything())
      expect(listAgentMessages).toHaveBeenCalledWith('w', expect.objectContaining({ anchor: MessagePageAnchor.AFTER, cursorSeq: 5n }), expect.anything())
      // One window swap with the disjoint, ascending concatenation; hasMoreOlder from before.
      expect(h.applyMessages).toHaveBeenCalledWith('a', [makeMsg(4n), makeMsg(5n), makeMsg(6n), makeMsg(7n)], true)
      // after.hasMore=false and caughtUp -> no newer.
      expect(h.state.hasMoreNewer.a).toBe(false)
    })

    it('does not overflow the BEFORE cursor when seeking the maximum int64 seq', async () => {
      const h = harness({ messages: [makeMsg(1n)], caughtUp: () => true })
      const maxInt64Seq = 9223372036854775807n
      listAgentMessages.mockImplementation(async (_w: string, req: { anchor: number }) =>
        req.anchor === MessagePageAnchor.LATEST
          ? page([makeMsg(maxInt64Seq)], true)
          : page([], false))

      await h.paginator.jumpToMessagesAroundSeq('w', 'a', maxInt64Seq)

      expect(listAgentMessages).toHaveBeenCalledWith('w', expect.objectContaining({
        anchor: MessagePageAnchor.LATEST,
        limit: MESSAGE_PAGE_SIZE,
      }), expect.anything())
      expect(listAgentMessages).toHaveBeenCalledWith('w', expect.objectContaining({
        anchor: MessagePageAnchor.AFTER,
        cursorSeq: maxInt64Seq,
        limit: MESSAGE_PAGE_SIZE,
      }), expect.anything())
      expect(listAgentMessages).not.toHaveBeenCalledWith('w', expect.objectContaining({
        cursorSeq: maxInt64Seq + 1n,
      }), expect.anything())
      expect(h.applyMessages).toHaveBeenCalledWith('a', [makeMsg(maxInt64Seq)], true)
      expect(h.state.hasMoreNewer.a).toBe(false)
    })

    it('keeps hasMoreNewer set when a live message bumped the tail during the fetch (!caughtUp)', async () => {
      const h = harness({ messages: [makeMsg(1n)], caughtUp: () => false })
      routeByAnchor(page([makeMsg(5n)], false), page([makeMsg(6n)], false))

      await h.paginator.jumpToMessagesAroundSeq('w', 'a', 5n)

      // after.hasMore=false, but !caughtUpToLiveTail forces hasMoreNewer true.
      expect(h.state.hasMoreNewer.a).toBe(true)
    })

    it('lands on the surviving newest rows when the target seq is in a deleted prefix', async () => {
      const h = harness({ messages: [makeMsg(1n)], caughtUp: () => true })
      // BEFORE empty (nothing at-or-below the deleted target), AFTER has survivors.
      routeByAnchor(page([], false), page([makeMsg(8n), makeMsg(9n)], false))

      await h.paginator.jumpToMessagesAroundSeq('w', 'a', 3n)

      // hasMoreOlder from before.hasMore=false (nothing older survives).
      expect(h.applyMessages).toHaveBeenCalledWith('a', [makeMsg(8n), makeMsg(9n)], false)
    })

    it('carries the watch signal to BOTH page requests, so a caller can abort the round trip', async () => {
      // The seek that asked for this page can give up mid-flight (the rail's scrub moves on).
      // Suppressing its landing is not enough: the fetch would still arrive and SWAP THE
      // WINDOW, leaving the reader's own scroll position over a window centred somewhere they
      // abandoned. The signal has to reach the transport.
      const h = harness({ messages: [makeMsg(1n)], caughtUp: () => true })
      const watch = new AbortController()
      // Hold both pages in flight, so the abort lands while the requests are still open --
      // which is the only moment aborting them means anything.
      let releaseBefore: (() => void) | undefined
      listAgentMessages.mockImplementation((_w: string, req: { anchor: number }) =>
        req.anchor === MessagePageAnchor.BEFORE
          ? new Promise((resolve) => {
              releaseBefore = () => resolve(page([makeMsg(5n)], false))
            })
          : Promise.resolve(page([makeMsg(6n)], false)))

      const jump = h.paginator.jumpToMessagesAroundSeq('w', 'a', 5n, watch.signal)
      await Promise.resolve()

      const signals = listAgentMessages.mock.calls.map((call: unknown[]) => (call[2] as { signal?: AbortSignal } | undefined)?.signal)
      expect(signals).toHaveLength(2)
      expect(signals.every((sig?: AbortSignal) => sig !== undefined)).toBe(true)
      expect(signals.some((sig?: AbortSignal) => sig?.aborted === true)).toBe(false)

      // The caller gives up: every request it opened is aborted, not merely ignored.
      watch.abort()
      expect(signals.every((sig?: AbortSignal) => sig?.aborted === true)).toBe(true)

      releaseBefore?.()
      await jump
      expect(h.applyMessages).not.toHaveBeenCalled() // and nothing swaps the window afterwards
    })

    it('leaves the window untouched when the fetch rejects because it was aborted', async () => {
      // An abort is how this fetch is designed to end early, so it must not read as a failure
      // and must not disturb the window the reader is looking at.
      const h = harness({ messages: [makeMsg(1n)], caughtUp: () => true })
      const watch = new AbortController()
      listAgentMessages.mockImplementation(async () => {
        watch.abort()
        throw new DOMException('aborted', 'AbortError')
      })

      await expect(h.paginator.jumpToMessagesAroundSeq('w', 'a', 5n, watch.signal)).resolves.toBeUndefined()
      expect(h.applyMessages).not.toHaveBeenCalled()
    })

    it('empties the window and resets a stale tail when the history vanished around the seq', async () => {
      const h = harness({ messages: [makeMsg(1n)], caughtUp: () => true })
      routeByAnchor(page([], false), page([], false))

      await h.paginator.jumpToMessagesAroundSeq('w', 'a', 5n)

      expect(h.applyMessages).toHaveBeenCalledWith('a', [], false)
      expect(h.resetToEmptyIfStale).toHaveBeenCalledWith('a', expect.anything())
    })
  })

  describe('catchuptotail is idempotent under the continuous reconcile effect', () => {
    it('skips a re-kick while a catch-up is already draining the agent (no RPC thrash)', async () => {
      const h = harness({ messages: [makeMsg(1n)], hasMoreNewer: false })
      // The first page hangs so the catch-up stays in flight; the second kick (what the
      // reconcile effect fires on every window/live-tail mutation) must no-op rather than
      // abort and re-issue the in-flight page.
      let resolveFirst!: (v: ReturnType<typeof page>) => void
      listAgentMessages.mockReturnValueOnce(new Promise<ReturnType<typeof page>>((r) => {
        resolveFirst = r
      }))

      const first = h.paginator.catchUpToTail('w', 'a', 1n)
      await h.paginator.catchUpToTail('w', 'a', 1n) // re-kick: guarded, returns at once

      expect(listAgentMessages).toHaveBeenCalledTimes(1)

      // Let the first drain (one page, no more) and confirm the slot is released so a
      // LATER kick (a genuinely new lag) can run again.
      resolveFirst(page([makeMsg(2n)], false))
      await first
      listAgentMessages.mockResolvedValue(page([makeMsg(3n)], false))
      await h.paginator.catchUpToTail('w', 'a', 2n)
      expect(listAgentMessages).toHaveBeenCalledTimes(2)
    })
  })

  describe('exhaustion-forced deferred tail fill', () => {
    it('marks the gap deferred (not a settled wall) when a storm outruns the bounded fill', async () => {
      // caughtUpToLiveTail never settles (a sustained broadcast storm) while every round
      // still advances the window, so the bounded fill runs out of attempts and PARKS:
      // hasMoreNewer is re-flagged AND the gap is tagged exhaustion-forced so the
      // reconcile resumes it (rather than stranding a following reader behind the affordance).
      const h = harness({ messages: [makeMsg(1n)], hasMoreNewer: false, caughtUp: () => false })
      let next = 2n
      listAgentMessages.mockImplementation(() => {
        const m = makeMsg(next)
        next += 1n
        return Promise.resolve(page([m], false)) // server tail reached, but live tail moved on
      })
      await h.paginator.forwardFillToLiveTail('w', 'a', new AbortController().signal, 0n)
      expect(h.state.hasMoreNewer.a).toBe(true)
      expect(h.state.tailFillDeferred.a).toBe(true)
    })

    it('resumeDeferredTailFill no-ops when no deferral is armed', async () => {
      const h = harness({ messages: [makeMsg(1n)], hasMoreNewer: false })
      await h.paginator.resumeDeferredTailFill('w', 'a')
      expect(listAgentMessages).not.toHaveBeenCalled()
    })

    it('a fresh fill that is already caught up clears a prior deferral', async () => {
      const h = harness({ messages: [makeMsg(1n)], hasMoreNewer: true }) // caughtUp default true
      h.setState('tailFillDeferred', 'a', true)
      await h.paginator.forwardFillToLiveTail('w', 'a', new AbortController().signal, 0n)
      // Entry clears it; caught up so the exhaustion branch never re-arms it.
      expect(h.state.tailFillDeferred.a).toBe(false)
    })

    it('resumeDeferredTailFill resumes a parked fill and clears the deferral once the storm subsides', async () => {
      let caught = false
      const h = harness({ messages: [makeMsg(1n)], hasMoreNewer: false, caughtUp: () => caught })
      let next = 2n
      listAgentMessages.mockImplementation(() => {
        const m = makeMsg(next)
        next += 1n
        return Promise.resolve(page([m], false))
      })
      // The storm parks the fill.
      await h.paginator.forwardFillToLiveTail('w', 'a', new AbortController().signal, 0n)
      expect(h.state.tailFillDeferred.a).toBe(true)
      // The storm subsides (caught up now): the reconcile-driven resume clears the deferral.
      caught = true
      await h.paginator.resumeDeferredTailFill('w', 'a')
      expect(h.state.tailFillDeferred.a).toBe(false)
    })
  })

  describe('forwardfilltolivetail all-locals guard', () => {
    it('does not page the OLDEST page as the tail when the window holds only locals', async () => {
      // Window holds only an optimistic local (seq 0n); a mid-fetch broadcast left the
      // recorded tail at 10n so it is not caught up. getLastSeq collapses to 0n, and
      // listMessagesAfter(0n) would fetch the OLDEST page -- which must NOT be spliced
      // in as the tail. The fill must re-anchor below the recorded tail instead.
      const h = harness({ messages: [makeMsg(0n, 'local-1')], hasMoreNewer: false, caughtUp: () => false, liveGet: () => 10n })
      listAgentMessages.mockResolvedValue(page([], false)) // the tail region is empty (vanished seq)
      await h.paginator.forwardFillToLiveTail('w', 'a', new AbortController().signal, 5n)
      // Never paged from 0n (the OLDEST page); the recovery re-anchored at recordedTail-1 (9n).
      expect(listAgentMessages).not.toHaveBeenCalledWith('w', expect.objectContaining({ cursorSeq: 0n }))
      expect(listAgentMessages).toHaveBeenCalledWith('w', expect.objectContaining({ cursorSeq: 9n }))
    })
  })

  describe('catchuptotail frees the single-flight slot at abort time', () => {
    it('lets a re-kick run instead of dropping it when a prior loop was aborted mid-await', async () => {
      const h = harness({ messages: [makeMsg(1n)], hasMoreNewer: false, caughtUp: () => false, liveGet: () => 9n })
      const watch = new AbortController()
      let resolveFirst: (v: ReturnType<typeof page>) => void = () => {}
      listAgentMessages.mockReturnValueOnce(new Promise((r) => {
        resolveFirst = r
      }))
      const first = h.paginator.catchUpToTail('w', 'a', 1n, watch.signal)
      await Promise.resolve() // let the first loop reach its hung await
      watch.abort() // teardown aborts it; the slot must free NOW, not at resume
      listAgentMessages.mockResolvedValue(page([makeMsg(2n)], false))
      await h.paginator.catchUpToTail('w', 'a', 1n) // re-kick
      expect(listAgentMessages).toHaveBeenCalledTimes(2) // would be 1 if the slot were still held
      resolveFirst(page([], false))
      await first
    })

    it('an aborted loop resuming late does NOT clobber a superseding loop\'s single-flight slot', async () => {
      // The abort listener frees the slot INSTANTLY on abort, so a re-kick can start a new
      // (superseding) loop while the aborted one is still suspended on its signal-less RPC.
      // When that aborted loop finally resumes into its `finally`, it must NOT delete the
      // superseding loop's slot -- else the next reconcile tick passes the single-flight guard,
      // starts a THIRD loop, and aborts the superseding loop's in-flight page (RPC thrash).
      const h = harness({ messages: [makeMsg(1n)], hasMoreNewer: false, caughtUp: () => false, liveGet: () => 9n })
      const watch = new AbortController()
      let resolveFirst: (v: ReturnType<typeof page>) => void = () => {}
      let resolveSecond: (v: ReturnType<typeof page>) => void = () => {}
      listAgentMessages
        .mockReturnValueOnce(new Promise((r) => { resolveFirst = r })) // loop 1 hangs
        .mockReturnValueOnce(new Promise((r) => { resolveSecond = r })) // loop 2 (superseding) hangs

      const first = h.paginator.catchUpToTail('w', 'a', 1n, watch.signal)
      await Promise.resolve()
      watch.abort() // aborts loop 1; the abort listener frees the slot immediately
      const second = h.paginator.catchUpToTail('w', 'a', 1n) // superseding loop 2 claims the freed slot
      await Promise.resolve()
      expect(listAgentMessages).toHaveBeenCalledTimes(2)

      // Loop 1's aborted RPC resolves LATE; its finally must leave loop 2's slot intact.
      resolveFirst(page([], false))
      await first

      // A reconcile re-kick while loop 2 is still draining: guarded OFF iff its slot survived.
      await h.paginator.catchUpToTail('w', 'a', 1n)
      expect(listAgentMessages).toHaveBeenCalledTimes(2) // 3 if loop 1 clobbered loop 2's slot

      resolveSecond(page([], false))
      await second
    })
  })
})

describe('linkwatchsignal', () => {
  it('aborts the controller immediately when the watch signal is already aborted', () => {
    const watch = new AbortController()
    watch.abort()
    const controller = new AbortController()
    linkWatchSignal(controller, watch.signal)
    expect(controller.signal.aborted).toBe(true)
  })

  it('aborts the controller when the watch signal fires later', () => {
    const watch = new AbortController()
    const controller = new AbortController()
    linkWatchSignal(controller, watch.signal)
    expect(controller.signal.aborted).toBe(false)
    watch.abort()
    expect(controller.signal.aborted).toBe(true)
  })

  it('removes its watch-signal listener once the controller is aborted (no per-fetch leak)', () => {
    const watch = new AbortController()
    const remove = vi.spyOn(watch.signal, 'removeEventListener')
    const controller = new AbortController()
    linkWatchSignal(controller, watch.signal)
    // A superseding fetch aborts the prior controller; the watch-signal listener
    // must come off so a long-lived subscription doesn't accumulate one per fetch.
    controller.abort()
    expect(remove).toHaveBeenCalledWith('abort', expect.any(Function))
    // And the now-removed listener must NOT re-abort a fresh controller when the
    // watch signal later fires.
    const survivor = new AbortController()
    linkWatchSignal(survivor, watch.signal)
    // The first controller is already aborted; firing the watch signal should only
    // touch the survivor, proving the first listener is gone.
    watch.abort()
    expect(survivor.signal.aborted).toBe(true)
  })

  it('returns an idempotent cleanup for a normally completed fetch', () => {
    const watch = new AbortController()
    const remove = vi.spyOn(watch.signal, 'removeEventListener')
    const controller = new AbortController()
    const cleanup = linkWatchSignal(controller, watch.signal)
    cleanup()
    cleanup()
    expect(remove).toHaveBeenCalledTimes(1)
    expect(remove).toHaveBeenCalledWith('abort', expect.any(Function))
    expect(controller.signal.aborted).toBe(false)
  })

  it('cleans up the watch listener when catchuptotail completes normally', async () => {
    const watch = new AbortController()
    const remove = vi.spyOn(watch.signal, 'removeEventListener')
    const h = harness({ messages: [makeMsg(1n)], hasMoreNewer: false })
    listAgentMessages.mockResolvedValue(page([], false))

    await h.paginator.catchUpToTail('w', 'a', 1n, watch.signal)

    expect(remove).toHaveBeenCalledWith('abort', expect.any(Function))
  })

  it('does nothing without a watch signal', () => {
    const controller = new AbortController()
    expect(() => linkWatchSignal(controller, undefined)).not.toThrow()
    expect(controller.signal.aborted).toBe(false)
  })
})

/**
 * The cold-start page carries the registry, and `background_tasks_loaded=false`
 * says the worker's query FAILED (a child agent reports loaded=true with an
 * empty list). The section is hidden when the registry is empty, so treating a
 * failure as emptiness took the whole section off screen with nothing to say
 * why -- which a database missing a column did, leaving only a slog.Warn.
 */
describe('chathistorypaginator background-task snapshot', () => {
  beforeEach(() => {
    listAgentMessages.mockReset()
  })

  function latestPage(loaded: boolean) {
    return {
      messages: [],
      hasMore: false,
      todos: [],
      todosLoaded: true,
      backgroundTasks: [],
      backgroundTasksLoaded: loaded,
    }
  }

  it('applies the registry when the worker answered', async () => {
    const h = harness({ messages: [] })
    listAgentMessages.mockResolvedValue(latestPage(true))

    await h.paginator.loadInitialMessages('w', 'a')

    expect(h.replaceBackgroundTasks).toHaveBeenCalledWith('a', [])
    expect(h.markBackgroundTasksLoadFailed).not.toHaveBeenCalled()
  })

  it('records a failure when the worker could not answer', async () => {
    const h = harness({ messages: [] })
    listAgentMessages.mockResolvedValue(latestPage(false))

    await h.paginator.loadInitialMessages('w', 'a')

    expect(h.markBackgroundTasksLoadFailed).toHaveBeenCalledWith('a')
    // ...and the registry is NOT overwritten: an empty repeated field on a
    // failed query would wipe whatever the client already had.
    expect(h.replaceBackgroundTasks).not.toHaveBeenCalled()
  })

  /**
   * The OTHER way the registry cannot be read. The in-band flag only travels on
   * a response that arrived; when the call itself rejects, nothing applies the
   * page at all -- and the section, which is hidden while empty, would leave the
   * screen with nothing to say why.
   */
  it('records a failure when the cold-start call itself rejects', async () => {
    const h = harness({ messages: [] })
    listAgentMessages.mockRejectedValue(new Error('worker unreachable'))

    await expect(h.paginator.loadInitialMessages('w', 'a')).rejects.toThrow('worker unreachable')

    expect(h.markBackgroundTasksLoadFailed).toHaveBeenCalledWith('a')
  })

  /**
   * The session goal travels with its ORDERING STAMP, and this is the path the
   * stamp exists for: the worker reads the row here, and the browser applies
   * the answer a round trip later, so a goal change in between must win. The
   * store drops a strictly older stamp -- but only if this hand-off carries it.
   */
  it('applies the goal and the stamp that orders it', async () => {
    const h = harness({ messages: [] })
    const goal = { objective: 'ship it' }
    listAgentMessages.mockResolvedValue({
      ...latestPage(true),
      goal,
      goalLoaded: true,
      goalSupportedActions: [1],
      goalUpdatedAt: '2026-09-06T10:00:00.000Z',
    })

    await h.paginator.loadInitialMessages('w', 'a')

    expect(h.replaceGoal).toHaveBeenCalledWith('a', goal, [1], '2026-09-06T10:00:00.000Z')
  })

  /**
   * A CHILD agent answers loaded=true with an ABSENT goal, and that write is
   * what clears a goal a tab inherited from a previous root -- so an absent goal
   * still has to be applied, with its stamp.
   */
  it('applies an absent goal, so a tab does not keep a previous root\'s', async () => {
    const h = harness({ messages: [] })
    listAgentMessages.mockResolvedValue({
      ...latestPage(true),
      goal: undefined,
      goalLoaded: true,
      goalSupportedActions: [],
      goalUpdatedAt: '2026-09-06T10:00:00.000Z',
    })

    await h.paginator.loadInitialMessages('w', 'a')

    expect(h.replaceGoal).toHaveBeenCalledWith('a', undefined, [], '2026-09-06T10:00:00.000Z')
  })

  // A DB error leaves goal_loaded false, and skipping the write is what stops a
  // transient failure from blanking a live card.
  it('leaves the goal alone when the worker could not read it', async () => {
    const h = harness({ messages: [] })
    listAgentMessages.mockResolvedValue({
      ...latestPage(true),
      goal: undefined,
      goalLoaded: false,
      goalSupportedActions: [],
      goalUpdatedAt: '',
    })

    await h.paginator.loadInitialMessages('w', 'a')

    expect(h.replaceGoal).not.toHaveBeenCalled()
  })
})
