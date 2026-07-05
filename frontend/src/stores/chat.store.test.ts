import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { listMessageMarks } from '~/api/workerRpc'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MarkType, MessageMarkSchema, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { createChatStore } from './chat.store'

vi.mock('~/api/workerRpc', () => ({
  getAgentMessage: vi.fn(),
  listMessageMarks: vi.fn(),
}))

function jsonContent(value: unknown): Uint8Array {
  return new TextEncoder().encode(JSON.stringify(value))
}

function claudeToolUse(id: string, seq: bigint, spanId: string): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.AGENT,
    content: jsonContent({
      type: 'assistant',
      message: {
        role: 'assistant',
        content: [{ type: 'tool_use', id: 'toolu_1', name: 'TaskGet', input: { task_id: 'task-1' } }],
      },
    }),
    contentCompression: ContentCompression.NONE,
    seq,
    agentProvider: AgentProvider.CLAUDE_CODE,
    spanId,
  })
}

function claudeToolResult(id: string, seq: bigint, spanId: string, content: string): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.USER,
    content: jsonContent({
      type: 'user',
      message: {
        role: 'user',
        content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content }],
      },
    }),
    contentCompression: ContentCompression.NONE,
    seq,
    agentProvider: AgentProvider.CLAUDE_CODE,
    spanId,
  })
}

function markedMessage(id: string, seq: bigint, markType = MarkType.USER_MESSAGE): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.USER,
    content: jsonContent({ content: id }),
    contentCompression: ContentCompression.NONE,
    seq,
    agentProvider: AgentProvider.CLAUDE_CODE,
    markType,
  })
}

