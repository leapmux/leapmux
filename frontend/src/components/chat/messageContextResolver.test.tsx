import type { MessageInitShape } from '@bufbuild/protobuf'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { MessageSpanIdentity } from '~/lib/messageSpan'
import type { TodoItem } from '~/models/todo'
import { create } from '@bufbuild/protobuf'
import { createEffect, createMemo, createRoot, createSignal, untrack } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { createChatStore } from '~/stores/chat.store'
import { createSpanIndex } from '~/stores/chatSpanIndex'
import { createMessageContextResolver } from './messageContextResolver'
import './providers/opencode/plugin'

const cleanups: Array<() => void> = []
afterEach(() => cleanups.splice(0).forEach(dispose => dispose()))

const waitForRenderPrune = () => new Promise<void>(resolve => setTimeout(resolve, 0))

function toolMessage(id: string, seq: bigint, side: 'request' | 'result', extra: MessageInitShape<typeof AgentChatMessageSchema> = {}): AgentChatMessage {
  const content = {
    sessionUpdate: side === 'request' ? 'tool_call' : 'tool_call_update',
    toolCallId: 'span',
    status: side === 'request' ? 'pending' : 'completed',
    kind: 'read',
    rawInput: side === 'request' ? { filePath: '/project/file.ts' } : undefined,
  }
  return create(AgentChatMessageSchema, {
    id,
    seq,
    spanId: 'span',
    agentProvider: AgentProvider.OPENCODE,
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify(content)),
    contentCompression: ContentCompression.NONE,
    ...extra,
  })
}

function fixture(initial: AgentChatMessage[] = []) {
  return createRoot((dispose) => {
    cleanups.push(dispose)
    const [messages, setMessages] = createSignal(initial)
    const [version, setVersion] = createSignal(0)
    const [todo, setTodo] = createSignal<TodoItem>()
    const index = createSpanIndex()
    index.reindex('agent', initial)
    const observers = new Set<(message: AgentChatMessage) => void>()
    const fetchSpan = vi.fn(async (_identity: MessageSpanIdentity, _signal: AbortSignal): Promise<AgentChatMessage[]> => [])
    const fetchMessage = vi.fn(async (_seq: bigint, _signal: AbortSignal): Promise<AgentChatMessage | undefined> => undefined)
    const resolver = createMessageContextResolver({
      scopeKey: 'worker/agent',
      messages,
      messageVersion: version,
      contentVersion: () => 0,
      spanMessage: (spanId, side) => side === 'request' ? index.getOpenerMessage('agent', spanId) : index.getResultMessage('agent', spanId),
      messageBySeq: seq => messages().find(message => message.seq === seq),
      fetchSpan,
      fetchMessage,
      fetchFileImage: async () => { throw new Error('The image source is unavailable') },
      subscribe: (observer) => {
        observers.add(observer)
        return () => observers.delete(observer)
      },
      todo: () => todo(),
      backgroundTask: () => undefined,
      progress: () => undefined,
    })
    return {
      resolver,
      fetchSpan,
      fetchMessage,
      setTodo,
      dispose,
      emit: (message: AgentChatMessage) => observers.forEach(observer => observer(message)),
      replaceMessages: (next: AgentChatMessage[]) => {
        index.reindex('agent', next)
        setMessages(next)
        setVersion(value => value + 1)
      },
    }
  })
}

