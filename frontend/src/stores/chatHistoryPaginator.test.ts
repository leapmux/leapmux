import type { ChatStoreState } from './chat.store'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { createStore } from 'solid-js/store'
import { beforeEach, describe, expect, it, vi } from 'vitest'

// listAgentMessages is the only external dependency; hoist the mock so the factory
// can reference it (vi.mock is hoisted above imports).
const { listAgentMessages } = vi.hoisted(() => ({ listAgentMessages: vi.fn() }))
vi.mock('~/api/workerRpc', () => ({ listAgentMessages }))

const { createHistoryPaginator, linkWatchSignal } = await import('./chatHistoryPaginator')
const { MessageSource } = await import('~/generated/leapmux/v1/agent_pb')

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
    loadLocalMessages: vi.fn(),
  })

  return { state, setState, paginator, trimNewestEnd, trimOldestEnd, addMessage, applyMessages, settleToWindow, resetToEmptyIfStale }
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
