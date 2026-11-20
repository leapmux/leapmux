import type { MessageInitShape } from '@bufbuild/protobuf'
import type { MessageContextSources } from './messageContextResolver'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { MessageSpanIdentity } from '~/lib/messageSpan'
import type { ChatWindowChange } from '~/stores/chat.store'
import { create } from '@bufbuild/protobuf'
import { batch, createEffect, createRenderEffect, createRoot, createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { createChatStore } from '~/stores/chat.store'
import { createSpanIndex } from '~/stores/chatSpanIndex'
import { createDrawnRequestRows } from '~/test-support/messageContext'
import { createMessageContextResolver, createMessageRenderSources } from './messageContextResolver'
import './providers/codex/plugin'
import './providers/opencode/plugin'

// Supply an explicit no-side role without a provider wire format.
// All other records use the actual registry and provider plugins.
vi.mock('~/components/chat/providers/registry', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/components/chat/providers/registry')>()
  return {
    ...actual,
    resolvedSpanRole: (parsed: ParsedMessageContent, provider: AgentProvider) =>
      parsed.parentObject?.noSpanSideForTest === true
        ? 'none'
        : actual.resolvedSpanRole(parsed, provider),
  }
})

const cleanups: Array<() => void> = []
afterEach(() => cleanups.splice(0).forEach(dispose => dispose()))

const waitForRenderPrune = () => new Promise<void>(resolve => setTimeout(resolve, 0))

function toolMessage(id: string, seq: bigint, side: 'request' | 'result', extra: MessageInitShape<typeof AgentChatMessageSchema> = {}): AgentChatMessage {
  const content = {
    sessionUpdate: side === 'request'
      ? 'tool_call'
      : 'tool_call_update',
    toolCallId: 'span',
    status: side === 'request'
      ? 'pending'
      : 'completed',
    kind: 'read',
    rawInput: side === 'request'
      ? { filePath: '/project/file.ts' }
      : undefined,
  }
  return create(AgentChatMessageSchema, {
    id,
    seq,
    spanId: 'span',
    agentProvider: AgentProvider.KILO,
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify(content)),
    contentCompression: ContentCompression.NONE,
    ...extra,
  })
}

function noSideMessage(id: string, seq: bigint, extra: MessageInitShape<typeof AgentChatMessageSchema> = {}): AgentChatMessage {
  return toolMessage(id, seq, 'request', {
    content: new TextEncoder().encode(JSON.stringify({ noSpanSideForTest: true })),
    ...extra,
  })
}

/** The sources that a case sets in place of the defaults of `fixture`. */
type FixtureSources = Partial<Pick<MessageContextSources, 'spanMessage' | 'fetchFileImage' | 'fetchFileImageSize' | 'holdsOldest' | 'holdsNewest'>>

function fixture(initial: AgentChatMessage[] = [], sources: FixtureSources = {}) {
  return createRoot((dispose) => {
    cleanups.push(dispose)
    const [messages, setMessages] = createSignal(initial)
    const [version, setVersion] = createSignal(0)
    const index = createSpanIndex()
    index.reindex('agent', initial)
    const observers = new Set<(message: AgentChatMessage) => void>()
    const windowObservers = new Set<(change: ChatWindowChange) => void>()
    const fetchSpan = vi.fn(async (_identity: MessageSpanIdentity, _signal: AbortSignal): Promise<AgentChatMessage[]> => [])
    const fetchMessage = vi.fn(async (_seq: bigint, _signal: AbortSignal): Promise<AgentChatMessage | undefined> => undefined)
    const resolver = createMessageContextResolver({
      scopeKey: 'worker/agent',
      messages,
      messageVersion: version,
      contentVersion: () => 0,
      spanMessage: sources.spanMessage ?? ((spanId, side) => side === 'request'
        ? index.getRequestMessage('agent', spanId)
        : index.getResultMessage('agent', spanId)),
      messageBySeq: seq => messages().find(message => message.seq === seq),
      fetchSpan,
      fetchMessage,
      fetchFileImage: sources.fetchFileImage ?? (async () => { throw new Error('The image source is unavailable') }),
      fetchFileImageSize: sources.fetchFileImageSize ?? (async () => undefined),
      subscribe: (observer) => {
        observers.add(observer)
        return () => observers.delete(observer)
      },
      subscribeWindowChanges: (observer) => {
        windowObservers.add(observer)
        return () => windowObservers.delete(observer)
      },
      backgroundTask: () => undefined,
      progress: () => undefined,
      // The window holds the whole transcript unless a case states another edge.
      holdsOldest: sources.holdsOldest ?? (() => true),
      holdsNewest: sources.holdsNewest ?? (() => true),
    })
    return {
      resolver,
      // The request rows the list draws over the window, as ChatView derives them.
      visibleRows: createDrawnRequestRows(resolver, messages),
      fetchSpan,
      fetchMessage,
      dispose,
      emit: (message: AgentChatMessage) => observers.forEach(observer => observer(message)),
      changeWindow: (change: ChatWindowChange) => windowObservers.forEach(observer => observer(change)),
      replaceMessages: (next: AgentChatMessage[]) => batch(() => {
        const ids = new Set(next.map(message => message.id))
        const dropped = messages().filter(message => !ids.has(message.id))
        if (dropped.length > 0)
          windowObservers.forEach(observer => observer({ kind: 'eviction', dropped, kept: next }))
        index.reindex('agent', next)
        setMessages(next)
        setVersion(value => value + 1)
      }),
    }
  })
}

