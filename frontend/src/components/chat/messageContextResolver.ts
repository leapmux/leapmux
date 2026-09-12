import type { FileImageLoadOptions, FileImageReader } from './fileImageResolver'
import type { SpanRole } from './providers/registry'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import type { TodoItem } from '~/stores/chatTodos'
import type { ToolProgressEntry } from '~/stores/chatToolProgress'
import type { SpanMessageRevision, ToolMessageSide } from '~/stores/chatTypes'
import { batch, createEffect, createSignal, onCleanup, untrack } from 'solid-js'
import { parseMessageContent } from '~/lib/messageParser'
import { preferNewerSupplement } from '~/stores/chatMessageOrder'
import { createSpanIndex } from '~/stores/chatSpanIndex'
import { createFileImageResolver } from './fileImageResolver'
import { parsedMessageForRendering, pluginFor } from './providers/registry'

/** A message and the revision of the data that its renderer receives. */
export interface ResolvedMessage {
  message: AgentChatMessage
  original: ParsedMessageContent
  parsed: ParsedMessageContent
  revision: SpanMessageRevision
}

/** The data sources for one agent. Entity getters read live stores. */
export interface MessageContextSources {
  scopeKey: () => string
  messages: () => AgentChatMessage[]
  messageVersion: () => number
  contentVersion: (messageId: string) => number
  spanMessage: (spanId: string, side: ToolMessageSide) => AgentChatMessage | undefined
  messageBySeq: (seq: bigint) => AgentChatMessage | undefined
  fetchSpan: (spanId: string, signal: AbortSignal) => Promise<AgentChatMessage[]>
  fetchMessage: (seq: bigint, signal: AbortSignal) => Promise<AgentChatMessage | undefined>
  fetchFileImage: FileImageReader
  subscribe: (observer: (message: AgentChatMessage) => void) => () => void
  todo: (taskId: string) => TodoItem | undefined
  backgroundTask: (rowKey: string) => BackgroundTaskItem | undefined
  progress: (spanId: string) => ToolProgressEntry | undefined
}

/** One resolution path for tool renderers, image tabs, and message previews. */
export interface MessageContextResolver {
  current: (message: AgentChatMessage, parsed?: ParsedMessageContent) => ResolvedMessage
  request: (spanId: string) => ResolvedMessage | undefined
  result: (spanId: string) => ResolvedMessage | undefined
  loadSpan: (spanId: string) => Promise<void>
  loadRelated: (message: AgentChatMessage, parsed?: ParsedMessageContent) => Promise<void>
  retainSpan: (spanId: string) => () => void
  peek: (seq: bigint) => ResolvedMessage | undefined
  message: (seq: bigint) => Promise<ResolvedMessage | undefined>
  todo: (taskId: string) => TodoItem | undefined
  backgroundTask: (rowKey: string) => BackgroundTaskItem | undefined
  progress: (spanId: string) => ToolProgressEntry | undefined
  contentVersion: (messageId: string) => number
  fileImage: (path: string, options?: FileImageLoadOptions) => Promise<ImageResultSource>
  cachedFileImage: (path: string, reference?: string) => ImageResultSource | undefined
}

/** Reactive sources for one rendered row. Only the consuming component subscribes. */
export interface MessageRenderSources {
  current: () => ParsedMessageContent | undefined
  request: () => ParsedMessageContent | undefined
  result: () => ParsedMessageContent | undefined
  role: () => SpanRole
  fileImage: MessageContextResolver['fileImage']
  cachedFileImage: MessageContextResolver['cachedFileImage']
  todo: MessageContextResolver['todo']
  backgroundTask: MessageContextResolver['backgroundTask']
  progress: () => ToolProgressEntry | undefined
}

export function createMessageRenderSources(resolver: () => MessageContextResolver | undefined, message: () => AgentChatMessage, current: () => ParsedMessageContent): MessageRenderSources {
  const spanId = () => message().spanId
  return {
    current,
    request: () => resolver()?.request(spanId())?.parsed,
    result: () => resolver()?.result(spanId())?.parsed,
    role: () => {
      const context = resolver()
      const own = message()
      if (context?.result(own.spanId)?.message.id === own.id)
        return 'result'
      if (context?.request(own.spanId)?.message.id === own.id)
        return 'opener'
      return pluginFor(own.agentProvider)?.spanRole?.(current()) ?? 'other'
    },
    fileImage: (path, options) => resolver()?.fileImage(path, { ...options, reference: message().spanId || message().id }) ?? Promise.reject(new Error('The image source is unavailable')),
    cachedFileImage: path => resolver()?.cachedFileImage(path, message().spanId || message().id),
    todo: taskId => resolver()?.todo(taskId),
    backgroundTask: rowKey => resolver()?.backgroundTask(rowKey),
    progress: () => resolver()?.progress(spanId()),
  }
}