function messageMark(seq: bigint, type: MarkType) {
  return create(MessageMarkSchema, { seq, type })
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

beforeEach(() => {
  vi.mocked(listMessageMarks).mockReset()
})

describe('chatstore span content versions', () => {
  it('exposes same-seq tool_result content-version bumps by span id', () => {
    const store = createChatStore()
    const agentId = 'agent-1'
    const spanId = 'span-1'

    store.setMessages(agentId, [
      claudeToolUse('opener-1', 1n, spanId),
      claudeToolResult('result-1', 2n, spanId, 'first result'),
    ])

    expect(JSON.stringify(store.getToolResultParsedBySpanId(agentId, spanId)?.parentObject)).toContain('first result')
    expect(store.getToolUseRevisionBySpanId(agentId, spanId)).toEqual({ id: 'opener-1', seq: 1n, contentVersion: 0 })
    expect(store.getToolResultRevisionBySpanId(agentId, spanId)).toEqual({ id: 'result-1', seq: 2n, contentVersion: 0 })
    expect(store.getToolResultContentVersionBySpanId(agentId, spanId)).toBe(0)
    expect(store.getToolUseContentVersionBySpanId(agentId, spanId)).toBe(0)

    store.addMessage(agentId, claudeToolResult('result-1', 2n, spanId, 'updated result'))

    expect(JSON.stringify(store.getToolResultParsedBySpanId(agentId, spanId)?.parentObject)).toContain('updated result')
    expect(store.getToolResultRevisionBySpanId(agentId, spanId)).toEqual({ id: 'result-1', seq: 2n, contentVersion: 1 })
    expect(store.getToolResultContentVersionBySpanId(agentId, spanId)).toBe(1)
    expect(store.getToolUseContentVersionBySpanId(agentId, spanId)).toBe(0)
  })

  it('updates span revisions when a full window replace swaps a sibling at content version zero', () => {
    const store = createChatStore()
    const agentId = 'agent-1'
    const spanId = 'span-1'

    store.setMessages(agentId, [
      claudeToolUse('opener-1', 1n, spanId),
      claudeToolResult('result-1', 2n, spanId, 'first result'),
    ])

    store.setMessages(agentId, [
      claudeToolUse('opener-2', 3n, spanId),
      claudeToolResult('result-1', 2n, spanId, 'first result'),
    ])

    expect(store.getToolUseContentVersionBySpanId(agentId, spanId)).toBe(0)
    expect(store.getToolUseRevisionBySpanId(agentId, spanId)).toEqual({ id: 'opener-2', seq: 3n, contentVersion: 0 })
  })

  it('returns zero when no result is indexed for a span id', () => {
    const store = createChatStore()
    const agentId = 'agent-1'
    const spanId = 'span-1'

    store.setMessages(agentId, [claudeToolUse('opener-1', 1n, spanId)])

    expect(store.getToolResultParsedBySpanId(agentId, spanId)).toBeUndefined()
    expect(store.getToolResultContentVersionBySpanId(agentId, spanId)).toBe(0)
  })
})

describe('chatstore get loaded message by seq', () => {
  it('finds a loaded message by seq, skipping gaps, optimistic locals, and unknown agents', () => {
    const store = createChatStore()
    const agentId = 'agent-1'
    store.setMessages(agentId, [
      claudeToolUse('m1', 3n, 'span-1'),
      claudeToolResult('m2', 5n, 'span-1', 'result'),
    ])

    expect(store.getLoadedMessageBySeq(agentId, 3n)?.id).toBe('m1')
    expect(store.getLoadedMessageBySeq(agentId, 5n)?.id).toBe('m2')
    // A seq that is not loaded (a gap between the two rows) resolves to undefined.
    expect(store.getLoadedMessageBySeq(agentId, 4n)).toBeUndefined()
    // Optimistic locals carry seq 0n; the guard must never return one for a "0" mark.
    expect(store.getLoadedMessageBySeq(agentId, 0n)).toBeUndefined()
    // An agent with no loaded window resolves to undefined rather than throwing.
    expect(store.getLoadedMessageBySeq('other-agent', 5n)).toBeUndefined()
  })

  it('finds a server row even when optimistic locals trail the window', () => {
    const store = createChatStore()
    const agentId = 'agent-1'
    const local = create(AgentChatMessageSchema, {
      id: 'local-1',
      source: MessageSource.USER,
      content: jsonContent({ content: 'unsent' }),
      contentCompression: ContentCompression.NONE,
      seq: 0n,
      agentProvider: AgentProvider.CLAUDE_CODE,
    })
    // Server rows [3n, 5n] ascending, then a trailing optimistic local (seq 0n). The
    // binary search must bound itself to the server region (serverMessageEnd) and still
    // resolve a server seq -- never wander into or trip over the trailing local.
    store.setMessages(agentId, [
      claudeToolUse('m1', 3n, 'span-1'),
      claudeToolResult('m2', 5n, 'span-1', 'result'),
      local,
    ])

    expect(store.getLoadedMessageBySeq(agentId, 3n)?.id).toBe('m1')
    expect(store.getLoadedMessageBySeq(agentId, 5n)?.id).toBe('m2')
    expect(store.getLoadedMessageBySeq(agentId, 4n)).toBeUndefined()
    expect(store.getLoadedMessageBySeq(agentId, 0n)).toBeUndefined()
  })
})

describe('chatstore loadmessagemarks', () => {
  it('does not request marks before the worker id is available', async () => {
    const store = createChatStore()

    await store.loadMessageMarks('', 'agent-1')

    expect(listMessageMarks).not.toHaveBeenCalled()
    expect(store.getRailData('agent-1').loaded).toBe(false)
  })

  it('ignores an older overlapping response that resolves after a newer seed', async () => {
    const store = createChatStore()
    const older = deferred<Awaited<ReturnType<typeof listMessageMarks>>>()
    const newer = deferred<Awaited<ReturnType<typeof listMessageMarks>>>()
    vi.mocked(listMessageMarks)
      .mockReturnValueOnce(older.promise)
      .mockReturnValueOnce(newer.promise)

    const firstLoad = store.loadMessageMarks('worker-1', 'agent-1')
    const secondLoad = store.loadMessageMarks('worker-1', 'agent-1')

    newer.resolve({
      $typeName: 'leapmux.v1.ListMessageMarksResponse',
      marks: [messageMark(9n, MarkType.USER_MESSAGE)],
      minSeq: 1n,
      maxSeq: 9n,
    })
    await secondLoad

    older.resolve({
      $typeName: 'leapmux.v1.ListMessageMarksResponse',
      marks: [messageMark(2n, MarkType.USER_MESSAGE)],
      minSeq: 1n,
      maxSeq: 2n,
    })
    await firstLoad

    expect(store.getRailData('agent-1').marks.map(m => m.seq)).toEqual([9n])
  })

  it('fences a pre-close seed still in flight across a close/reopen of the same agent', async () => {
    const store = createChatStore()
    const stale = deferred<Awaited<ReturnType<typeof listMessageMarks>>>()
    const fresh = deferred<Awaited<ReturnType<typeof listMessageMarks>>>()
    vi.mocked(listMessageMarks)
      .mockReturnValueOnce(stale.promise)
      .mockReturnValueOnce(fresh.promise)

    // Open the agent: its seed RPC goes in flight, then the tab closes (forgetAgent) before it
    // lands. A per-agent epoch counter would restart at 0 here; the reopen below would mint the
    // SAME epoch this stale seed holds, so its late resolve would slip past the cancel guard.
    const firstLoad = store.loadMessageMarks('worker-1', 'agent-1')
    store.forgetAgent('agent-1')

    // Reopen the same agent and seed it freshly.
    const secondLoad = store.loadMessageMarks('worker-1', 'agent-1')
    fresh.resolve({
      $typeName: 'leapmux.v1.ListMessageMarksResponse',
      marks: [messageMark(9n, MarkType.USER_MESSAGE)],
      minSeq: 9n,
      maxSeq: 9n,
    })
    await secondLoad
    expect(store.getRailData('agent-1').marks.map(m => m.seq)).toEqual([9n])

    // The pre-close seed resolves LATE with stale marks: the monotonic epoch must fence it so it
    // can't clobber the reopened window.
    stale.resolve({
      $typeName: 'leapmux.v1.ListMessageMarksResponse',
      marks: [messageMark(2n, MarkType.USER_MESSAGE), messageMark(3n, MarkType.USER_MESSAGE)],
      minSeq: 2n,
      maxSeq: 3n,
    })
    await firstLoad

    const rail = store.getRailData('agent-1')
    expect(rail.marks.map(m => m.seq)).toEqual([9n])
    expect(rail.maxSeq).toBe(9n)
  })

  it('does not let an in-flight seed resurrect a mark deleted by a live event', async () => {
    const store = createChatStore()
    store.addMessage('agent-1', markedMessage('m2', 2n))
    expect(store.getRailData('agent-1').marks.map(m => m.seq)).toEqual([2n])

    const stale = deferred<Awaited<ReturnType<typeof listMessageMarks>>>()
    vi.mocked(listMessageMarks)
      .mockReturnValueOnce(stale.promise)
      .mockResolvedValueOnce({
        $typeName: 'leapmux.v1.ListMessageMarksResponse',
        marks: [],
        minSeq: 0n,
        maxSeq: 0n,
      })
    const load = store.loadMessageMarks('worker-1', 'agent-1')

    store.removeMessage('agent-1', 'm2', 2n, 0n)
    stale.resolve({
      $typeName: 'leapmux.v1.ListMessageMarksResponse',
      marks: [messageMark(2n, MarkType.USER_MESSAGE)],
      minSeq: 1n,
      maxSeq: 2n,
    })
    await load

    expect(listMessageMarks).toHaveBeenCalledTimes(2)
    expect(store.getRailData('agent-1').marks.map(m => m.seq)).toEqual([])
    expect(store.getRailData('agent-1').loaded).toBe(true)
  })

  it('retries a stale seed so a live mark during startup still reveals the rail', async () => {
    const store = createChatStore()
    const stale = deferred<Awaited<ReturnType<typeof listMessageMarks>>>()
    vi.mocked(listMessageMarks)
      .mockReturnValueOnce(stale.promise)
      .mockResolvedValueOnce({
        $typeName: 'leapmux.v1.ListMessageMarksResponse',
        marks: [messageMark(2n, MarkType.USER_MESSAGE)],
        minSeq: 2n,
        maxSeq: 2n,
      })

    const load = store.loadMessageMarks('worker-1', 'agent-1')
    store.addMessage('agent-1', markedMessage('m2', 2n))
    stale.resolve({
      $typeName: 'leapmux.v1.ListMessageMarksResponse',
      marks: [],
      minSeq: 0n,
      maxSeq: 0n,
    })
    await load

    const rail = store.getRailData('agent-1')
    expect(listMessageMarks).toHaveBeenCalledTimes(2)
    expect(rail.loaded).toBe(true)
    expect(rail.marks.map(m => m.seq)).toEqual([2n])
  })

  it('schedules another marks seed when every immediate response races live mark changes', async () => {
    vi.useFakeTimers()
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    try {
      const store = createChatStore()
      const first = deferred<Awaited<ReturnType<typeof listMessageMarks>>>()
      const second = deferred<Awaited<ReturnType<typeof listMessageMarks>>>()
      const third = deferred<Awaited<ReturnType<typeof listMessageMarks>>>()
      vi.mocked(listMessageMarks)
        .mockReturnValueOnce(first.promise)
        .mockReturnValueOnce(second.promise)
        .mockReturnValueOnce(third.promise)
        .mockResolvedValueOnce({
          $typeName: 'leapmux.v1.ListMessageMarksResponse',
          marks: [
            messageMark(1n, MarkType.USER_MESSAGE),
            messageMark(2n, MarkType.USER_MESSAGE),
            messageMark(3n, MarkType.USER_MESSAGE),
            messageMark(99n, MarkType.USER_MESSAGE),
          ],
          minSeq: 1n,
          maxSeq: 99n,
        })

      const load = store.loadMessageMarks('worker-1', 'agent-1')

      store.addMessage('agent-1', markedMessage('m1', 1n))
      first.resolve({
        $typeName: 'leapmux.v1.ListMessageMarksResponse',
        marks: [],
        minSeq: 0n,
        maxSeq: 0n,
      })
      await Promise.resolve()

      store.addMessage('agent-1', markedMessage('m2', 2n))
      second.resolve({
        $typeName: 'leapmux.v1.ListMessageMarksResponse',
        marks: [messageMark(1n, MarkType.USER_MESSAGE)],
        minSeq: 1n,
        maxSeq: 1n,
      })
      await Promise.resolve()

      store.addMessage('agent-1', markedMessage('m3', 3n))
      third.resolve({
        $typeName: 'leapmux.v1.ListMessageMarksResponse',
        marks: [messageMark(2n, MarkType.USER_MESSAGE)],
        minSeq: 2n,
        maxSeq: 2n,
      })
      await load

      expect(listMessageMarks).toHaveBeenCalledTimes(3)
      expect(store.getRailData('agent-1').loaded).toBe(false)
      expect(warn).toHaveBeenCalledWith('message marks seed retry scheduled', {
        agentId: 'agent-1',
        reason: 'live updates kept racing the seed',
        rescheduleDepth: 0,
      })

      await vi.runOnlyPendingTimersAsync()
      await Promise.resolve()

      expect(listMessageMarks).toHaveBeenCalledTimes(4)
      const rail = store.getRailData('agent-1')
      expect(rail.loaded).toBe(true)
      expect(rail.marks.map(m => m.seq)).toEqual([1n, 2n, 3n, 99n])
    }
    finally {
      warn.mockRestore()
      vi.useRealTimers()
    }
  })

  it('retries a FIRST seed whose range came back indeterminate, so a transient DB error does not hide the rail all session', async () => {
    vi.useFakeTimers()
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    try {
      const store = createChatStore()
      // First seed: marks present but the worker's seq-range subquery errored (-1/-1). The
      // rail must stay hidden (loaded=false) yet schedule a retry -- the live add/remove path
      // can never reveal a never-seeded rail, so without the retry it hides for the session.
      vi.mocked(listMessageMarks)
        .mockResolvedValueOnce({
          $typeName: 'leapmux.v1.ListMessageMarksResponse',
          marks: [messageMark(3n, MarkType.USER_MESSAGE)],
          minSeq: undefined, // indeterminate: the worker left the range unset (a DB error)
          maxSeq: undefined,
        })
        .mockResolvedValueOnce({
          $typeName: 'leapmux.v1.ListMessageMarksResponse',
          marks: [messageMark(3n, MarkType.USER_MESSAGE)],
          minSeq: 1n,
          maxSeq: 8n,
        })

      await store.loadMessageMarks('worker-1', 'agent-1')
      expect(store.getRailData('agent-1').loaded).toBe(false)
      expect(warn).toHaveBeenCalledWith('message marks seed retry scheduled', {
        agentId: 'agent-1',
        reason: 'first seed range indeterminate',
        rescheduleDepth: 0,
      })

      await vi.runOnlyPendingTimersAsync()
      await Promise.resolve()

      expect(listMessageMarks).toHaveBeenCalledTimes(2)
      const rail = store.getRailData('agent-1')
      expect(rail.loaded).toBe(true)
      expect(rail.minSeq).toBe(1n)
      expect(rail.marks.map(m => m.seq)).toEqual([3n])
    }
    finally {
      warn.mockRestore()
      vi.useRealTimers()
    }
  })

  it('cancels a pending marks seed retry when reseeded without a worker', async () => {
    vi.useFakeTimers()
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    try {
      const store = createChatStore()
      const first = deferred<Awaited<ReturnType<typeof listMessageMarks>>>()
      const second = deferred<Awaited<ReturnType<typeof listMessageMarks>>>()
      const third = deferred<Awaited<ReturnType<typeof listMessageMarks>>>()
      vi.mocked(listMessageMarks)
        .mockReturnValueOnce(first.promise)
        .mockReturnValueOnce(second.promise)
        .mockReturnValueOnce(third.promise)
        .mockResolvedValueOnce({
          $typeName: 'leapmux.v1.ListMessageMarksResponse',
          marks: [messageMark(99n, MarkType.USER_MESSAGE)],
          minSeq: 99n,
          maxSeq: 99n,
        })

      const load = store.loadMessageMarks('worker-1', 'agent-1')

      store.addMessage('agent-1', markedMessage('m1', 1n))
      first.resolve({
        $typeName: 'leapmux.v1.ListMessageMarksResponse',
        marks: [],
        minSeq: 0n,
        maxSeq: 0n,
      })
      await Promise.resolve()
      store.addMessage('agent-1', markedMessage('m2', 2n))
      second.resolve({
        $typeName: 'leapmux.v1.ListMessageMarksResponse',
        marks: [],
        minSeq: 0n,
        maxSeq: 0n,
      })
      await Promise.resolve()
      store.addMessage('agent-1', markedMessage('m3', 3n))
      third.resolve({
        $typeName: 'leapmux.v1.ListMessageMarksResponse',
        marks: [],
        minSeq: 0n,
        maxSeq: 0n,
      })
      await load

      expect(listMessageMarks).toHaveBeenCalledTimes(3)

      await store.loadMessageMarks('', 'agent-1')
      await vi.runOnlyPendingTimersAsync()

      expect(listMessageMarks).toHaveBeenCalledTimes(3)
    }
    finally {
      warn.mockRestore()
      vi.useRealTimers()
    }
  })

  it('stops reseeding after a bounded number of retries when the range stays indeterminate, instead of polling forever', async () => {
    vi.useFakeTimers()
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    try {
      const store = createChatStore()
      // The worker's seq-range subquery is PERSISTENTLY broken: every response carries marks but
      // an indeterminate (-1) range, so seed() keeps loaded=false. Without a cap this reschedules
      // a fetch every retry-delay for the whole session -- an infinite RPC poll.
      vi.mocked(listMessageMarks).mockResolvedValue({
        $typeName: 'leapmux.v1.ListMessageMarksResponse',
        marks: [messageMark(3n, MarkType.USER_MESSAGE)],
        minSeq: undefined, // indeterminate: the worker left the range unset (a DB error)
        maxSeq: undefined,
      })

      await store.loadMessageMarks('worker-1', 'agent-1')
      // Drive far more reschedule ticks than the cap allows; the chain must terminate.
      for (let i = 0; i < 20; i++) {
        await vi.runOnlyPendingTimersAsync()
        await Promise.resolve()
      }

      // Bounded to MAX_MESSAGE_MARK_SEED_RESCHEDULES fetches total, then it gives up.
      expect(listMessageMarks).toHaveBeenCalledTimes(5)
      expect(store.getRailData('agent-1').loaded).toBe(false)
      expect(warn).toHaveBeenCalledWith('message marks seed gave up after retries', expect.objectContaining({
        agentId: 'agent-1',
        reason: 'first seed range indeterminate',
      }))
      // No pending timer remains -- the poll has genuinely stopped, not just paused.
      expect(vi.getTimerCount()).toBe(0)
    }
    finally {
      warn.mockRestore()
      vi.useRealTimers()
    }
  })

  it('reseeds a loaded agent whose reseed range came back indeterminate, healing a dropped beyond-horizon mark', async () => {
    vi.useFakeTimers()
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    try {
      const store = createChatStore()
      vi.mocked(listMessageMarks)
        // 1) First seed: good range reveals the rail with mark 2n.
        .mockResolvedValueOnce({
          $typeName: 'leapmux.v1.ListMessageMarksResponse',
          marks: [messageMark(2n, MarkType.USER_MESSAGE)],
          minSeq: 1n,
          maxSeq: 5n,
        })
        // 2) Reseed: seq-range subquery errored (-1/-1) AND its snapshot missed the live 9n mark
        //    noted below, so seed() drops 9n (horizon unknown) while staying loaded.
        .mockResolvedValueOnce({
          $typeName: 'leapmux.v1.ListMessageMarksResponse',
          marks: [messageMark(2n, MarkType.USER_MESSAGE)],
          minSeq: undefined, // indeterminate: the worker left the range unset (a DB error)
          maxSeq: undefined,
        })
        // 3) Retry: horizon healed and now includes 9n -- restores the dropped dot.
        .mockResolvedValueOnce({
          $typeName: 'leapmux.v1.ListMessageMarksResponse',
          marks: [messageMark(2n, MarkType.USER_MESSAGE), messageMark(9n, MarkType.USER_MESSAGE)],
          minSeq: 1n,
          maxSeq: 9n,
        })

      await store.loadMessageMarks('worker-1', 'agent-1')
      expect(store.getRailData('agent-1').loaded).toBe(true)

      // A live send lands beyond the seeded horizon.
      store.addMessage('agent-1', markedMessage('m9', 9n))
      expect(store.getRailData('agent-1').marks.map(m => m.seq)).toEqual([2n, 9n])

      // The indeterminate reseed drops 9n transiently but MUST schedule a retry to heal it.
      await store.loadMessageMarks('worker-1', 'agent-1')
      expect(store.getRailData('agent-1').marks.map(m => m.seq)).toEqual([2n])
      expect(warn).toHaveBeenCalledWith('message marks seed retry scheduled', {
        agentId: 'agent-1',
        reason: 'reseed range indeterminate',
        rescheduleDepth: 0,
      })

      await vi.runOnlyPendingTimersAsync()
      await Promise.resolve()

      expect(listMessageMarks).toHaveBeenCalledTimes(3)
      expect(store.getRailData('agent-1').marks.map(m => m.seq)).toEqual([2n, 9n])
    }
    finally {
      warn.mockRestore()
      vi.useRealTimers()
    }
  })

  it('retries a transient rpc rejection so a one-off failure does not hide the rail all session', async () => {
    vi.useFakeTimers()
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    try {
      const store = createChatStore()
      // The initial seed RPC rejects once (a network blip). An outright rejection never reaches
      // seed(), so the indeterminate-range retry can't cover it; without a retry here the rail
      // stays hidden for the whole session (loaded never flips, and the live add/remove path
      // can't reveal a never-seeded rail). A bounded retry heals it.
      vi.mocked(listMessageMarks)
        .mockRejectedValueOnce(new Error('worker busy'))
        .mockResolvedValueOnce({
          $typeName: 'leapmux.v1.ListMessageMarksResponse',
          marks: [messageMark(3n, MarkType.USER_MESSAGE)],
          minSeq: 1n,
          maxSeq: 8n,
        })

      await store.loadMessageMarks('worker-1', 'agent-1')
      expect(store.getRailData('agent-1').loaded).toBe(false)
      expect(warn).toHaveBeenCalledWith('failed to load message marks', expect.objectContaining({ agentId: 'agent-1' }))
      expect(warn).toHaveBeenCalledWith('message marks seed retry scheduled', {
        agentId: 'agent-1',
        reason: 'list message marks rpc failed',
        rescheduleDepth: 0,
      })

      await vi.runOnlyPendingTimersAsync()
      await Promise.resolve()

      expect(listMessageMarks).toHaveBeenCalledTimes(2)
      const rail = store.getRailData('agent-1')
      expect(rail.loaded).toBe(true)
      expect(rail.marks.map(m => m.seq)).toEqual([3n])
    }
    finally {
      warn.mockRestore()
      vi.useRealTimers()
    }
  })

  it('skips the seed entirely when the subscription signal is already aborted', async () => {
    const store = createChatStore()
    const controller = new AbortController()
    controller.abort()

    await store.loadMessageMarks('worker-1', 'agent-1', controller.signal)

    expect(listMessageMarks).not.toHaveBeenCalled()
    expect(store.getRailData('agent-1').loaded).toBe(false)
  })

  it('ties the RPC to the subscription signal and drops a response that lands after teardown', async () => {
    const store = createChatStore()
    const controller = new AbortController()
    const inflight = deferred<Awaited<ReturnType<typeof listMessageMarks>>>()
    vi.mocked(listMessageMarks).mockReturnValueOnce(inflight.promise)

    const load = store.loadMessageMarks('worker-1', 'agent-1', controller.signal)
    // The RPC carries the subscription's signal, so a teardown aborts it mid-flight.
    expect(listMessageMarks).toHaveBeenCalledWith('worker-1', { agentId: 'agent-1' }, { signal: controller.signal })

    // The subscription tears down (workspace switch / reconnect) before the response lands.
    controller.abort()
    inflight.resolve({
      $typeName: 'leapmux.v1.ListMessageMarksResponse',
      marks: [messageMark(2n, MarkType.USER_MESSAGE)],
      minSeq: 1n,
      maxSeq: 2n,
    })
    await load

    // The late response is dropped: no seed against a worker the reader navigated away from.
    expect(store.getRailData('agent-1').loaded).toBe(false)
    expect(store.getRailData('agent-1').marks).toEqual([])
  })

  it('does not log or retry when a torn-down subscription aborts the in-flight RPC', async () => {
    vi.useFakeTimers()
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    try {
      const store = createChatStore()
      const controller = new AbortController()
      const inflight = deferred<Awaited<ReturnType<typeof listMessageMarks>>>()
      vi.mocked(listMessageMarks).mockReturnValueOnce(inflight.promise)

      const load = store.loadMessageMarks('worker-1', 'agent-1', controller.signal)
      // Teardown aborts the RPC, which rejects (AbortError) -- a deliberate cancellation, NOT a
      // transient failure, so it must neither warn nor schedule a retry (unlike a network blip).
      controller.abort()
      inflight.reject(new DOMException('aborted', 'AbortError'))
      await load

      expect(warn).not.toHaveBeenCalledWith('failed to load message marks', expect.anything())
      expect(warn).not.toHaveBeenCalledWith('message marks seed retry scheduled', expect.anything())
      expect(listMessageMarks).toHaveBeenCalledTimes(1)
      expect(store.getRailData('agent-1').loaded).toBe(false)
    }
    finally {
      warn.mockRestore()
      vi.useRealTimers()
    }
  })
})