function storeFixture(initial: AgentChatMessage[], fetchSpan: MessageContextSources['fetchSpan'] = async () => []) {
  const store = createChatStore()
  const agentId = 'agent'
  store.setMessages(agentId, initial)
  return createRoot((dispose) => {
    cleanups.push(dispose)
    const messages = () => store.getMessages(agentId)
    const resolver = createMessageContextResolver({
      scopeKey: 'worker/agent',
      messages,
      messageVersion: () => store.getMessageVersion(agentId),
      contentVersion: store.getMessageContentVersion,
      spanMessage: (identity, side) => store.getSpanMessage(agentId, identity, side),
      messageBySeq: seq => store.getLoadedMessageBySeq(agentId, seq),
      fetchSpan,
      fetchMessage: async () => undefined,
      fetchFileImage: async () => { throw new Error('The image source is unavailable') },
      fetchFileImageSize: async () => undefined,
      subscribe: observer => store.subscribeMessages(agentId, observer),
      subscribeWindowChanges: observer => store.subscribeWindowChanges(agentId, observer),
      retainContentVersion: id => store.retainMessageContentVersion(agentId, id),
      backgroundTask: () => undefined,
      progress: () => undefined,
      holdsOldest: () => !store.hasOlderMessages(agentId),
      holdsNewest: () => !store.hasNewerMessages(agentId),
    })
    const observe = <T,>(read: () => T): T[] => createRoot((disposeObserver) => {
      cleanups.push(disposeObserver)
      const observations: T[] = []
      createRenderEffect(() => {
        observations.push(read())
      })
      return observations
    })
    return {
      store,
      agentId,
      messages,
      resolver,
      observe,
    }
  })
}