describe('message context resolver', () => {
  it('separates identical tool IDs in loaded provider sessions', () => {
    const first = toolMessage('first-request', 1n, 'request', { agentSessionId: 'first-session' })
    const second = toolMessage('second-request', 2n, 'request', { agentSessionId: 'second-session' })
    const { resolver } = fixture([first, second])
    expect(resolver.request({ spanId: 'span', agentSessionId: 'first-session' })?.message.id).toBe(first.id)
    expect(resolver.request({ spanId: 'span', agentSessionId: 'second-session' })?.message.id).toBe(second.id)
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })).toBeUndefined()
  })

  it('rejects a fetched tool from another provider session', async () => {
    const { resolver, fetchSpan } = fixture()
    fetchSpan.mockResolvedValue([toolMessage('foreign', 1n, 'request', { agentSessionId: 'foreign-session' })])
    await expect(resolver.loadSpan({ spanId: 'span', agentSessionId: 'expected-session' })).rejects.toThrow('different span')
    expect(resolver.request({ spanId: 'span', agentSessionId: 'expected-session' })).toBeUndefined()
    expect(resolver.request({ spanId: 'span', agentSessionId: 'foreign-session' })).toBeUndefined()
  })

  it('keeps concurrent recovery separate for repeated tool IDs across sessions', async () => {
    const { resolver, fetchSpan } = fixture()
    fetchSpan.mockImplementation(async identity => [toolMessage(`${identity.agentSessionId}-request`, identity.agentSessionId === 'first' ? 1n : 2n, 'request', identity)])
    const first = { spanId: 'span', agentSessionId: 'first' }
    const second = { spanId: 'span', agentSessionId: 'second' }
    const releaseFirst = resolver.retainSpan(first)
    const releaseSecond = resolver.retainSpan(second)
    await Promise.all([resolver.loadSpan(first), resolver.loadSpan(second)])
    expect(fetchSpan).toHaveBeenCalledTimes(2)
    expect(resolver.request(first)?.message.id).toBe('first-request')
    expect(resolver.request(second)?.message.id).toBe('second-request')
    releaseFirst()
    expect(resolver.request(first)).toBeUndefined()
    expect(resolver.request(second)?.message.id).toBe('second-request')
    releaseSecond()
  })
  it.each(['span', 'related', 'message'] as const)('does not subscribe an async %s lookup to cache or transcript changes', async (method) => {
    const f = fixture()
    const request = toolMessage('request', 1n, 'request')
    const result = toolMessage('result', 2n, 'result')
    f.fetchSpan.mockResolvedValue([request, result])
    f.fetchMessage.mockResolvedValue(result)
    const calls: Promise<unknown>[] = []
    createRoot((dispose) => {
      cleanups.push(dispose)
      createEffect(() => {
        calls.push(method === 'span' ? f.resolver.loadSpan({ spanId: 'span', agentSessionId: '' }) : method === 'related' ? f.resolver.loadRelated(result) : f.resolver.message(2n))
      })
    })
    await Promise.all(calls)
    f.replaceMessages([request, result])
    await Promise.all(calls)
    expect(calls).toHaveLength(1)
  })

  it('distinguishes an absent message from a failed lookup and permits a retry', async () => {
    const { resolver, fetchMessage } = fixture()
    expect(await resolver.message(9n)).toBeUndefined()
    fetchMessage.mockRejectedValueOnce(new Error('offline'))
    await expect(resolver.message(9n)).rejects.toThrow('offline')
    fetchMessage.mockResolvedValueOnce(toolMessage('recovered', 9n, 'result'))
    expect((await resolver.message(9n))?.message.id).toBe('recovered')
    expect(fetchMessage).toHaveBeenCalledTimes(3)
    expect(fetchMessage).toHaveBeenLastCalledWith(9n, expect.any(AbortSignal))
  })

  it('rejects a response with another sequence', async () => {
    const { resolver, fetchMessage } = fixture()
    fetchMessage.mockResolvedValueOnce(toolMessage('wrong', 8n, 'result'))
    await expect(resolver.message(9n)).rejects.toThrow('different sequence')
  })

  it('rejects a late response after disposal and cancels its transport', async () => {
    const { resolver, fetchMessage, dispose } = fixture()
    let finish!: (message: AgentChatMessage) => void
    fetchMessage.mockImplementationOnce(() => new Promise(resolve => finish = resolve))
    const pending = resolver.message(9n)
    dispose()
    // The call was made above; `?.` is the type-level guard alone.
    expect(fetchMessage.mock.calls[0]?.[1].aborted).toBe(true)
    finish(toolMessage('late', 9n, 'result'))
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' })
  })

  // Disposal is the whole lifetime mechanism: the scope of a resolver is fixed
  // for its life, so nothing else ends one. See `MessageContextSources.scopeKey`.
  it('drops its cache and refuses every lease after disposal', async () => {
    const { resolver, fetchSpan, dispose } = fixture()
    fetchSpan.mockResolvedValue([toolMessage('request', 8n, 'request')])
    const releaseSpan = resolver.retainSpan({ spanId: 'span', agentSessionId: '' })
    const releaseRenderSpan = resolver.retainRenderSpan({ spanId: 'render-span', agentSessionId: '' })
    const releaseMessage = resolver.retainMessage(8n)
    await resolver.loadSpan({ spanId: 'span', agentSessionId: '' })
    expect(resolver.peek(8n)?.message.id).toBe('request')
    releaseRenderSpan()
    dispose()
    await waitForRenderPrune()
    expect(resolver.peek(8n)).toBeUndefined()
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })).toBeUndefined()
    expect(releaseSpan).not.toThrow()
    expect(releaseRenderSpan).not.toThrow()
    expect(releaseMessage).not.toThrow()
    expect(resolver.retainSpan({ spanId: 'span', agentSessionId: '' })).not.toThrow()
    expect(resolver.retainRenderSpan({ spanId: 'span', agentSessionId: '' })).not.toThrow()
    expect(resolver.retainMessage(8n)).not.toThrow()
    expect(resolver.peek(8n)).toBeUndefined()
  })

  it('does not send invalid sequences to the transport', async () => {
    const { resolver, fetchMessage } = fixture()
    expect(await resolver.message(0n)).toBeUndefined()
    expect(await resolver.message(-1n)).toBeUndefined()
    expect(fetchMessage).not.toHaveBeenCalled()
  })

  it('resolves resident requests and results without a network request', async () => {
    const request = toolMessage('request', 1n, 'request')
    const result = toolMessage('result', 2n, 'result')
    const { resolver, fetchSpan } = fixture([request, result])
    await resolver.loadSpan({ spanId: 'span', agentSessionId: '' })
    expect(fetchSpan).not.toHaveBeenCalled()
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })?.message.id).toBe('request')
    expect(resolver.result({ spanId: 'span', agentSessionId: '' })?.message.id).toBe('result')
  })

  it('loads a missing request and pairs messages regardless of response order', async () => {
    const request = toolMessage('request', 1n, 'request')
    const result = toolMessage('result', 2n, 'result')
    const { resolver, fetchSpan } = fixture([result])
    let finish!: (messages: AgentChatMessage[]) => void
    fetchSpan.mockImplementationOnce(() => new Promise(resolve => finish = resolve))
    const first = resolver.loadSpan({ spanId: 'span', agentSessionId: '' })
    const second = resolver.loadSpan({ spanId: 'span', agentSessionId: '' })
    expect(fetchSpan).toHaveBeenCalledTimes(1)
    finish([result, request])
    await Promise.all([first, second])
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })?.parsed.parentObject?.rawInput).toEqual({ filePath: '/project/file.ts' })
    expect(resolver.result({ spanId: 'span', agentSessionId: '' })?.message.id).toBe('result')
  })

  it('keeps a live supplement when an older lookup response arrives later', async () => {
    const result = toolMessage('result', 2n, 'result')
    const request = toolMessage('request', 1n, 'request')
    const { resolver, fetchSpan, emit } = fixture([result])
    let finish!: (messages: AgentChatMessage[]) => void
    fetchSpan.mockImplementationOnce(() => new Promise(resolve => finish = resolve))
    const loading = resolver.loadSpan({ spanId: 'span', agentSessionId: '' })
    emit(create(AgentChatMessageSchema, {
      ...request,
      supplementalRevision: 2n,
      supplementalContentCompression: ContentCompression.NONE,
      supplementalContent: new TextEncoder().encode(JSON.stringify({ provider: {
        sessionUpdate: 'tool_call',
        toolCallId: 'span',
        status: 'pending',
        rawInput: { filePath: '/project/recovered.ts' },
      } })),
    }))
    finish([request, result])
    await loading
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })?.message.supplementalRevision).toBe(2n)
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })?.parsed.parentObject?.rawInput).toEqual({ filePath: '/project/recovered.ts' })
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })?.original.parentObject?.rawInput).toEqual({ filePath: '/project/file.ts' })
  })

  it('keeps live task labels reactive', () => {
    const { resolver, setTodo } = fixture()
    createRoot((dispose) => {
      cleanups.push(dispose)
      const label = createMemo(() => resolver.todo('1')?.content)
      setTodo({ rowKey: '1', id: '1', content: 'First title', status: 'pending', activeForm: '' })
      expect(untrack(label)).toBe('First title')
      setTodo({ rowKey: '1', id: '1', content: 'Renamed title', status: 'pending', activeForm: '' })
      expect(untrack(label)).toBe('Renamed title')
    })
  })

  it('does not cache a transient lookup failure as a missing request', async () => {
    const { resolver, fetchSpan } = fixture([toolMessage('result', 2n, 'result')])
    fetchSpan.mockRejectedValueOnce(new Error('Connection lost'))
    await expect(resolver.loadSpan({ spanId: 'span', agentSessionId: '' })).rejects.toThrow('Connection lost')
    fetchSpan.mockResolvedValueOnce([toolMessage('request', 1n, 'request')])
    await resolver.loadSpan({ spanId: 'span', agentSessionId: '' })
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })?.message.id).toBe('request')
  })

  it('rejects an unrelated span without adding its data', async () => {
    const { resolver, fetchSpan } = fixture([toolMessage('result', 2n, 'result')])
    fetchSpan.mockResolvedValueOnce([toolMessage('foreign', 1n, 'request', { spanId: 'other-span' })])
    await expect(resolver.loadSpan({ spanId: 'span', agentSessionId: '' })).rejects.toThrow('different span')
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })).toBeUndefined()
  })

  it('shares message fetches for image and preview consumers', async () => {
    const { resolver, fetchMessage } = fixture()
    const original = toolMessage('result', 8n, 'result')
    fetchMessage.mockResolvedValueOnce(original)
    const [image, preview] = await Promise.all([resolver.message(8n), resolver.message(8n)])
    expect(fetchMessage).toHaveBeenCalledTimes(1)
    expect(image?.message.id).toBe('result')
    expect(preview?.message.id).toBe('result')
    expect(await resolver.message(0n)).toBeUndefined()
  })

  it('retains a sequence and rejects stale fetches and live enrichment', async () => {
    const { resolver, fetchMessage, emit } = fixture()
    const original = toolMessage('request', 8n, 'request')
    let finish!: (message: AgentChatMessage) => void
    fetchMessage.mockImplementationOnce(() => new Promise(resolve => finish = resolve))
    const release = resolver.retainMessage(8n)
    const pending = resolver.message(8n)
    const recovered = toolMessage('request', 8n, 'request', { supplementalRevision: 2n })
    emit(recovered)
    finish(original)
    expect((await pending)?.message).toBe(recovered)
    expect(resolver.peek(8n)?.message).toBe(recovered)
    emit(toolMessage('request', 8n, 'request', { supplementalRevision: 1n }))
    expect((await resolver.message(8n))?.message).toBe(recovered)
    expect(fetchMessage).toHaveBeenCalledTimes(1)
    release()
    expect(resolver.peek(8n)).toBeUndefined()
    emit(toolMessage('request', 8n, 'request', { supplementalRevision: 3n }))
    expect(resolver.peek(8n)).toBeUndefined()
  })

  it.each(['message', 'span'] as const)('keeps a live replacement when an older %s response arrives', async (method) => {
    const { resolver, fetchMessage, fetchSpan, emit } = fixture()
    const older = toolMessage('old-request', 8n, 'request')
    const replacement = toolMessage('new-request', 8n, 'request')
    let finish!: () => void
    fetchMessage.mockImplementationOnce(() => new Promise(resolve => finish = () => resolve(older)))
    fetchSpan.mockImplementationOnce(() => new Promise(resolve => finish = () => resolve([older])))
    const release = resolver.retainMessage(8n)
    const pending = method === 'message' ? resolver.message(8n) : resolver.loadSpan({ spanId: 'span', agentSessionId: '' })
    emit(replacement)
    finish()
    const resolved = await pending
    if (method === 'message')
      expect(resolved?.message).toBe(replacement)
    expect(resolver.peek(8n)?.message).toBe(replacement)
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })?.message).toBe(replacement)
    release()
  })

  it('keeps retained messages until the last consumer releases them', async () => {
    const { resolver, fetchMessage, replaceMessages } = fixture()
    fetchMessage.mockResolvedValueOnce(toolMessage('request', 8n, 'request'))
    const first = resolver.retainMessage(8n)
    const second = resolver.retainMessage(8n)
    await resolver.message(8n)
    replaceMessages([])
    first()
    first()
    expect(resolver.peek(8n)?.message.id).toBe('request')
    second()
    expect(resolver.peek(8n)).toBeUndefined()
  })

  it.each([0n, -1n])('does not retain an invalid sequence (%s)', (seq) => {
    const { resolver, emit } = fixture()
    const release = resolver.retainMessage(seq)
    emit(toolMessage('invalid', seq, 'request'))
    expect(resolver.peek(seq)).toBeUndefined()
    expect(release).not.toThrow()
  })

  it('releases fetched data when its window and leases no longer use the span', async () => {
    const { resolver, fetchSpan, replaceMessages } = fixture([toolMessage('result', 2n, 'result')])
    fetchSpan.mockResolvedValueOnce([toolMessage('request', 1n, 'request')])
    const release = resolver.retainSpan({ spanId: 'span', agentSessionId: '' })
    await resolver.loadSpan({ spanId: 'span', agentSessionId: '' })
    replaceMessages([])
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })).toBeDefined()
    release()
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })).toBeUndefined()
  })

  it('keeps fetched data through a same-turn span lease transfer', async () => {
    const identity = { spanId: 'span', agentSessionId: '' }
    const { resolver, fetchSpan, replaceMessages } = fixture([toolMessage('result', 2n, 'result')])
    fetchSpan.mockResolvedValueOnce([toolMessage('request', 1n, 'request')])
    const releaseFirst = resolver.retainRenderSpan(identity)
    await resolver.loadSpan(identity)
    replaceMessages([])

    releaseFirst()
    // Another consumer can prune the same span before Solid mounts the replacement
    // row. The render grace must protect the fetched request from that prune.
    const releaseImmediate = resolver.retainSpan(identity)
    releaseImmediate()
    await Promise.resolve()
    const releaseSecond = resolver.retainRenderSpan(identity)
    await waitForRenderPrune()
    expect(resolver.request(identity)).toBeDefined()

    releaseSecond()
    await waitForRenderPrune()
    expect(resolver.request(identity)).toBeUndefined()
  })

  it('walks the window for a membership change, and not for an in-place body replacement', () => {
    const store = createChatStore()
    const agentId = 'agent'
    store.setMessages(agentId, [toolMessage('request', 1n, 'request')])
    let walks = 0
    createRoot((dispose) => {
      cleanups.push(dispose)
      createMessageContextResolver({
        scopeKey: 'worker/agent',
        messages: () => {
          walks++
          return store.getMessages(agentId)
        },
        messageVersion: () => store.getMessageVersion(agentId),
        contentVersion: store.getMessageContentVersion,
        spanMessage: (identity, side) => store.getSpanMessage(agentId, identity, side),
        messageBySeq: seq => store.getLoadedMessageBySeq(agentId, seq),
        fetchSpan: async () => [],
        fetchMessage: async () => undefined,
        fetchFileImage: async () => { throw new Error('The image source is unavailable') },
        subscribe: observer => store.subscribeMessages(agentId, observer),
        todo: () => undefined,
        backgroundTask: () => undefined,
        progress: () => undefined,
      })
    })
    // The effects of a root flush after its body returns, so the first walk is
    // counted here rather than inside it.
    const initial = walks
    expect(initial).toBeGreaterThan(0)

    // A same-id same-seq re-delivery merges into the stored row in place. It
    // moves a field the walk READS (the span), and still no membership, so the
    // walk must not run again for it.
    expect(store.addMessage(agentId, toolMessage('request', 1n, 'request', { spanId: 'other-span' }))).toBe(true)
    expect(walks).toBe(initial)

    // A new row replaces the array, which is what prune must answer.
    expect(store.addMessage(agentId, toolMessage('result', 2n, 'result'))).toBe(true)
    expect(walks).toBe(initial + 1)
  })
})
