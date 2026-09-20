import type { FileImageLoadOptions, FileImageReader } from './fileImageResolver'
import type { ToolSpanRowPresence } from './model/row'
import type {} from './providers/registry'
import type { ResolvedMessageContent } from './rowExtractionTypes'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { MessageRevision, MessageSpanIdentity, ToolSpanRole, ToolSpanSide } from '~/lib/messageSpan'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import type { ToolProgressEntry } from '~/stores/chatToolProgress'
import { batch, createEffect, createSignal, onCleanup, untrack } from 'solid-js'
import { parseMessageContent } from '~/lib/messageParser'
import { messageSpanIdentity, messageSpanKey } from '~/lib/messageSpan'
import { preferNewerSupplement } from '~/stores/chatMessageOrder'
import { createSpanIndex } from '~/stores/chatSpanIndex'
import { createFileImageResolver } from './fileImageResolver'
import { pluginFor, resolvedSpanRole, resolveMessageForRendering } from './providers/registry'

/** A message and the revision of the data that its renderer receives. */
export interface ResolvedMessage {
  message: AgentChatMessage
  original: ParsedMessageContent
  resolved: ResolvedMessageContent
  revision: MessageRevision
}

/** The data sources for one agent. Entity getters read live stores. */
export interface MessageContextSources {
  /**
   * The agent partition of the span index, and the isolation promise of this
   * resolver: no row of another agent reaches a reader through it.
   *
   * A plain string, not an accessor. One resolver serves ONE agent for its whole
   * life. `TileRenderer` builds each resolver inside a `mapArray` keyed by this
   * same string, so a changed key DISPOSES the mapped scope and builds a fresh
   * resolver rather than moving this one. The type states that, which makes a
   * resolver that outlives its agent unrepresentable. An accessor stated the
   * opposite, and every runtime guard that compared it was unreachable.
   *
   * `onCleanup` sets `disposed` when `mapArray` drops the key. That is the
   * lifetime mechanism, and it stays.
   */
  readonly scopeKey: string
  messages: () => AgentChatMessage[]
  messageVersion: () => number
  contentVersion: (messageId: string) => number
  spanMessage: (identity: MessageSpanIdentity, side: ToolSpanSide) => AgentChatMessage | undefined
  messageBySeq: (seq: bigint) => AgentChatMessage | undefined
  fetchSpan: (identity: MessageSpanIdentity, signal: AbortSignal) => Promise<AgentChatMessage[]>
  fetchMessage: (seq: bigint, signal: AbortSignal) => Promise<AgentChatMessage | undefined>
  fetchFileImage: FileImageReader
  subscribe: (observer: (message: AgentChatMessage) => void) => () => void
  backgroundTask: (rowKey: string) => BackgroundTaskItem | undefined
  progress: (identity: MessageSpanIdentity) => ToolProgressEntry | undefined
}

/** One resolution path for tool renderers, image tabs, and message previews. */
export interface MessageContextResolver {
  resolvedMessage: (message: AgentChatMessage, parsed?: ParsedMessageContent) => ResolvedMessage
  role: (message: AgentChatMessage, parsed?: ParsedMessageContent) => ToolSpanRole
  visibleRows: (identity: MessageSpanIdentity) => ToolSpanRowPresence
  request: (identity: MessageSpanIdentity) => ResolvedMessage | undefined
  result: (identity: MessageSpanIdentity) => ResolvedMessage | undefined
  loadSpan: (identity: MessageSpanIdentity) => Promise<void>
  loadRelated: (message: AgentChatMessage, parsed?: ParsedMessageContent) => Promise<void>
  retainSpan: (identity: MessageSpanIdentity) => () => void
  /** Retain a rendered row's span across a keyed component remount. */
  retainRenderSpan: (identity: MessageSpanIdentity) => () => void
  retainMessage: (seq: bigint) => () => void
  peek: (seq: bigint) => ResolvedMessage | undefined
  message: (seq: bigint) => Promise<ResolvedMessage | undefined>
  backgroundTask: (rowKey: string) => BackgroundTaskItem | undefined
  progress: (identity: MessageSpanIdentity) => ToolProgressEntry | undefined
  contentVersion: (messageId: string) => number
  fileImage: (path: string, options?: FileImageLoadOptions) => Promise<ImageResultSource>
  cachedFileImage: (path: string, reference?: string) => ImageResultSource | undefined
}