describe('message context during history mutations', () => {
  const identity = { spanId: 'span', agentSessionId: '' }
  const request = toolMessage('request', 1n, 'request')
  const result = toolMessage('result', 2n, 'result')

  it('retains a request before reactive readers see its result alone after a trim', () => {
    const { store, agentId, messages, resolver, observe } = storeFixture([request, result])
    const seen = observe(() => {
      messages()
      return resolver.request(identity)?.revision
    })

    batch(() => store.trimOldestEnd(agentId, 1))

    expect(seen).not.toContain(undefined)
    for (const revision of seen)
      expect(revision).toEqual(seen[0])
    expect(store.getLoadedMessageBySeq(agentId, request.seq)).toBeUndefined()
  })

  it('discards a result retracted by the authoritative tail', () => {
    const { store, agentId, resolver } = storeFixture([request, result])
    expect(resolver.result(identity)?.message.id).toBe(result.id)

    store.reconcileAuthoritativeTail(agentId, request.seq)

    expect(store.getLoadedMessageBySeq(agentId, result.seq)).toBeUndefined()
    expect(resolver.result(identity)).toBeUndefined()
  })

  it('discards a result replaced at the same sequence by a different span', () => {
    const { store, agentId, resolver } = storeFixture([request, result])
    const replacement = toolMessage('replacement', result.seq, 'result', { spanId: 'other-span' })

    store.setMessages(agentId, [request, replacement])

    expect(store.getLoadedMessageBySeq(agentId, result.seq)?.id).toBe(replacement.id)
    expect(resolver.result(identity)).toBeUndefined()
  })

  it('keeps a late span response from restoring a retracted result', async () => {
    let respond!: (messages: AgentChatMessage[]) => void
    const { store, agentId, resolver } = storeFixture([result], () => new Promise(resolve => respond = resolve))
    const release = resolver.retainSpan(identity)
    try {
      const lookup = resolver.loadRequest(result)
      expect(lookup).toBeDefined()
      store.reconcileAuthoritativeTail(agentId, request.seq)
      respond([request, result])
      await lookup

      expect(resolver.result(identity)).toBeUndefined()
    }
    finally {
      release()
    }
  })

  it('preserves a revision on trim and re-admission before a later edit', () => {
    const { store, agentId, resolver } = storeFixture([request, result])
    const editedResult = toolMessage(result.id, result.seq, 'result', { spanLines: '[]' })
    store.addMessage(agentId, editedResult)
    const editedRevision = resolver.result(identity)?.revision
    expect(editedRevision?.contentVersion).toBe(1)

    store.trimNewestEnd(agentId, 1)
    expect.soft(resolver.result(identity)?.revision).toEqual(editedRevision)
    store.setMessages(agentId, [request, editedResult])
    expect.soft(resolver.result(identity)?.revision).toEqual(editedRevision)
    store.addMessage(agentId, toolMessage(result.id, result.seq, 'result', { spanLines: '[null]' }))
    expect(resolver.result(identity)?.revision.contentVersion).toBeGreaterThan(editedRevision!.contentVersion)
  })

  it('updates a mounted span lookup when a page supplies the missing side', () => {
    const { store, agentId, resolver, observe } = storeFixture([request])
    const seen = observe(() => resolver.result(identity)?.message.id)
    expect(seen).toEqual([undefined])

    store.setMessages(agentId, [request, result])

    expect(seen.at(-1)).toBe(result.id)
  })

  it('keeps a live completion for a rendered request when the store refuses the new row', () => {
    const filler = toolMessage('filler', 2n, 'request', { spanId: 'unrelated' })
    const completion = toolMessage('completion', 3n, 'result')
    const { store, agentId, resolver } = storeFixture([request, filler])
    const release = resolver.retainRenderSpan(identity)
    try {
      store.trimNewestEnd(agentId, 1)
      expect(store.addMessage(agentId, completion)).toBe(false)

      expect(resolver.result(identity)?.message.id).toBe(completion.id)
    }
    finally {
      release()
    }
  })
})

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
    fetchSpan.mockResolvedValue([
      toolMessage('foreign', 1n, 'request', { agentSessionId: 'foreign-session' }),
    ])
    await expect(resolver.loadSpan({ spanId: 'span', agentSessionId: 'expected-session' })).rejects.toThrow('different span')
    expect(resolver.request({ spanId: 'span', agentSessionId: 'expected-session' })).toBeUndefined()
    expect(resolver.request({ spanId: 'span', agentSessionId: 'foreign-session' })).toBeUndefined()
  })

  it('keeps concurrent recovery separate for repeated tool IDs across sessions', async () => {
    const { resolver, fetchSpan } = fixture()
    fetchSpan.mockImplementation(async identity => [
      toolMessage(`${identity.agentSessionId}-request`, identity.agentSessionId === 'first'
        ? 1n
        : 2n, 'request', identity),
    ])
    const first = { spanId: 'span', agentSessionId: 'first' }
    const second = { spanId: 'span', agentSessionId: 'second' }
    const releaseFirst = resolver.retainSpan(first)
    const releaseSecond = resolver.retainSpan(second)
    await Promise.all([
      resolver.loadSpan(first),
      resolver.loadSpan(second),
    ])
    expect(fetchSpan).toHaveBeenCalledTimes(2)
    expect(resolver.request(first)?.message.id).toBe('first-request')
    expect(resolver.request(second)?.message.id).toBe('second-request')
    releaseFirst()
    expect(resolver.request(first)).toBeUndefined()
    expect(resolver.request(second)?.message.id).toBe('second-request')
    releaseSecond()
  })
  it.each(['span', 'request', 'message'] as const)('does not subscribe an async %s lookup to cache or transcript changes', async (method) => {
    const f = fixture()
    const request = toolMessage('request', 1n, 'request')
    const result = toolMessage('result', 2n, 'result')
    f.fetchSpan.mockResolvedValue([request, result])
    f.fetchMessage.mockResolvedValue(result)
    const calls: Promise<unknown>[] = []
    createRoot((dispose) => {
      cleanups.push(dispose)
      createEffect(() => {
        calls.push(method === 'span'
          ? f.resolver.loadSpan({ spanId: 'span', agentSessionId: '' })
          : method === 'request'
            ? Promise.resolve(f.resolver.loadRequest(result))
            : f.resolver.message(2n))
      })
    })
    await Promise.all(calls)
    f.replaceMessages([request, result])
    await Promise.all(calls)
    expect(calls).toHaveLength(1)
  })

  describe('loadRequest', () => {
    it('returns no fetch when the browser holds the request', () => {
      const request = toolMessage('request', 1n, 'request')
      const result = toolMessage('result', 2n, 'result')
      const { resolver, fetchSpan } = fixture([request, result])

      expect(resolver.loadRequest(result)).toBeUndefined()
      expect(fetchSpan).not.toHaveBeenCalled()
    })

    it('returns the fetch of a missing request in the same task, and shares it with a second caller', async () => {
      const request = toolMessage('request', 1n, 'request')
      const result = toolMessage('result', 2n, 'result')
      const { resolver, fetchSpan } = fixture([result])
      let finish!: (messages: AgentChatMessage[]) => void
      fetchSpan.mockImplementationOnce(() => new Promise(resolve => finish = resolve))

      const first = resolver.loadRequest(result)
      const second = resolver.loadRequest(result)

      expect(first).toBeInstanceOf(Promise)
      expect(second).toBeInstanceOf(Promise)
      expect(fetchSpan).toHaveBeenCalledTimes(1)
      finish([request, result])
      await Promise.all([first, second])
      expect(resolver.request({ spanId: 'span', agentSessionId: '' })?.message.id).toBe('request')
      // The request arrived, so the row needs no further fetch.
      expect(resolver.loadRequest(result)).toBeUndefined()
      expect(fetchSpan).toHaveBeenCalledTimes(1)
    })

    it('returns no fetch once a fetch of the span settled without the request', async () => {
      const result = toolMessage('result', 2n, 'result')
      const { resolver, fetchSpan } = fixture([result])
      // The worker holds no request for the span, as for a request that a history trim removed.
      fetchSpan.mockResolvedValueOnce([result])

      await resolver.loadRequest(result)

      expect(resolver.request({ spanId: 'span', agentSessionId: '' })).toBeUndefined()
      expect(resolver.loadRequest(result)).toBeUndefined()
      expect(fetchSpan).toHaveBeenCalledTimes(1)
    })

    it('returns no fetch for a row that needs no request', () => {
      const lone = noSideMessage('progress', 1n)
      const { resolver, fetchSpan } = fixture([lone])

      expect(resolver.loadRequest(lone)).toBeUndefined()
      expect(fetchSpan).not.toHaveBeenCalled()
    })

    // No row reads a later message of its span, so a request row never waits for its
    // result: not while the call runs, and not when the result lies in a newer page.
    it('never fetches for a request row, whatever the window holds', () => {
      const request = toolMessage('request', 1n, 'request', {
        content: new TextEncoder().encode(JSON.stringify({
          sessionUpdate: 'tool_call',
          toolCallId: 'span',
          status: 'pending',
          kind: 'read',
          rawInput: {},
        })),
      })
      const inWindow = fixture([request])
      const outsideWindow = fixture([])

      expect(inWindow.resolver.loadRequest(request)).toBeUndefined()
      expect(outsideWindow.resolver.loadRequest(request)).toBeUndefined()
      expect(inWindow.fetchSpan).not.toHaveBeenCalled()
      expect(outsideWindow.fetchSpan).not.toHaveBeenCalled()
    })

    it('rejects the fetch when it fails, and fetches again at the next call', async () => {
      const result = toolMessage('result', 2n, 'result')
      const { resolver, fetchSpan } = fixture([result])
      fetchSpan.mockRejectedValueOnce(new Error('offline'))

      await expect(resolver.loadRequest(result)).rejects.toThrow('offline')

      // A failed fetch settled nothing, so the next call asks again.
      expect(resolver.loadRequest(result)).toBeInstanceOf(Promise)
      expect(fetchSpan).toHaveBeenCalledTimes(2)
    })

    // A Codex command row draws everything from its own frame, so its plugin asks for no
    // request. Its result row still draws its own header and command only while the
    // transcript draws no request row beside it, and only a fetch tells whether a request
    // outside the window exists. So the row waits for that fetch before it shows.
    describe('for a result row that draws nothing from its request', () => {
      const identity = { spanId: 'exec-1', agentSessionId: '' }
      const command = (id: string, seq: bigint, status: 'inProgress' | 'completed') => create(AgentChatMessageSchema, {
        id,
        seq,
        spanId: identity.spanId,
        spanType: 'commandExecution',
        agentProvider: AgentProvider.CODEX,
        source: MessageSource.AGENT,
        content: new TextEncoder().encode(JSON.stringify({
          item: {
            type: 'commandExecution',
            id: identity.spanId,
            command: '/bin/zsh -lc \'task test\'',
            cwd: '/project',
            status,
            commandActions: [
              { type: 'unknown', command: 'task test' },
            ],
            aggregatedOutput: status === 'completed'
              ? 'ok'
              : null,
            exitCode: status === 'completed'
              ? 0
              : null,
          },
          threadId: 'thread-1',
          turnId: 'turn-1',
        })),
        contentCompression: ContentCompression.NONE,
      })
      const request = command('exec-start', 1n, 'inProgress')
      const result = command('exec-done', 2n, 'completed')

      it('returns the fetch of the span while the browser does not hold the request', async () => {
        const { resolver, fetchSpan, visibleRows } = fixture([result])
        expect(resolver.role(result)).toBe('result')
        fetchSpan.mockResolvedValueOnce([request, result])
        const release = resolver.retainSpan(identity)

        const fetch = resolver.loadRequest(result)

        expect(fetch).toBeInstanceOf(Promise)
        expect(fetchSpan).toHaveBeenCalledOnce()
        await fetch
        // The answer that the row draws from is known before the row shows.
        expect(visibleRows(identity)).toEqual({ request: true })
        expect(resolver.loadRequest(result)).toBeUndefined()
        release()
      })

      it('returns no fetch while the window holds the request', () => {
        const { resolver, fetchSpan } = fixture([request, result])

        expect(resolver.loadRequest(result)).toBeUndefined()
        expect(fetchSpan).not.toHaveBeenCalled()
      })

      it('returns no fetch for the request row', () => {
        const { resolver, fetchSpan } = fixture([request])

        expect(resolver.loadRequest(request)).toBeUndefined()
        expect(fetchSpan).not.toHaveBeenCalled()
      })
    })

    it('returns no fetch after the resolver is disposed', () => {
      const result = toolMessage('result', 2n, 'result')
      const { resolver, fetchSpan, dispose } = fixture([result])
      dispose()

      expect(resolver.loadRequest(result)).toBeUndefined()
      expect(fetchSpan).not.toHaveBeenCalled()
    })

    // The result of a running call arrives on the live path, and the header of the
    // request row reads it from the window.
    it('reads the result of a span when it arrives in the window', () => {
      const identity = { spanId: 'span', agentSessionId: '' }
      const request = toolMessage('request', 1n, 'request')
      const result = toolMessage('result', 2n, 'result')
      const { resolver, replaceMessages } = fixture([request])
      expect(resolver.result(identity)).toBeUndefined()

      replaceMessages([request, result])

      expect(resolver.result(identity)?.message.id).toBe('result')
    })
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
    fetchSpan.mockResolvedValue([
      toolMessage('request', 8n, 'request'),
    ])
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

  // No row reads its result, so a span whose request the browser holds has nothing to
  // fetch, also while its result has not arrived.
  it('fetches nothing for a span whose request the browser holds', async () => {
    const request = toolMessage('request', 1n, 'request')
    const { resolver, fetchSpan } = fixture([request])
    await resolver.loadSpan({ spanId: 'span', agentSessionId: '' })
    expect(fetchSpan).not.toHaveBeenCalled()
    expect(resolver.result({ spanId: 'span', agentSessionId: '' })).toBeUndefined()
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
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })?.resolved.parentObject?.rawInput).toEqual({ filePath: '/project/file.ts' })
    expect(resolver.result({ spanId: 'span', agentSessionId: '' })?.message.id).toBe('result')
  })

  // The result row must draw the same thing before and after the page of its request loads:
  // it shrank by its header and its request side in view when that page arrived.
  it('counts the request row of a fetched request that the window does not hold', async () => {
    const request = toolMessage('request', 1n, 'request')
    const result = toolMessage('result', 2n, 'result')
    const { resolver, visibleRows, fetchSpan, replaceMessages } = fixture([result])
    const identity = { spanId: 'span', agentSessionId: '' }
    expect(visibleRows(identity)).toEqual({ request: false })
    fetchSpan.mockResolvedValue([request, result])
    const release = resolver.retainSpan(identity)
    await resolver.loadSpan(identity)
    expect(resolver.request(identity)?.message.id).toBe('request')
    expect(visibleRows(identity)).toEqual({ request: true })
    expect(resolver.role(result)).toBe('result')
    // The page of the request arrives: the answer stays.
    replaceMessages([request, result])
    expect(visibleRows(identity)).toEqual({ request: true })
    release()
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
      supplementalContent: new TextEncoder().encode(JSON.stringify({
        provider: {
          sessionUpdate: 'tool_call',
          toolCallId: 'span',
          status: 'pending',
          rawInput: { filePath: '/project/recovered.ts' },
        },
      })),
    }))
    finish([request, result])
    await loading
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })?.message.supplementalRevision).toBe(2n)
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })?.resolved.parentObject?.rawInput).toEqual({ filePath: '/project/recovered.ts' })
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })?.original.parentObject?.rawInput).toEqual({ filePath: '/project/file.ts' })
  })

  it('does not cache a transient lookup failure as a missing request', async () => {
    const { resolver, fetchSpan } = fixture([
      toolMessage('result', 2n, 'result'),
    ])
    fetchSpan.mockRejectedValueOnce(new Error('Connection lost'))
    await expect(resolver.loadSpan({ spanId: 'span', agentSessionId: '' })).rejects.toThrow('Connection lost')
    fetchSpan.mockResolvedValueOnce([
      toolMessage('request', 1n, 'request'),
    ])
    await resolver.loadSpan({ spanId: 'span', agentSessionId: '' })
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })?.message.id).toBe('request')
  })

  it('rejects an unrelated span without adding its data', async () => {
    const { resolver, fetchSpan } = fixture([
      toolMessage('result', 2n, 'result'),
    ])
    fetchSpan.mockResolvedValueOnce([
      toolMessage('foreign', 1n, 'request', { spanId: 'other-span' }),
    ])
    await expect(resolver.loadSpan({ spanId: 'span', agentSessionId: '' })).rejects.toThrow('different span')
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })).toBeUndefined()
  })

  it('shares message fetches for image and preview consumers', async () => {
    const { resolver, fetchMessage } = fixture()
    const original = toolMessage('result', 8n, 'result')
    fetchMessage.mockResolvedValueOnce(original)
    const [image, preview] = await Promise.all([
      resolver.message(8n),
      resolver.message(8n),
    ])
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
    const pending = method === 'message'
      ? resolver.message(8n)
      : resolver.loadSpan({ spanId: 'span', agentSessionId: '' })
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
    const { resolver, fetchSpan, replaceMessages } = fixture([
      toolMessage('result', 2n, 'result'),
    ])
    fetchSpan.mockResolvedValueOnce([
      toolMessage('request', 1n, 'request'),
    ])
    const release = resolver.retainSpan({ spanId: 'span', agentSessionId: '' })
    await resolver.loadSpan({ spanId: 'span', agentSessionId: '' })
    replaceMessages([])
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })).toBeDefined()
    release()
    expect(resolver.request({ spanId: 'span', agentSessionId: '' })).toBeUndefined()
  })

  it('discards off-window span and point replies that predate an exact retraction', async () => {
    const opener = toolMessage('opener', 1n, 'request')
    const result = toolMessage('result', 2n, 'result')
    const f = fixture([opener], {
      holdsNewest: () => false,
    })
    const { resolver, fetchSpan, fetchMessage } = f
    const releaseSpan = resolver.retainSpan(opener)
    const releasePoint = resolver.retainMessage(result.seq)
    let finishSpan!: (rows: AgentChatMessage[]) => void
    let finishPoint!: (row: AgentChatMessage) => void
    fetchSpan.mockImplementationOnce(() => new Promise(resolve => finishSpan = resolve))
    fetchMessage.mockImplementationOnce(() => new Promise(resolve => finishPoint = resolve))
    const span = resolver.loadResult(opener)
    const point = resolver.message(result.seq)

    f.changeWindow({
      kind: 'retraction',
      includes: message => message.seq === result.seq,
    })
    finishSpan([opener, result])
    finishPoint(result)
    await span

    expect(await point).toBeUndefined()
    expect(resolver.peek(result.seq)).toBeUndefined()
    expect(resolver.result(opener)).toBeUndefined()
    expect(resolver.request(opener)?.message.id).toBe(opener.id)
    releasePoint()
    releaseSpan()
  })

  it('revalidates off-window span and point rows when the full watch is promoted', async () => {
    const opener = toolMessage('request', 1n, 'request')
    const result = toolMessage('result', 2n, 'result')
    const point = toolMessage('point', 3n, 'result', { spanId: 'other-span' })
    const f = fixture([opener], {
      holdsNewest: () => false,
    })
    const { resolver, fetchMessage, fetchSpan } = f
    const releaseSpan = resolver.retainSpan(opener)
    const releasePoint = resolver.retainMessage(point.seq)
    fetchSpan.mockResolvedValueOnce([opener, result]).mockResolvedValueOnce([opener])
    fetchMessage.mockResolvedValue(point)
    await resolver.loadResult(opener)
    await resolver.message(point.seq)
    expect(resolver.result(opener)?.message.id).toBe(result.id)
    expect(resolver.peek(point.seq)?.message.id).toBe(point.id)

    const watch = new AbortController()
    f.changeWindow({ kind: 'promotion', signal: watch.signal })
    await vi.waitFor(() => {
      expect(resolver.result(opener)).toBeUndefined()
      expect(resolver.peek(point.seq)?.message.id).toBe(point.id)
    })
    expect(fetchSpan).toHaveBeenCalledTimes(2)
    expect(fetchMessage).toHaveBeenCalledTimes(2)
    expect(resolver.request(opener)?.message.id).toBe(opener.id)
    releasePoint()
    releaseSpan()
  })

  it.each(['aborted', 'empty'] as const)('reloads active span demand after a previous FULL lookup settled %s', async (outcome) => {
    const opener = toolMessage('request', 1n, 'request')
    const oldResult = toolMessage('old-result', 2n, 'result')
    const currentResult = toolMessage('current-result', 3n, 'result')
    const f = fixture([opener], {
      holdsNewest: () => false,
    })
    const { resolver, fetchSpan } = f
    const release = resolver.retainRenderSpan(opener)
    let finishPrevious!: (rows: AgentChatMessage[]) => void
    fetchSpan
      .mockResolvedValueOnce([opener, oldResult])
      .mockImplementationOnce(() => new Promise(resolve => finishPrevious = resolve))
      .mockResolvedValueOnce([opener, currentResult])
    await resolver.loadResult(opener)
    expect(resolver.result(opener)?.message.id).toBe(oldResult.id)

    const previous = new AbortController()
    f.changeWindow({ kind: 'promotion', signal: previous.signal })
    const previousLookup = resolver.loadResult(opener)
    if (outcome === 'aborted')
      previous.abort()
    finishPrevious(outcome === 'empty'
      ? []
      : [oldResult])
    await previousLookup
    expect(resolver.result(opener)).toBeUndefined()
    expect(fetchSpan).toHaveBeenCalledTimes(2)

    f.changeWindow({ kind: 'promotion', signal: new AbortController().signal })
    expect(fetchSpan).toHaveBeenCalledTimes(3)
    await vi.waitFor(() => expect(resolver.result(opener)?.message.id).toBe(currentResult.id))
    release()
  })

  it('drops empty span lookup demand after the last view releases its lease', async () => {
    const opener = toolMessage('request', 1n, 'request')
    const f = fixture([opener], {
      holdsNewest: () => false,
    })
    const releaseFirst = f.resolver.retainSpan(opener)
    const releaseLast = f.resolver.retainRenderSpan(opener)
    await f.resolver.loadResult(opener)
    releaseFirst()
    releaseLast()
    await waitForRenderPrune()

    f.changeWindow({ kind: 'promotion', signal: new AbortController().signal })

    expect(f.fetchSpan).toHaveBeenCalledOnce()
  })

  it('keeps fetched data through a same-turn span lease transfer', async () => {
    const identity = { spanId: 'span', agentSessionId: '' }
    const { resolver, fetchSpan, replaceMessages } = fixture([
      toolMessage('result', 2n, 'result'),
    ])
    fetchSpan.mockResolvedValueOnce([
      toolMessage('request', 1n, 'request'),
    ])
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
    store.setMessages(agentId, [
      toolMessage('request', 1n, 'request'),
    ])
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
        fetchFileImageSize: async () => undefined,
        subscribe: observer => store.subscribeMessages(agentId, observer),
        subscribeWindowChanges: observer => store.subscribeWindowChanges(agentId, observer),
        retainContentVersion: id => store.retainMessageContentVersion(agentId, id),
        backgroundTask: () => undefined,
        progress: () => undefined,
        holdsOldest: () => !store.hasOlderMessages(agentId),
        holdsNewest: () => !store.hasNewerMessages(agentId),
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

    // A new row replaces the array, which is what each walk must answer: `prune` and the
    // set of spans whose request row is listed each walk once more, as they did at the start.
    expect(store.addMessage(agentId, toolMessage('result', 2n, 'result'))).toBe(true)
    expect(walks).toBe(initial * 2)
  })
})