interface ResolvedCacheEntry {
  reference: ResolvedMessage
  contentVersion: number
  supplementalRevision: bigint
}

function newestRelatedMessage(first: AgentChatMessage | undefined, second: AgentChatMessage | undefined): AgentChatMessage | undefined {
  if (!first)
    return second
  if (!second)
    return first
  if (first.id === second.id && first.seq === second.seq)
    return preferNewerSupplement(second, first)
  return first.seq >= second.seq ? first : second
}

export function createMessageContextResolver(source: MessageContextSources): MessageContextResolver {
  const fileImages = createFileImageResolver(source.fetchFileImage)
  const fetched = new Map<string, AgentChatMessage>()
  const resolved = new Map<string, ResolvedCacheEntry>()
  const loadedSpans = new Set<string>()
  const leases = new Map<string, number>()
  const inflight = new Map<string, { controller: AbortController, promise: Promise<unknown> }>()
  const spans = createSpanIndex()
  const [cacheVersion, setCacheVersion] = createSignal(0)
  let scope = source.scopeKey()
  let disposed = false

  function clear(): void {
    fileImages.clear()
    for (const request of inflight.values())
      request.controller.abort()
    inflight.clear()
    fetched.clear()
    resolved.clear()
    loadedSpans.clear()
    spans.reindex(scope, [])
    setCacheVersion(value => value + 1)
  }

  function reference(message: AgentChatMessage, original = parseMessageContent(message), cache = true): ResolvedMessage {
    const version = source.contentVersion(message.id)
    const previous = cache ? resolved.get(message.id) : undefined
    if (previous?.reference.message === message && previous.reference.original === original
      && previous.contentVersion === version && previous.supplementalRevision === message.supplementalRevision) {
      return previous.reference
    }
    const value: ResolvedMessage = {
      message,
      original,
      parsed: parsedMessageForRendering(original, message.agentProvider),
      revision: { id: message.id, seq: message.seq, contentVersion: version, supplementalRevision: message.supplementalRevision },
    }
    if (cache)
      resolved.set(message.id, { reference: value, contentVersion: version, supplementalRevision: message.supplementalRevision })
    return value
  }

  function current(message: AgentChatMessage, parsed?: ParsedMessageContent): ResolvedMessage {
    cacheVersion()
    let latest = message
    if (!disposed && source.scopeKey() === scope) {
      const resident = source.messageBySeq(message.seq)
      const cached = fetched.get(message.id)
      if (resident?.id === message.id)
        latest = preferNewerSupplement(latest, resident)
      if (cached?.seq === message.seq && cached.supplementalRevision > latest.supplementalRevision)
        latest = cached
    }
    return reference(latest, latest === message ? parsed : undefined)
  }

  function remember(messages: AgentChatMessage[]): void {
    let changed = false
    let reindex = false
    for (const incoming of messages) {
      const resident = source.messageBySeq(incoming.seq)
      const message = resident?.id === incoming.id ? preferNewerSupplement(incoming, resident) : incoming
      const previous = fetched.get(message.id)
      if (previous && preferNewerSupplement(previous, message) === previous)
        continue
      fetched.set(message.id, message)
      resolved.delete(message.id)
      if (spans.index(scope, message))
        reindex = true
      changed = true
    }
    if (changed) {
      if (reindex)
        spans.reindex(scope, [...fetched.values()].sort((a, b) => a.seq < b.seq ? -1 : a.seq > b.seq ? 1 : 0))
      setCacheVersion(value => value + 1)
    }
  }

  function related(spanId: string, side: ToolMessageSide): ResolvedMessage | undefined {
    source.messageVersion()
    cacheVersion()
    if (!spanId || disposed || source.scopeKey() !== scope)
      return undefined
    const resident = source.spanMessage(spanId, side)
    const cached = side === 'request' ? spans.getOpenerMessage(scope, spanId) : spans.getResultMessage(scope, spanId)
    const message = newestRelatedMessage(resident, cached)
    return message ? reference(message) : undefined
  }

  function prune(messages: AgentChatMessage[]): void {
    const retainedSpans = new Set(messages.map(message => message.spanId).filter(Boolean))
    for (const spanId of leases.keys())
      retainedSpans.add(spanId)
    const retainedIds = new Set(messages.map(message => message.id))
    let changed = false
    for (const [id, message] of fetched) {
      if (!retainedSpans.has(message.spanId)) {
        fetched.delete(id)
        changed = true
      }
      else {
        retainedIds.add(id)
      }
    }
    for (const id of resolved.keys()) {
      if (!retainedIds.has(id))
        resolved.delete(id)
    }
    for (const spanId of loadedSpans) {
      if (!retainedSpans.has(spanId))
        loadedSpans.delete(spanId)
    }
    for (const [key, request] of inflight) {
      if (key.startsWith('span:') && !retainedSpans.has(key.slice(5))) {
        request.controller.abort()
        inflight.delete(key)
      }
    }
    if (changed) {
      spans.reindex(scope, [...fetched.values()].sort((a, b) => a.seq < b.seq ? -1 : a.seq > b.seq ? 1 : 0))
      setCacheVersion(value => value + 1)
    }
  }

  createEffect(() => {
    const nextScope = source.scopeKey()
    if (nextScope !== scope) {
      batch(() => {
        clear()
        scope = nextScope
      })
    }
    prune(source.messages())
  })
  const unsubscribe = source.subscribe((message) => {
    if (!disposed && message.spanId && (loadedSpans.has(message.spanId) || inflight.has(`span:${message.spanId}`)))
      remember([message])
  })
  onCleanup(() => {
    disposed = true
    unsubscribe()
    clear()
  })

  function loadSpan(spanId: string): Promise<void> {
    // Fetching must not subscribe the caller to cache changes that the fetch causes.
    return untrack(async () => {
      const currentScope = source.scopeKey()
      if (!spanId || disposed || currentScope !== scope || loadedSpans.has(spanId))
        return Promise.resolve()
      if (source.spanMessage(spanId, 'request') && source.spanMessage(spanId, 'result'))
        return Promise.resolve()
      const key = `span:${spanId}`
      const running = inflight.get(key)
      if (running)
        return running.promise as Promise<void>
      const controller = new AbortController()
      const capturedScope = scope
      const promise = source.fetchSpan(spanId, controller.signal).then((messages) => {
        if (disposed || controller.signal.aborted || source.scopeKey() !== capturedScope)
          return
        if (messages.some(message => message.spanId !== spanId))
          throw new Error('The related-message response contains a different span')
        batch(() => {
          remember(messages)
          loadedSpans.add(spanId)
        })
      }).finally(() => {
        if (inflight.get(key)?.controller === controller)
          inflight.delete(key)
      })
      inflight.set(key, { controller, promise })
      return promise
    })
  }

  function message(seq: bigint): Promise<ResolvedMessage | undefined> {
    return untrack(async () => {
      if (seq <= 0n)
        return undefined
      if (disposed || source.scopeKey() !== scope)
        throw new DOMException('The message resolver is no longer active', 'AbortError')
      const resident = source.messageBySeq(seq)
      if (resident)
        return reference(resident, undefined, false)
      const key = `seq:${seq}`
      const running = inflight.get(key)
      if (running)
        return running.promise as Promise<ResolvedMessage | undefined>
      const controller = new AbortController()
      const capturedScope = scope
      const promise = source.fetchMessage(seq, controller.signal).then((fetchedMessage) => {
        if (disposed || controller.signal.aborted || capturedScope !== source.scopeKey())
          throw new DOMException('The message resolver is no longer active', 'AbortError')
        if (!fetchedMessage)
          return undefined
        if (fetchedMessage.seq !== seq)
          throw new Error('The message response contains a different sequence')
        const latest = source.messageBySeq(seq)
        const selected = latest?.id === fetchedMessage.id ? preferNewerSupplement(fetchedMessage, latest) : fetchedMessage
        return reference(selected, undefined, false)
      }).finally(() => {
        if (inflight.get(key)?.controller === controller)
          inflight.delete(key)
      })
      inflight.set(key, { controller, promise })
      return promise
    })
  }

  return {
    current,
    fileImage: (path, options) => disposed ? Promise.reject(new DOMException('The image resolver is no longer active', 'AbortError')) : fileImages.load(path, options),
    cachedFileImage: fileImages.peek,
    request: spanId => related(spanId, 'request'),
    result: spanId => related(spanId, 'result'),
    loadSpan,
    loadRelated: (message, parsed) => untrack(async () => {
      const resolved = current(message, parsed)
      const sides = pluginFor(message.agentProvider)?.relatedMessages?.(resolved.parsed) ?? []
      if (sides.some(side => related(message.spanId, side) === undefined))
        await loadSpan(message.spanId)
    }),
    retainSpan: (spanId) => {
      if (!spanId || disposed)
        return () => undefined
      leases.set(spanId, (leases.get(spanId) ?? 0) + 1)
      let released = false
      return () => {
        if (released || disposed)
          return
        released = true
        const count = leases.get(spanId) ?? 0
        if (count <= 1)
          leases.delete(spanId)
        else
          leases.set(spanId, count - 1)
        prune(source.messages())
      }
    },
    peek: (seq) => {
      if (seq <= 0n || disposed || source.scopeKey() !== scope)
        return undefined
      const message = source.messageBySeq(seq)
      return message ? reference(message, undefined, false) : undefined
    },
    message,
    todo: taskId => disposed ? undefined : source.todo(taskId),
    backgroundTask: rowKey => disposed ? undefined : source.backgroundTask(rowKey),
    progress: spanId => disposed ? undefined : source.progress(spanId),
    contentVersion: messageId => source.contentVersion(messageId),
  }
}