/** Reactive sources for one rendered row. Only the consuming component subscribes. */
export interface MessageRenderSources {
  current: () => ResolvedMessageContent | undefined
  request: () => ResolvedMessageContent | undefined
  result: () => ResolvedMessageContent | undefined
  role: () => ToolSpanRole
  visibleRows: () => ToolSpanRowPresence
  fileImage: MessageContextResolver['fileImage']
  cachedFileImage: MessageContextResolver['cachedFileImage']
  backgroundTask: MessageContextResolver['backgroundTask']
  progress: () => ToolProgressEntry | undefined
}

export function createMessageRenderSources(resolver: () => MessageContextResolver | undefined, message: () => AgentChatMessage, current: () => ResolvedMessageContent): MessageRenderSources {
  const span = () => messageSpanIdentity(message())
  return {
    current,
    request: () => resolver()?.request(span())?.resolved,
    result: () => resolver()?.result(span())?.resolved,
    role: () => {
      const own = message()
      return resolver()?.role(own) ?? resolvedSpanRole(current(), own.agentProvider)
    },
    visibleRows: () => resolver()?.visibleRows(span()) ?? { request: false, result: false },
    fileImage: (path, options) => resolver()?.fileImage(path, { ...options, reference: message().id }) ?? Promise.reject(new Error('The image source is unavailable')),
    cachedFileImage: path => resolver()?.cachedFileImage(path, message().id),
    backgroundTask: rowKey => resolver()?.backgroundTask(rowKey),
    progress: () => resolver()?.progress(messageSpanIdentity(message())),
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
  const renderGraceSpans = new Set<string>()
  const messageLeases = new Map<bigint, number>()
  const inflight = new Map<string, { controller: AbortController, promise: Promise<unknown> }>()
  const spans = createSpanIndex()
  const [cacheVersion, setCacheVersion] = createSignal(0)
  let disposed = false
  let pruneTimer: ReturnType<typeof setTimeout> | undefined

  function clear(): void {
    if (pruneTimer !== undefined)
      clearTimeout(pruneTimer)
    pruneTimer = undefined
    fileImages.clear()
    for (const request of inflight.values())
      request.controller.abort()
    inflight.clear()
    fetched.clear()
    resolved.clear()
    loadedSpans.clear()
    leases.clear()
    renderGraceSpans.clear()
    messageLeases.clear()
    spans.reindex(source.scopeKey, [])
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
      resolved: resolveMessageForRendering(original, message.agentProvider),
      revision: { id: message.id, seq: message.seq, contentVersion: version, supplementalRevision: message.supplementalRevision },
    }
    if (cache)
      resolved.set(message.id, { reference: value, contentVersion: version, supplementalRevision: message.supplementalRevision })
    return value
  }

  function current(message: AgentChatMessage, parsed?: ParsedMessageContent): ResolvedMessage {
    cacheVersion()
    let latest = message
    if (!disposed) {
      const resident = source.messageBySeq(message.seq)
      const cached = fetched.get(message.id)
      if (resident?.id === message.id)
        latest = preferNewerSupplement(latest, resident)
      if (cached?.seq === message.seq && cached.supplementalRevision > latest.supplementalRevision)
        latest = cached
    }
    return reference(latest, latest === message ? parsed : undefined)
  }

  function role(message: AgentChatMessage, parsed?: ParsedMessageContent): ToolSpanRole {
    const selected = current(message, parsed)
    const identity = messageSpanIdentity(selected.message)
    if (related(identity, 'result')?.message.id === selected.message.id)
      return 'result'
    if (related(identity, 'request')?.message.id === selected.message.id)
      return 'request'
    return resolvedSpanRole(selected.resolved, selected.message.agentProvider)
  }

  function visibleRows(identity: MessageSpanIdentity): ToolSpanRowPresence {
    const key = messageSpanKey(identity)
    let request = false
    let result = false
    for (const message of source.messages()) {
      if (messageSpanKey(message) !== key)
        continue
      const rowRole = role(message)
      request ||= rowRole === 'request'
      result ||= rowRole === 'result'
    }
    return { request, result }
  }

  function remember(messages: AgentChatMessage[]): void {
    let changed = false
    let reindex = false
    const replacements = new Map(messages.map(message => [message.seq, message.id]))
    for (const [id, cached] of fetched) {
      const replacement = replacements.get(cached.seq)
      if (replacement !== undefined && replacement !== id) {
        fetched.delete(id)
        resolved.delete(id)
        changed = true
        reindex = true
      }
    }
    for (const incoming of messages) {
      const resident = source.messageBySeq(incoming.seq)
      const message = resident?.id === incoming.id ? preferNewerSupplement(incoming, resident) : incoming
      const previous = fetched.get(message.id)
      if (previous && preferNewerSupplement(previous, message) === previous)
        continue
      fetched.set(message.id, message)
      resolved.delete(message.id)
      if (spans.index(source.scopeKey, message))
        reindex = true
      changed = true
    }
    if (changed) {
      if (reindex)
        spans.reindex(source.scopeKey, [...fetched.values()].sort((a, b) => a.seq < b.seq ? -1 : a.seq > b.seq ? 1 : 0))
      setCacheVersion(value => value + 1)
    }
  }

  function related(identity: MessageSpanIdentity, side: ToolSpanSide): ResolvedMessage | undefined {
    source.messageVersion()
    cacheVersion()
    if (!identity.spanId || disposed)
      return undefined
    const matches = (message: AgentChatMessage | undefined) => message && messageSpanKey(message) === messageSpanKey(identity) ? message : undefined
    const resident = matches(source.spanMessage(identity, side))
    const cached = matches(side === 'request' ? spans.getRequestMessage(source.scopeKey, identity) : spans.getResultMessage(source.scopeKey, identity))
    const message = newestRelatedMessage(resident, cached)
    return message ? reference(message) : undefined
  }

  function messageBySeq(seq: bigint): AgentChatMessage | undefined {
    const resident = source.messageBySeq(seq)
    for (const cached of fetched.values()) {
      if (cached.seq === seq)
        return resident ? newestRelatedMessage(resident, cached) : cached
    }
    return resident
  }

  /** Keep a message observed during a fetch when the response carries an older identity or supplemental revision. */
  function selectRecoveredMessage(recovered: AgentChatMessage): AgentChatMessage {
    const observed = messageBySeq(recovered.seq)
    if (!observed)
      return recovered
    return observed.id === recovered.id ? preferNewerSupplement(recovered, observed) : observed
  }

  function prune(messages: AgentChatMessage[]): void {
    remember(messages.filter(message => messageLeases.has(message.seq)))
    const retainedSpans = new Set(messages.filter(message => message.spanId).map(messageSpanKey))
    for (const spanId of leases.keys())
      retainedSpans.add(spanId)
    for (const spanId of renderGraceSpans)
      retainedSpans.add(spanId)
    const retainedIds = new Set(messages.map(message => message.id))
    let changed = false
    for (const [id, message] of fetched) {
      if (!retainedSpans.has(messageSpanKey(message)) && !messageLeases.has(message.seq)) {
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
      spans.reindex(source.scopeKey, [...fetched.values()].sort((a, b) => a.seq < b.seq ? -1 : a.seq > b.seq ? 1 : 0))
      setCacheVersion(value => value + 1)
    }
  }

  // The array read stays tracked, and the walk does not.
  //
  // `prune` reads the seq, the id and the span of every window message through
  // the store proxy. A tracked walk therefore subscribes this effect to four
  // nodes on each of hundreds of rows, and builds that whole subscription set
  // again on every run. prune acts on MEMBERSHIP alone, and the array identity is
  // what a membership change moves: the store replaces the whole array for an
  // append, a prepend, a trim, and a reseq. An in-place merge into one row (the
  // store's updateExistingMessage) moves no membership, so it must wake nothing
  // here -- and a tracked walk wakes for each field of that merge which it reads.
  createEffect(() => {
    const messages = source.messages()
    untrack(() => prune(messages))
  })
  const unsubscribe = source.subscribe((message) => {
    if (!disposed && (messageLeases.has(message.seq)
      || (message.spanId && (loadedSpans.has(messageSpanKey(message)) || inflight.has(`span:${messageSpanKey(message)}`))))) {
      remember([message])
    }
  })
  onCleanup(() => {
    disposed = true
    unsubscribe()
    clear()
  })

  function loadSpan(identity: MessageSpanIdentity): Promise<void> {
    const spanId = messageSpanKey(identity)
    // Fetching must not subscribe the caller to cache changes that the fetch causes.
    return untrack(async () => {
      if (!identity.spanId || disposed || loadedSpans.has(spanId))
        return Promise.resolve()
      if (source.spanMessage(identity, 'request') && source.spanMessage(identity, 'result'))
        return Promise.resolve()
      const key = `span:${spanId}`
      const running = inflight.get(key)
      if (running)
        return running.promise as Promise<void>
      const controller = new AbortController()
      const promise = source.fetchSpan(identity, controller.signal).then((messages) => {
        if (disposed || controller.signal.aborted)
          return
        if (messages.some(message => messageSpanKey(message) !== spanId))
          throw new Error('The related-message response contains a different span or provider session')
        batch(() => {
          remember(messages.map(selectRecoveredMessage))
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
      if (disposed)
        throw new DOMException('The message resolver is no longer active', 'AbortError')
      const resident = messageBySeq(seq)
      if (resident)
        return reference(resident, undefined, false)
      const key = `seq:${seq}`
      const running = inflight.get(key)
      if (running)
        return running.promise as Promise<ResolvedMessage | undefined>
      const controller = new AbortController()
      const promise = source.fetchMessage(seq, controller.signal).then((fetchedMessage) => {
        if (disposed || controller.signal.aborted)
          throw new DOMException('The message resolver is no longer active', 'AbortError')
        if (!fetchedMessage)
          return undefined
        if (fetchedMessage.seq !== seq)
          throw new Error('The message response contains a different sequence')
        const selected = selectRecoveredMessage(fetchedMessage)
        if (messageLeases.has(seq))
          remember([selected])
        return reference(selected, undefined, false)
      }).finally(() => {
        if (inflight.get(key)?.controller === controller)
          inflight.delete(key)
      })
      inflight.set(key, { controller, promise })
      return promise
    })
  }

  /**
   * Prune after Solid completes the current task.
   *
   * A sibling arrival changes a classified entry and remounts its row. The old row
   * releases its lease before the replacement row takes the same lease. Immediate
   * pruning drops the fetched sibling during that transfer and restores the stale row.
   * Solid can mount the replacement row in a microtask. A separate grace set protects
   * the span from another consumer's immediate prune during that interval. A task
   * timer then clears the grace set and prunes from the final lease set.
   */
  function schedulePrune(): void {
    if (pruneTimer !== undefined || disposed)
      return
    pruneTimer = setTimeout(() => {
      pruneTimer = undefined
      if (disposed)
        return
      renderGraceSpans.clear()
      prune(source.messages())
    }, 0)
  }

  function retain<Key>(counts: Map<Key, number>, key: Key, onLastRelease: (counts: Map<Key, number>, key: Key) => void): () => void {
    counts.set(key, (counts.get(key) ?? 0) + 1)
    let released = false
    return () => {
      if (released || disposed)
        return
      released = true
      const count = counts.get(key) ?? 0
      if (count <= 1) {
        onLastRelease(counts, key)
      }
      else {
        counts.set(key, count - 1)
      }
    }
  }

  return {
    resolvedMessage: current,
    role,
    visibleRows,
    fileImage: (path, options) => disposed ? Promise.reject(new DOMException('The image resolver is no longer active', 'AbortError')) : fileImages.load(path, options),
    cachedFileImage: fileImages.peek,
    request: identity => related(identity, 'request'),
    result: identity => related(identity, 'result'),
    loadSpan,
    loadRelated: (message, parsed) => untrack(async () => {
      const resolved = current(message, parsed)
      const sides = pluginFor(message.agentProvider)?.transcript.relatedMessages?.(resolved.resolved) ?? []
      if (sides.some(side => related(messageSpanIdentity(message), side) === undefined))
        await loadSpan(messageSpanIdentity(message))
    }),
    retainSpan: (identity) => {
      if (!identity.spanId || disposed)
        return () => undefined
      return retain(leases, messageSpanKey(identity), (counts, key) => {
        counts.delete(key)
        prune(source.messages())
      })
    },
    retainRenderSpan: (identity) => {
      if (!identity.spanId || disposed)
        return () => undefined
      const key = messageSpanKey(identity)
      renderGraceSpans.delete(key)
      return retain(leases, key, (counts, releasedKey) => {
        counts.delete(releasedKey)
        renderGraceSpans.add(releasedKey)
        schedulePrune()
      })
    },
    retainMessage: seq => untrack(() => {
      if (seq <= 0n || disposed)
        return () => undefined
      const release = retain(messageLeases, seq, (counts, key) => {
        counts.delete(key)
        prune(source.messages())
      })
      const resident = source.messageBySeq(seq)
      if (resident)
        remember([resident])
      return release
    }),
    peek: (seq) => {
      cacheVersion()
      if (seq <= 0n || disposed)
        return undefined
      const message = messageBySeq(seq)
      return message ? reference(message, undefined, false) : undefined
    },
    message,
    backgroundTask: rowKey => disposed ? undefined : source.backgroundTask(rowKey),
    // The lookup takes the WHOLE span identity, because the store keys an entry
    // by the provider session and the span together. The producer states the
    // session on the `running_tool` payload, so a row reaches its own entry and
    // no other session's. Two clears still end an entry's life: a lifecycle
    // event clears the agent's whole store (clearPerTurnLiveState), and the
    // tool's result row drops its own entry (dropFinishedToolProgress).
    progress: identity => disposed ? undefined : source.progress(identity),
    contentVersion: messageId => source.contentVersion(messageId),
  }
}