describe('createMessageRenderSources', () => {
  const SIZE = { width: 400, height: 200 }

  it('reads the size of an image for its own row, and keeps it for that row only', async () => {
    const row = toolMessage('row', 1n, 'request')
    const fetchSize = vi.fn<MessageContextSources['fetchFileImageSize']>(async () => SIZE)
    const { resolver } = fixture([row], { fetchFileImageSize: fetchSize })
    const sources = createMessageRenderSources(() => resolver, () => row, () => resolver.resolvedMessage(row).resolved)
    expect(sources.fileImages.peekSize('/a.png')).toEqual({ kind: 'unknown' })

    await expect(sources.fileImages.loadSize('/a.png')).resolves.toEqual(SIZE)

    expect(fetchSize).toHaveBeenCalledExactlyOnceWith('/a.png', expect.any(AbortSignal))
    expect(sources.fileImages.peekSize('/a.png')).toEqual({ kind: 'known', size: SIZE })
    // The file can differ at the time of another tool call, so another row reads its own size.
    expect(resolver.cachedFileImageSize('/a.png')).toEqual({ kind: 'unknown' })
    expect(resolver.cachedFileImageSize('/a.png', 'other-row')).toEqual({ kind: 'unknown' })
    expect(resolver.cachedFileImageSize('/a.png', row.id)).toEqual({ kind: 'known', size: SIZE })
  })

  it('reads an image for its own row, and holds it for that row only', async () => {
    const row = toolMessage('row', 1n, 'request')
    const image = { data: 'AAAA', mimeType: 'image/png' }
    const fetchImage = vi.fn<MessageContextSources['fetchFileImage']>(async () => image)
    const { resolver } = fixture([row], { fetchFileImage: fetchImage })
    const sources = createMessageRenderSources(() => resolver, () => row, () => resolver.resolvedMessage(row).resolved)
    expect(sources.fileImages.peek('/a.png')).toBeUndefined()

    await expect(sources.fileImages.load('/a.png')).resolves.toEqual(image)

    expect(fetchImage).toHaveBeenCalledExactlyOnceWith('/a.png', expect.any(AbortSignal))
    expect(sources.fileImages.peek('/a.png')).toEqual(image)
    expect(resolver.cachedFileImage('/a.png')).toBeUndefined()
    expect(resolver.cachedFileImage('/a.png', row.id)).toEqual(image)
  })

  it('reads the image again past the cache for a refresh', async () => {
    const row = toolMessage('row', 1n, 'request')
    const fetchImage = vi.fn<MessageContextSources['fetchFileImage']>(async () => ({ data: 'AAAA', mimeType: 'image/png' }))
    const { resolver } = fixture([row], { fetchFileImage: fetchImage })
    const sources = createMessageRenderSources(() => resolver, () => row, () => resolver.resolvedMessage(row).resolved)
    await sources.fileImages.load('/a.png')

    await sources.fileImages.load('/a.png', { refresh: true })

    expect(fetchImage).toHaveBeenCalledTimes(2)
  })

  it('answers as NO_FILE_IMAGES with no resolver, so that a view waits for no read', async () => {
    const row = toolMessage('row', 1n, 'request')
    const sources = createMessageRenderSources(() => undefined, () => row, () => undefined as never)

    await expect(sources.fileImages.loadSize('/a.png')).resolves.toBeUndefined()
    expect(sources.fileImages.peekSize('/a.png')).toEqual({ kind: 'none' })
    expect(sources.fileImages.peek('/a.png')).toBeUndefined()
    await expect(sources.fileImages.load('/a.png')).rejects.toThrow('The image source is unavailable')
  })

  it('reads no size after the resolver is disposed', async () => {
    const row = toolMessage('row', 1n, 'request')
    const fetchSize = vi.fn<MessageContextSources['fetchFileImageSize']>(async () => SIZE)
    const { resolver, dispose } = fixture([row], { fetchFileImageSize: fetchSize })
    await resolver.fileImageSize('/a.png')
    fetchSize.mockClear()

    dispose()

    // The sizes go with the resolver, and it starts no read for a view that still asks.
    expect(resolver.cachedFileImageSize('/a.png')).toEqual({ kind: 'unknown' })
    await expect(resolver.fileImageSize('/b.png')).resolves.toBeUndefined()
    expect(fetchSize).not.toHaveBeenCalled()
  })
})

