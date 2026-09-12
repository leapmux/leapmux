import type { MessageInitShape } from '@bufbuild/protobuf'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { TodoItem } from '~/stores/chatTodos'
import { create } from '@bufbuild/protobuf'
import { createEffect, createMemo, createRoot, createSignal, untrack } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { createSpanIndex } from '~/stores/chatSpanIndex'
import { createMessageContextResolver } from './messageContextResolver'
import './providers/opencode'

const cleanups: Array<() => void> = []
afterEach(() => cleanups.splice(0).forEach(dispose => dispose()))

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
    const [scope, setScope] = createSignal('worker/agent')
    const [version, setVersion] = createSignal(0)
    const [todo, setTodo] = createSignal<TodoItem>()
    const index = createSpanIndex()
    index.reindex('agent', initial)
    const observers = new Set<(message: AgentChatMessage) => void>()
    const fetchSpan = vi.fn(async (_spanId: string, _signal: AbortSignal): Promise<AgentChatMessage[]> => [])
    const fetchMessage = vi.fn(async (_seq: bigint, _signal: AbortSignal): Promise<AgentChatMessage | undefined> => undefined)
    const resolver = createMessageContextResolver({
      scopeKey: scope,
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
      setScope,
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
        calls.push(method === 'span' ? f.resolver.loadSpan('span') : method === 'related' ? f.resolver.loadRelated(result) : f.resolver.message(2n))
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
    expect(fetchMessage.mock.calls[0][1].aborted).toBe(true)
    finish(toolMessage('late', 9n, 'result'))
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' })
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
    await resolver.loadSpan('span')
    expect(fetchSpan).not.toHaveBeenCalled()
    expect(resolver.request('span')?.message.id).toBe('request')
    expect(resolver.result('span')?.message.id).toBe('result')
  })

  it('loads a missing request and pairs messages regardless of response order', async () => {
    const request = toolMessage('request', 1n, 'request')
    const result = toolMessage('result', 2n, 'result')
    const { resolver, fetchSpan } = fixture([result])
    let finish!: (messages: AgentChatMessage[]) => void
    fetchSpan.mockImplementationOnce(() => new Promise(resolve => finish = resolve))
    const first = resolver.loadSpan('span')
    const second = resolver.loadSpan('span')
    expect(fetchSpan).toHaveBeenCalledTimes(1)
    finish([result, request])
    await Promise.all([first, second])
    expect(resolver.request('span')?.parsed.parentObject?.rawInput).toEqual({ filePath: '/project/file.ts' })
    expect(resolver.result('span')?.message.id).toBe('result')
  })

  it('keeps a live supplement when an older lookup response arrives later', async () => {
    const result = toolMessage('result', 2n, 'result')
    const request = toolMessage('request', 1n, 'request')
    const { resolver, fetchSpan, emit } = fixture([result])
    let finish!: (messages: AgentChatMessage[]) => void
    fetchSpan.mockImplementationOnce(() => new Promise(resolve => finish = resolve))
    const loading = resolver.loadSpan('span')
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
    expect(resolver.request('span')?.message.supplementalRevision).toBe(2n)
    expect(resolver.request('span')?.parsed.parentObject?.rawInput).toEqual({ filePath: '/project/recovered.ts' })
    expect(resolver.request('span')?.original.parentObject?.rawInput).toEqual({ filePath: '/project/file.ts' })
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
    await expect(resolver.loadSpan('span')).rejects.toThrow('Connection lost')
    fetchSpan.mockResolvedValueOnce([toolMessage('request', 1n, 'request')])
    await resolver.loadSpan('span')
    expect(resolver.request('span')?.message.id).toBe('request')
  })

  it('rejects an unrelated span without adding its data', async () => {
    const { resolver, fetchSpan } = fixture([toolMessage('result', 2n, 'result')])
    fetchSpan.mockResolvedValueOnce([toolMessage('foreign', 1n, 'request', { spanId: 'other-span' })])
    await expect(resolver.loadSpan('span')).rejects.toThrow('different span')
    expect(resolver.request('span')).toBeUndefined()
  })

  it('ignores an outstanding response after the scope changes', async () => {
    const { resolver, fetchSpan, setScope } = fixture([toolMessage('result', 2n, 'result')])
    let finish!: (messages: AgentChatMessage[]) => void
    fetchSpan.mockImplementationOnce(() => new Promise(resolve => finish = resolve))
    const loading = resolver.loadSpan('span')
    setScope('other-worker/agent')
    finish([toolMessage('request', 1n, 'request')])
    await loading
    expect(resolver.request('span')).toBeUndefined()
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

  it('releases fetched data when its window and leases no longer use the span', async () => {
    const { resolver, fetchSpan, replaceMessages } = fixture([toolMessage('result', 2n, 'result')])
    fetchSpan.mockResolvedValueOnce([toolMessage('request', 1n, 'request')])
    const release = resolver.retainSpan('span')
    await resolver.loadSpan('span')
    replaceMessages([])
    expect(resolver.request('span')).toBeDefined()
    release()
    expect(resolver.request('span')).toBeUndefined()
  })
})
