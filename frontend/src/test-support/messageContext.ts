import type { MessageContextResolver, MessageContextSources } from '~/components/chat/messageContextResolver'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { MessageSpanIdentity } from '~/lib/messageSpan'
import { createMemo, createRoot, createSignal } from 'solid-js'
import { afterEach } from 'vitest'
import { createMessageContextResolver } from '~/components/chat/messageContextResolver'
import { messageSpanKey } from '~/lib/messageSpan'
import { createSpanIndex } from '~/stores/chatSpanIndex'

/** The roots a test built and its own afterEach disposes. Shared with the scenario. */
export const cleanups: Array<() => void> = []
afterEach(() => cleanups.splice(0).forEach(dispose => dispose()))

/** Exercise the real resolver with controlled transcript and transport sources. */
export function testMessageContext(overrides: Partial<MessageContextSources> = {}) {
  return createRoot((dispose) => {
    cleanups.push(dispose)
    const messages = overrides.messages ?? (() => [])
    const index = createMemo(() => {
      const spans = createSpanIndex()
      spans.reindex('test', messages())
      return spans
    })
    return createMessageContextResolver({
      scopeKey: 'test',
      messages,
      messageVersion: () => 0,
      contentVersion: () => 0,
      messageBySeq: seq => messages().find(message => message.seq === seq),
      spanMessage: (spanId, side) => side === 'request' ? index().getRequestMessage('test', spanId) : index().getResultMessage('test', spanId),
      fetchMessage: async () => undefined,
      fetchSpan: async () => [],
      fetchFileImage: async () => { throw new Error('The image source is unavailable') },
      subscribe: () => () => undefined,
      todo: () => undefined,
      backgroundTask: () => undefined,
      progress: () => undefined,
      ...overrides,
    })
  })
}

/**
 * What a mutable transcript overrides on the transport sources it cannot model
 * itself. Everything else — the archive, the fetches over it, the span index,
 * the version counters and the observer list — the transcript owns.
 */
export interface MutableTranscriptOptions {
  /** Replaces the default archive-backed span fetch (stubbing a transport fault). */
  fetchSpan?: MessageContextSources['fetchSpan']
  /** Replaces the default archive-backed sequence fetch. */
  fetchMessage?: MessageContextSources['fetchMessage']
  fetchFileImage?: MessageContextSources['fetchFileImage']
  todo?: MessageContextSources['todo']
  backgroundTask?: MessageContextSources['backgroundTask']
  progress?: MessageContextSources['progress']
}

/**
 * A transcript a test can mutate the way the store does: a loaded window over a
 * complete archive, per-message content versions, and an observer list the live
 * path broadcasts through.
 *
 * The window holds MESSAGE IDS, not sequences, so a same-id replacement (the
 * store's in-place merge) keeps its place while a `replaceWindow` models a page
 * load that swapped the whole view. Every mutation bumps the version the
 * resolver reads to re-walk its span sides; a same-id replacement also bumps
 * that message's content version, which is the discipline the store keeps for
 * an in-place body change.
 */
export interface MutableTranscript {
  /** The resolver sources this transcript backs. */
  sources: MessageContextSources
  /** The message ids currently loaded, in window order (by sequence). */
  windowIds: () => readonly string[]
  /** A message of a NEW id: it enters the archive and the loaded window. */
  append: (message: AgentChatMessage) => void
  /**
   * A message of an id the archive already holds: an in-place body or
   * supplemental replacement under the same id (and usually the same seq).
   */
  replace: (message: AgentChatMessage) => void
  /** A live arrival: the same mutation `append`/`replace` performs, plus the observer broadcast. */
  broadcast: (message: AgentChatMessage) => void
  /** Swap the loaded window; the archive stays whole for the fetches. */
  replaceWindow: (ids: readonly string[]) => void
}

export function createMutableTranscript(archive: readonly AgentChatMessage[], options: MutableTranscriptOptions = {}): MutableTranscript {
  const archiveById = new Map(archive.map(message => [message.id, message]))
  const [windowIds, setWindowIds] = createSignal<string[]>(archive.map(message => message.id))
  const [version, setVersion] = createSignal(0)
  const contentVersions = new Map<string, number>()
  const observers = new Set<(message: AgentChatMessage) => void>()

  const messages = createMemo(() => windowIds()
    .map(id => archiveById.get(id))
    .filter((message): message is AgentChatMessage => message !== undefined)
    .sort((a, b) => (a.seq < b.seq ? -1 : a.seq > b.seq ? 1 : 0)))
  const index = createMemo(() => {
    const spans = createSpanIndex()
    spans.reindex('test', messages())
    return spans
  })

  function store(message: AgentChatMessage, broadcast: boolean): void {
    const known = archiveById.has(message.id)
    archiveById.set(message.id, message)
    if (!known) {
      setWindowIds(ids => [...ids, message.id])
    }
    else {
      contentVersions.set(message.id, (contentVersions.get(message.id) ?? 0) + 1)
    }
    setVersion(value => value + 1)
    if (broadcast) {
      for (const observer of observers)
        observer(message)
    }
  }

  return {
    sources: {
      scopeKey: 'test',
      messages,
      messageVersion: () => version(),
      contentVersion: id => contentVersions.get(id) ?? 0,
      messageBySeq: (seq) => {
        for (const message of archiveById.values()) {
          if (message.seq === seq)
            return message
        }
        return undefined
      },
      spanMessage: (identity, side) => side === 'request' ? index().getRequestMessage('test', identity) : index().getResultMessage('test', identity),
      // The default span fetch answers from the COMPLETE archive, filtered by the
      // whole span identity: the session AND the span id, which is what keeps one
      // session from answering another's reused tool id.
      fetchSpan: options.fetchSpan ?? (async (identity: MessageSpanIdentity) =>
        [...archiveById.values()].filter(message => messageSpanKey(message) === messageSpanKey(identity))),
      fetchMessage: options.fetchMessage ?? (async (seq: bigint) =>
        [...archiveById.values()].find(message => message.seq === seq)),
      fetchFileImage: options.fetchFileImage ?? (async () => { throw new Error('The image source is unavailable') }),
      subscribe: (observer) => {
        observers.add(observer)
        return () => observers.delete(observer)
      },
      todo: options.todo ?? (() => undefined),
      backgroundTask: options.backgroundTask ?? (() => undefined),
      progress: options.progress ?? (() => undefined),
    },
    windowIds: () => windowIds(),
    append: message => store(message, false),
    replace: message => store(message, false),
    broadcast: message => store(message, true),
    replaceWindow: (ids) => {
      setWindowIds([...ids])
      setVersion(value => value + 1)
    },
  }
}

/** The real resolver over a mutable transcript, disposed with the test that built it. */
export function testTranscriptContext(archive: readonly AgentChatMessage[], options: MutableTranscriptOptions = {}) {
  const tuple: { transcript: MutableTranscript, resolver: MessageContextResolver } = { transcript: undefined as never, resolver: undefined as never }
  createRoot((dispose) => {
    cleanups.push(dispose)
    tuple.transcript = createMutableTranscript(archive, options)
    tuple.resolver = createMessageContextResolver(tuple.transcript.sources)
  })
  return tuple
}