// Codex hides the empty start frame of a web search, and the completed frame closes the
// span. The hidden request draws no row, so the row of the completed search must draw its
// own header and request side: no request row stands beside it.
describe('visibleRows of a hidden request message', () => {
  const identity = { spanId: 'ws-1', agentSessionId: '' }
  const webSearch = (id: string, seq: bigint, item: Record<string, unknown>, closing: boolean) => create(AgentChatMessageSchema, {
    id,
    seq,
    spanId: 'ws-1',
    spanType: 'webSearch',
    agentProvider: AgentProvider.CODEX,
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify({
      item: { type: 'webSearch', id: 'ws-1', ...item },
      threadId: 'thread-1',
      turnId: 'turn-1',
    })),
    contentCompression: ContentCompression.NONE,
    spanLines: closing
      ? JSON.stringify([
          { span_id: 'ws-1', color: 1, type: 'connector_end' },
        ])
      : '[]',
  })
  const start = webSearch('ws-start', 1n, {
    query: '',
    action: { type: 'other' },
  }, false)
  const done = webSearch('ws-done', 2n, {
    query: 'codex app server',
    action: { type: 'search', query: 'codex app server' },
  }, true)

  it('counts no request row for a request message that draws none', () => {
    const { resolver, visibleRows } = fixture([start, done])
    expect(resolver.role(start)).toBe('request')
    expect(visibleRows(identity)).toEqual({ request: false })
  })

  it.each(['older', 'newer'] as const)('preserves positional roles when trimming the %s half of a span', (side) => {
    const { store, agentId, resolver } = storeFixture([start, done])

    if (side === 'older')
      store.trimOldestEnd(agentId, 1)
    else
      store.trimNewestEnd(agentId, 1)

    expect(resolver.request(identity)?.message.id).toBe(start.id)
    expect(resolver.result(identity)?.message.id).toBe(done.id)
    expect(resolver.role(done)).toBe('result')
  })

  it('counts the request row of a request message that draws one', () => {
    const shown = webSearch('ws-start', 1n, {
      query: 'codex',
      action: { type: 'search', query: 'codex' },
    }, false)
    const { resolver, visibleRows } = fixture([shown, done])
    expect(resolver.role(shown)).toBe('request')
    expect(visibleRows(identity)).toEqual({ request: true })
  })
})

// Neither frame of a Codex web search states its side, so the window pairs them by their
// place in it. With the start in an older page, the completed frame is the first frame of
// its span in the window, and the window takes it as the request: it drew as the request
// row until the older page loaded, and then as the result row.
describe('a span whose frames state no side', () => {
  const identity = { spanId: 'ws-1', agentSessionId: '' }
  const webSearch = (id: string, seq: bigint, item: Record<string, unknown>) => create(AgentChatMessageSchema, {
    id,
    seq,
    spanId: 'ws-1',
    spanType: 'webSearch',
    agentProvider: AgentProvider.CODEX,
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify({
      item: { type: 'webSearch', id: 'ws-1', ...item },
      threadId: 'thread-1',
      turnId: 'turn-1',
    })),
    contentCompression: ContentCompression.NONE,
  })
  const start = webSearch('ws-start', 1n, {
    query: 'codex',
    action: { type: 'search', query: 'codex' },
  })
  const done = webSearch('ws-done', 2n, {
    query: 'codex app server',
    action: { type: 'search', query: 'codex app server' },
  })

  it('fetches the span of the frame that the window pairs as the request while older pages remain, and pairs it by the whole span', async () => {
    const { resolver, fetchSpan, replaceMessages } = fixture([done], {
      holdsOldest: () => false,
    })
    expect(resolver.role(done)).toBe('request')
    fetchSpan.mockResolvedValueOnce([start, done])
    const release = resolver.retainSpan(identity)

    const fetch = resolver.loadRequest(done)

    expect(fetch).toBeInstanceOf(Promise)
    await fetch
    expect(resolver.request(identity)?.message.id).toBe('ws-start')
    expect(resolver.role(done)).toBe('result')
    // The span is settled: no second fetch.
    expect(resolver.loadRequest(done)).toBeUndefined()
    // The page of the start arrives: the pairing stays.
    replaceMessages([start, done])
    expect(resolver.request(identity)?.message.id).toBe('ws-start')
    expect(resolver.role(done)).toBe('result')
    expect(fetchSpan).toHaveBeenCalledOnce()
    release()
  })

  it('fetches nothing for the first frame of the window while the window starts at the transcript', () => {
    const { resolver, fetchSpan } = fixture([done], {
      holdsOldest: () => true,
    })
    expect(resolver.loadRequest(done)).toBeUndefined()
    expect(fetchSpan).not.toHaveBeenCalled()
  })

  it('fetches nothing for a later frame of the window, whose request the window holds', () => {
    const { resolver, fetchSpan } = fixture([start, done], {
      holdsOldest: () => false,
    })
    expect(resolver.role(done)).toBe('result')
    expect(resolver.loadRequest(done)).toBeUndefined()
    expect(fetchSpan).not.toHaveBeenCalled()
  })
})

// A trim of the window removes one side of a span while the other side stays. The resolver
// keeps the side that left, so no row of the span changes when the window moves.
describe('a span that the window holds in part', () => {
  const identity = { spanId: 'span', agentSessionId: '' }

  it('keeps a request that left the window while its result stays, and drops it once the whole span left', async () => {
    const request = toolMessage('request', 1n, 'request')
    const result = toolMessage('result', 2n, 'result')
    const { resolver, visibleRows, replaceMessages } = fixture([request, result])
    expect(visibleRows(identity)).toEqual({ request: true })

    replaceMessages([result])

    expect(resolver.request(identity)?.message.id).toBe('request')
    expect(visibleRows(identity)).toEqual({ request: true })
    expect(resolver.loadRequest(result)).toBeUndefined()

    replaceMessages([])
    await waitForRenderPrune()
    expect(resolver.request(identity)).toBeUndefined()
  })

  it('keeps a result that left the window while its request stays', () => {
    const request = toolMessage('request', 1n, 'request')
    const result = toolMessage('result', 2n, 'result')
    const { resolver, replaceMessages } = fixture([request, result])

    replaceMessages([request])

    expect(resolver.result(identity)?.message.id).toBe('result')
  })

  it('keeps no message that left with the rest of its span', () => {
    const request = toolMessage('request', 1n, 'request')
    const other = toolMessage('other', 3n, 'request', { spanId: 'other-span' })
    const { resolver, replaceMessages } = fixture([request, other])

    replaceMessages([other])

    expect(resolver.request(identity)).toBeUndefined()
  })
})

describe('loadResult', () => {
  const identity = { spanId: 'span', agentSessionId: '' }
  const request = toolMessage('request', 1n, 'request')
  const result = toolMessage('result', 2n, 'result')

  it('returns the fetch of the span while the window holds no result and newer pages remain, and shares it', async () => {
    const { resolver, fetchSpan } = fixture([request], {
      holdsNewest: () => false,
    })
    let finish!: (messages: AgentChatMessage[]) => void
    fetchSpan.mockImplementationOnce(() => new Promise(resolve => finish = resolve))
    const release = resolver.retainSpan(identity)

    const first = resolver.loadResult(identity)
    const second = resolver.loadResult(identity)

    expect(first).toBeInstanceOf(Promise)
    expect(second).toBeInstanceOf(Promise)
    expect(fetchSpan).toHaveBeenCalledOnce()
    finish([request, result])
    await Promise.all([first, second])
    expect(resolver.result(identity)?.message.id).toBe('result')
    expect(resolver.loadResult(identity)).toBeUndefined()
    release()
  })

  // While the window holds the newest message, a span with no result in it has none yet:
  // the call runs, and its result arrives live.
  it('returns no fetch while the window holds the newest message', () => {
    const { resolver, fetchSpan } = fixture([request], {
      holdsNewest: () => true,
    })
    expect(resolver.loadResult(identity)).toBeUndefined()
    expect(fetchSpan).not.toHaveBeenCalled()
  })

  it('returns no fetch while the window holds the result', () => {
    const { resolver, fetchSpan } = fixture([request, result], {
      holdsNewest: () => false,
    })
    expect(resolver.loadResult(identity)).toBeUndefined()
    expect(fetchSpan).not.toHaveBeenCalled()
  })

  it('returns no fetch once a fetch of the span settled without a result', async () => {
    const { resolver, fetchSpan } = fixture([request], {
      holdsNewest: () => false,
    })
    fetchSpan.mockResolvedValueOnce([request])
    const release = resolver.retainSpan(identity)
    await resolver.loadResult(identity)
    expect(resolver.loadResult(identity)).toBeUndefined()
    expect(fetchSpan).toHaveBeenCalledOnce()
    release()
  })

  it('returns no fetch for a message of no span', () => {
    const { resolver, fetchSpan } = fixture([], {
      holdsNewest: () => false,
    })
    expect(resolver.loadResult({ spanId: '', agentSessionId: '' })).toBeUndefined()
    expect(fetchSpan).not.toHaveBeenCalled()
  })
})

describe('explicit no-side message context', () => {
  const identity = { spanId: 'span', agentSessionId: '' }

  it('keeps an isolated no-side record out of pair lookups and visible rows', () => {
    const progress = noSideMessage('window', 1n)
    const { resolver, visibleRows } = fixture([progress])
    expect(resolver.role(progress)).toBe('none')
    expect(resolver.request(identity)).toBeUndefined()
    expect(resolver.result(identity)).toBeUndefined()
    expect(visibleRows(identity)).toEqual({ request: false })
  })

  it('keeps hidden no-side progress beside the genuine request', () => {
    const request = toolMessage('request', 1n, 'request')
    const progress = noSideMessage('window', 2n)
    const { resolver, visibleRows } = fixture([request, progress])
    expect(resolver.role(progress)).toBe('none')
    expect(resolver.request(identity)?.message).toBe(request)
    expect(resolver.result(identity)).toBeUndefined()
    expect(visibleRows(identity)).toEqual({ request: true })
  })

  it.each(['request', 'result'] as const)('refuses a stale %s slot that contains a no-side record', (side) => {
    const progress = noSideMessage('window', 2n)
    const { resolver, visibleRows } = fixture([progress], {
      spanMessage: (_identity, requestedSide) => requestedSide === side
        ? progress
        : undefined,
    })
    expect(resolver.role(progress)).toBe('none')
    expect(resolver.request(identity)).toBeUndefined()
    expect(resolver.result(identity)).toBeUndefined()
    expect(visibleRows(identity)).toEqual({ request: false })
  })

  it('keeps fetched no-side records out of pair lookups', async () => {
    const progress = noSideMessage('window', 1n)
    const { resolver, visibleRows, fetchSpan } = fixture()
    fetchSpan.mockResolvedValueOnce([progress])
    const release = resolver.retainSpan(identity)
    await resolver.loadSpan(identity)
    expect(resolver.role(progress)).toBe('none')
    expect(resolver.request(identity)).toBeUndefined()
    expect(resolver.result(identity)).toBeUndefined()
    expect(visibleRows(identity)).toEqual({ request: false })
    release()
  })

  it('keeps a genuine fetched result when a newer resident slot contains no-side progress', async () => {
    const result = toolMessage('result', 2n, 'result')
    const progress = noSideMessage('window', 3n)
    const { resolver, visibleRows, fetchSpan } = fixture([progress], {
      spanMessage: (_identity, side) => side === 'result'
        ? progress
        : undefined,
    })
    fetchSpan.mockResolvedValueOnce([result])
    const release = resolver.retainSpan(identity)
    await resolver.loadSpan(identity)
    expect(resolver.result(identity)?.message).toBe(result)
    expect(resolver.role(progress)).toBe('none')
    expect(visibleRows(identity)).toEqual({ request: false })
    release()
  })

  it('recovers the genuine pair when both resident slots contain no-side records', async () => {
    const first = noSideMessage('first-window', 1n)
    const second = noSideMessage('second-window', 2n)
    const request = toolMessage('request', 3n, 'request')
    const result = toolMessage('result', 4n, 'result')
    const { resolver, visibleRows, fetchSpan } = fixture([first, second], {
      spanMessage: (_identity, side) => side === 'request'
        ? first
        : second,
    })
    fetchSpan.mockResolvedValueOnce([request, result])
    const release = resolver.retainSpan(identity)
    await resolver.loadSpan(identity)
    expect(fetchSpan).toHaveBeenCalledTimes(1)
    expect(resolver.request(identity)?.message).toBe(request)
    expect(resolver.result(identity)?.message).toBe(result)
    // The genuine request lies outside the window, and the transcript draws it as a row.
    expect(visibleRows(identity)).toEqual({ request: true })
    release()
  })

  it.each(['request', 'result'] as const)('removes a %s side after a same-ID replacement becomes no-side', (side) => {
    const original = toolMessage('changing', 1n, side)
    const replacement = noSideMessage(original.id, original.seq)
    const { resolver, visibleRows, replaceMessages } = fixture([original])
    expect(resolver.role(original)).toBe(side)
    replaceMessages([replacement])
    expect(resolver.role(original)).toBe('none')
    expect(resolver.request(identity)).toBeUndefined()
    expect(resolver.result(identity)).toBeUndefined()
    expect(visibleRows(identity)).toEqual({ request: false })
  })

  it('keeps a reused span separate when only one session has no-side progress', () => {
    const first = toolMessage('first-request', 1n, 'request', { agentSessionId: 'first-session' })
    const second = toolMessage('second-result', 2n, 'result', { agentSessionId: 'second-session' })
    const progress = noSideMessage('window', 3n, { agentSessionId: 'first-session' })
    const { resolver, visibleRows } = fixture([first, second, progress])
    expect(visibleRows({ spanId: 'span', agentSessionId: 'first-session' })).toEqual({ request: true })
    expect(visibleRows({ spanId: 'span', agentSessionId: 'second-session' })).toEqual({ request: false })
    expect(resolver.request({ spanId: 'span', agentSessionId: 'first-session' })?.message).toBe(first)
    expect(resolver.result({ spanId: 'span', agentSessionId: 'second-session' })?.message).toBe(second)
  })
})
