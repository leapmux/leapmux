import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { createStore } from 'solid-js/store'
import * as workerRpc from '~/api/workerRpc'
import { ContentCompression, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { PREFIX_LOCAL_MESSAGES, safeGetJson, safeRemoveItem, safeSetJson } from '~/lib/browserStorage'
import { extractTodos, findLatestTodos, parseMessageContent } from '~/lib/messageParser'

// ---------------------------------------------------------------------------
// Local (optimistic) message persistence via localStorage
// ---------------------------------------------------------------------------

interface PersistedLocalMessage {
  id: string
  contentText: string
  createdAt: string
  deliveryError: string
  attachments?: Array<{ filename?: string, mime_type?: string, data?: string }>
}

function getPersistedLocalMessages(agentId: string): PersistedLocalMessage[] {
  return safeGetJson<PersistedLocalMessage[]>(`${PREFIX_LOCAL_MESSAGES}${agentId}`) ?? []
}

function persistLocalMessage(agentId: string, msg: PersistedLocalMessage) {
  const list = getPersistedLocalMessages(agentId)
  list.push(msg)
  safeSetJson(`${PREFIX_LOCAL_MESSAGES}${agentId}`, list)
}

function removePersistedLocalMessage(agentId: string, messageId: string) {
  const list = getPersistedLocalMessages(agentId)
  if (list.length === 0)
    return
  const filtered = list.filter(m => m.id !== messageId)
  if (filtered.length === 0) {
    safeRemoveItem(`${PREFIX_LOCAL_MESSAGES}${agentId}`)
  }
  else {
    safeSetJson(`${PREFIX_LOCAL_MESSAGES}${agentId}`, filtered)
  }
}

function extractUserMessagePayload(message: AgentChatMessage): { content: string, attachments?: Array<{ filename?: string, mime_type?: string }> } | null {
  if (message.role !== MessageRole.USER)
    return null
  const parsed = parseMessageContent(message)
  const parent = parsed.parentObject
  if (!parent)
    return null
  if (typeof parent.content === 'string') {
    const attachments = Array.isArray(parent.attachments)
      ? (parent.attachments as Array<{ filename?: string, mime_type?: string }>)
          .map(att => ({
            filename: typeof att?.filename === 'string' ? att.filename : undefined,
            mime_type: typeof att?.mime_type === 'string' ? att.mime_type : undefined,
          }))
      : undefined
    return { content: parent.content, attachments }
  }
  const msg = parent.message as Record<string, unknown> | undefined
  if (msg && typeof msg.content === 'string') {
    const attachments = Array.isArray(msg.attachments)
      ? (msg.attachments as Array<{ filename?: string, mime_type?: string }>)
          .map(att => ({
            filename: typeof att?.filename === 'string' ? att.filename : undefined,
            mime_type: typeof att?.mime_type === 'string' ? att.mime_type : undefined,
          }))
      : undefined
    return { content: msg.content, attachments }
  }
  return null
}

function userMessageSignature(message: AgentChatMessage): string | null {
  const payload = extractUserMessagePayload(message)
  if (!payload)
    return null
  return JSON.stringify({
    content: payload.content,
    attachments: payload.attachments?.map(att => ({
      filename: att.filename ?? '',
      mime_type: att.mime_type ?? '',
    })) ?? [],
  })
}

/** Reconstruct an AgentChatMessage from a persisted local message. */
function hydrateLocalMessage(p: PersistedLocalMessage): AgentChatMessage {
  const contentJson = JSON.stringify({
    content: p.contentText,
    ...(p.attachments && p.attachments.length > 0
      ? {
          attachments: p.attachments.map(att => ({
            ...(att.filename ? { filename: att.filename } : {}),
            ...(att.mime_type ? { mime_type: att.mime_type } : {}),
            ...(att.data ? { data: att.data } : {}),
          })),
        }
      : {}),
  })
  return {
    $typeName: 'leapmux.v1.AgentChatMessage' as const,
    id: p.id,
    role: MessageRole.USER,
    content: new TextEncoder().encode(contentJson),
    contentCompression: ContentCompression.NONE,
    seq: 0n,
    createdAt: p.createdAt,
    deliveryError: p.deliveryError,
    depth: 0,
    parentSpanId: '',
    spanId: '',
    spanLines: '[]',
  } as AgentChatMessage
}

export interface TodoItem {
  content: string
  status: 'pending' | 'in_progress' | 'completed'
  activeForm: string
}

export interface CommandStreamSegment {
  kind: 'output' | 'interaction' | 'reasoning_summary' | 'reasoning_content' | 'reasoning_summary_break'
  text: string
}

/** Max number of loaded messages to keep for the visible agent tab window. */
export const MAX_LOADED_CHAT_MESSAGES = 150
/** Max number of loaded messages to keep for hidden/background agent tabs. */
export const MAX_BACKGROUND_CHAT_MESSAGES = 50

const METHOD_TO_SEGMENT_KIND: Record<string, CommandStreamSegment['kind']> = {
  'item/commandExecution/terminalInteraction': 'interaction',
  'item/reasoning/summaryTextDelta': 'reasoning_summary',
  'item/reasoning/textDelta': 'reasoning_content',
  'item/reasoning/summaryPartAdded': 'reasoning_summary_break',
}

interface ChatStoreState {
  messagesByAgent: Record<string, AgentChatMessage[]>
  streamingText: Record<string, string>
  commandStreamsByAgent: Record<string, Record<string, CommandStreamSegment[]>>
  messageErrors: Record<string, string>
  /** Latest TodoWrite todos per agent, updated incrementally as messages arrive. */
  todosByAgent: Record<string, TodoItem[]>
  loading: boolean
  /** Whether there are older messages available to fetch (per agent). */
  hasMoreOlder: Record<string, boolean>
  /** Whether a fetch for older messages is in progress (per agent). */
  fetchingOlder: Record<string, boolean>
  /** For viewport restoration: scroll state saved when the user switched away. */
  savedViewportScroll: Record<string, { distFromBottom: number, atBottom: boolean }>
  /** Whether initial load has completed for an agent. */
  initialLoadComplete: Record<string, boolean>
  /** Monotonic counter incremented on every addMessage (including notification updates). */
  messageVersion: Record<string, number>
}

export function createChatStore() {
  const [state, setState] = createStore<ChatStoreState>({
    messagesByAgent: {},
    streamingText: {},
    commandStreamsByAgent: {},
    messageErrors: {},
    todosByAgent: {},
    loading: false,
    hasMoreOlder: {},
    fetchingOlder: {},
    savedViewportScroll: {},
    initialLoadComplete: {},
    messageVersion: {},
  })

  /** Non-reactive index of messages by spanId for tool_use ↔ tool_result lookup. */
  const spanIndex = new Map<string, Map<string, AgentChatMessage>>()
  /** Non-reactive index: maps spanId → last (tool_result) message for reverse lookup. */
  const spanResultIndex = new Map<string, Map<string, AgentChatMessage>>()

  /**
   * Index messages by spanId. The first message per spanId is stored in
   * spanIndex (the tool_use opener); subsequent messages (tool_result)
   * are stored in spanResultIndex.
   */
  function indexBySpanId(agentId: string, ...messages: AgentChatMessage[]) {
    let agentSpans = spanIndex.get(agentId)
    let agentResults = spanResultIndex.get(agentId)
    for (const msg of messages) {
      if (msg.spanId) {
        if (!agentSpans) {
          agentSpans = new Map()
          spanIndex.set(agentId, agentSpans)
        }
        if (!agentSpans.has(msg.spanId)) {
          agentSpans.set(msg.spanId, msg)
        }
        else {
          if (!agentResults) {
            agentResults = new Map()
            spanResultIndex.set(agentId, agentResults)
          }
          agentResults.set(msg.spanId, msg)
        }
      }
    }
  }

  /** Shared implementation for setMessages / loadInitialMessages. */
  function applyMessages(agentId: string, messages: AgentChatMessage[], hasMore: boolean) {
    // Clear stale span index entries before re-indexing to prevent memory leaks
    // when the message list is fully replaced (e.g. on reconnect or agent switch).
    spanIndex.delete(agentId)
    spanResultIndex.delete(agentId)
    // Index spans before setting messages so that reactive computations
    // triggered by the message list update can already look up tool_use
    // messages by spanId.
    indexBySpanId(agentId, ...messages)
    setState('messagesByAgent', agentId, messages)
    setState('hasMoreOlder', agentId, hasMore)
    setState('initialLoadComplete', agentId, true)
    for (const msg of messages) {
      if (msg.deliveryError) {
        setState('messageErrors', msg.id, msg.deliveryError)
      }
    }
    // Extract todos from the last TodoWrite message in the loaded history.
    const todos = findLatestTodos(messages)
    if (todos) {
      setState('todosByAgent', agentId, todos)
    }
  }

  return {
    state,

    getMessages(agentId: string): AgentChatMessage[] {
      return state.messagesByAgent[agentId] ?? []
    },

    getMessageBySpanId(agentId: string, spanId: string): AgentChatMessage | undefined {
      return spanIndex.get(agentId)?.get(spanId)
    },

    getToolResultBySpanId(agentId: string, spanId: string): AgentChatMessage | undefined {
      return spanResultIndex.get(agentId)?.get(spanId)
    },

    setMessages(agentId: string, messages: AgentChatMessage[], hasMore = false) {
      applyMessages(agentId, messages, hasMore)
    },

    addMessage(agentId: string, message: AgentChatMessage) {
      // Notification thread update: LEAPMUX notification messages can be updated
      // in-place when consolidating. Check if a message with this ID exists.
      const messages = state.messagesByAgent[agentId]
      const existingIdx = messages?.findLastIndex(m => m.id === message.id) ?? -1

      if (existingIdx !== -1) {
        const existing = messages![existingIdx]
        if (existing.seq === message.seq) {
          // Shallow-merge via path setter: preserves the store proxy reference
          // so <For> keeps the existing MessageBubble (local UI state survives).
          setState('messagesByAgent', agentId, existingIdx, message)
        }
        else {
          // Notification thread rows are updated in place on the backend but
          // receive a new seq. Reinsert them so the visible order follows seq.
          setState('messagesByAgent', agentId, (prev = []) => {
            const next = [...prev]
            next.splice(existingIdx, 1)

            // Local (optimistic) messages have seq === 0n and always stay at the end.
            if (message.seq === 0n) {
              next.push(message)
              return next
            }

            let serverEnd = next.length
            while (serverEnd > 0 && next[serverEnd - 1].seq === 0n)
              serverEnd--

            if (serverEnd === 0 || message.seq > next[serverEnd - 1].seq) {
              next.splice(serverEnd, 0, message)
              return next
            }

            let lo = 0
            let hi = serverEnd
            while (lo < hi) {
              const mid = (lo + hi) >>> 1
              if (next[mid].seq < message.seq)
                lo = mid + 1
              else
                hi = mid
            }
            next.splice(lo, 0, message)
            return next
          })
        }
      }
      else {
        // Reconcile optimistic local user messages before updating the store,
        // so the localStorage side-effect stays outside the setState updater.
        let reconciledLocalId: string | undefined
        if (message.role === MessageRole.USER) {
          const incomingSignature = userMessageSignature(message)
          if (incomingSignature) {
            const current = state.messagesByAgent[agentId] ?? []
            const local = current.find(candidate =>
              candidate.id.startsWith('local-')
              && candidate.role === MessageRole.USER
              && !candidate.deliveryError
              && userMessageSignature(candidate) === incomingSignature,
            )
            if (local)
              reconciledLocalId = local.id
          }
        }
        if (reconciledLocalId)
          removePersistedLocalMessage(agentId, reconciledLocalId)

        setState('messagesByAgent', agentId, (prev = []) => {
          if (reconciledLocalId) {
            const localIdx = prev.findIndex(m => m.id === reconciledLocalId)
            if (localIdx !== -1)
              return [...prev.slice(0, localIdx), message, ...prev.slice(localIdx + 1)]
          }

          // Local (optimistic) messages have seq === 0n and always go at the end.
          if (message.seq === 0n) {
            return [...prev, message]
          }

          // Find the boundary where trailing local messages start.
          let serverEnd = prev.length
          while (serverEnd > 0 && prev[serverEnd - 1].seq === 0n)
            serverEnd--

          // Dedup: skip if a server message with this exact seq already exists.
          for (let i = serverEnd - 1; i >= 0; i--) {
            if (prev[i].seq === message.seq)
              return prev
          }

          // Fast path: message is in order relative to the last server message.
          if (serverEnd === 0 || message.seq > prev[serverEnd - 1].seq) {
            return [...prev.slice(0, serverEnd), message, ...prev.slice(serverEnd)]
          }

          // Slow path: binary-insert among server messages [0, serverEnd).
          let lo = 0
          let hi = serverEnd
          while (lo < hi) {
            const mid = (lo + hi) >>> 1
            if (prev[mid].seq < message.seq)
              lo = mid + 1
            else
              hi = mid
          }
          return [...prev.slice(0, lo), message, ...prev.slice(lo)]
        })
      }

      // Index by spanId for tool_use ↔ tool_result lookup.
      indexBySpanId(agentId, message)

      // Track delivery errors
      if (message.deliveryError) {
        setState('messageErrors', message.id, message.deliveryError)
      }

      // Track latest TodoWrite
      const parsed = parseMessageContent(message)
      const todos = extractTodos(message, parsed)
      if (todos) {
        setState('todosByAgent', agentId, todos)
      }

      // Bump version so auto-scroll effects can detect notification updates
      // (which don't change messages.length).
      setState('messageVersion', agentId, (prev = 0) => prev + 1)
    },

    getLastSeq(agentId: string): bigint {
      const messages = state.messagesByAgent[agentId]
      if (!messages || messages.length === 0)
        return 0n
      // Skip trailing local messages (seq === 0n).
      for (let i = messages.length - 1; i >= 0; i--) {
        if (messages[i].seq !== 0n)
          return messages[i].seq
      }
      return 0n
    },

    setMessageError(messageId: string, error: string) {
      setState('messageErrors', messageId, error)
    },

    clearMessageError(messageId: string) {
      setState('messageErrors', messageId, undefined!)
    },

    removeMessage(agentId: string, messageId: string) {
      setState(
        'messagesByAgent',
        agentId,
        (prev = []) => prev.filter(m => m.id !== messageId),
      )
      setState('messageErrors', messageId, undefined!)
      if (messageId.startsWith('local-')) {
        removePersistedLocalMessage(agentId, messageId)
      }
    },

    /** Persist a local optimistic message to localStorage. */
    persistLocalMessage(
      agentId: string,
      messageId: string,
      contentText: string,
      deliveryError: string,
      attachments?: Array<{ filename?: string, mime_type?: string, data?: string }>,
    ) {
      persistLocalMessage(agentId, {
        id: messageId,
        contentText,
        createdAt: new Date().toISOString(),
        deliveryError,
        attachments,
      })
    },

    /** Load persisted local messages from localStorage and add them to the store. */
    loadLocalMessages(agentId: string) {
      const list = getPersistedLocalMessages(agentId)
      if (list.length === 0)
        return
      for (const p of list) {
        const msg = hydrateLocalMessage(p)
        this.addMessage(agentId, msg)
      }
    },

    setStreamingText(agentId: string, text: string) {
      setState('streamingText', agentId, text)
    },

    clearStreamingText(agentId: string) {
      setState('streamingText', agentId, '')
    },

    appendCommandStream(agentId: string, spanId: string, method: string, text: string) {
      if (!spanId)
        return
      const segmentKind: CommandStreamSegment['kind'] = METHOD_TO_SEGMENT_KIND[method] ?? 'output'
      if (!text && segmentKind !== 'reasoning_summary_break')
        return
      setState('commandStreamsByAgent', agentId, spanId, (prev = []) => {
        const last = prev.at(-1)
        if (segmentKind !== 'reasoning_summary_break' && last && last.kind === segmentKind) {
          return [
            ...prev.slice(0, -1),
            { kind: segmentKind, text: last.text + text },
          ]
        }
        return [...prev, { kind: segmentKind, text }]
      })
      setState('messageVersion', agentId, (prev = 0) => prev + 1)
    },

    getCommandStream(agentId: string, spanId: string): CommandStreamSegment[] {
      if (!spanId)
        return []
      return state.commandStreamsByAgent[agentId]?.[spanId] ?? []
    },

    clearCommandStream(agentId: string, spanId: string) {
      if (!spanId)
        return
      const streams = state.commandStreamsByAgent[agentId]
      if (!streams || !(spanId in streams))
        return
      setState('commandStreamsByAgent', agentId, spanId, undefined!)
      setState('messageVersion', agentId, (prev = 0) => prev + 1)
    },

    getTodos(agentId: string): TodoItem[] {
      return state.todosByAgent[agentId] ?? []
    },

    clearTodos(agentId: string) {
      setState('todosByAgent', agentId, [])
    },

    setLoading(loading: boolean) {
      setState('loading', loading)
    },

    /** Fetch the latest messages for an agent (initial page load). */
    async loadInitialMessages(workerId: string, agentId: string): Promise<void> {
      if (state.initialLoadComplete[agentId])
        return
      setState('fetchingOlder', agentId, true)
      try {
        const resp = await workerRpc.listAgentMessages(workerId, {
          agentId,
          limit: 50,
        })
        applyMessages(agentId, resp.messages, resp.hasMore)
      }
      finally {
        setState('fetchingOlder', agentId, false)
      }
      // Restore any local messages that were persisted to localStorage
      // (e.g. undelivered messages that survived a page refresh).
      this.loadLocalMessages(agentId)
    },

    /** Fetch older messages before the current window. */
    async loadOlderMessages(workerId: string, agentId: string): Promise<void> {
      if (state.fetchingOlder[agentId])
        return
      if (!state.hasMoreOlder[agentId])
        return
      const messages = state.messagesByAgent[agentId]
      if (!messages || messages.length === 0)
        return

      const firstSeq = messages[0].seq
      setState('fetchingOlder', agentId, true)
      try {
        const resp = await workerRpc.listAgentMessages(workerId, {
          agentId,
          beforeSeq: firstSeq,
          limit: 50,
        })
        if (resp.messages.length > 0) {
          setState('messagesByAgent', agentId, (prev = []) => {
            const existingSeqs = new Set(prev.map(m => m.seq))
            const newMsgs = resp.messages.filter(m => !existingSeqs.has(m.seq))
            return [...newMsgs, ...prev]
          })
          indexBySpanId(agentId, ...resp.messages)
          // Extract todos from older messages if none found yet.
          if (!state.todosByAgent[agentId] || state.todosByAgent[agentId].length === 0) {
            const todos = findLatestTodos(resp.messages)
            if (todos) {
              setState('todosByAgent', agentId, todos)
            }
          }
        }
        setState('hasMoreOlder', agentId, resp.hasMore)
      }
      finally {
        setState('fetchingOlder', agentId, false)
      }
    },

    /**
     * Fetch messages forward from a given seq, looping until all are retrieved.
     * Used after WatchEvents catch-up replay to fill any gap beyond the 50-message replay limit.
     */
    async loadNewerMessages(workerId: string, agentId: string, afterSeq: bigint, signal?: AbortSignal): Promise<void> {
      let cursor = afterSeq
      while (!signal?.aborted) {
        const resp = await workerRpc.listAgentMessages(workerId, {
          agentId,
          afterSeq: cursor,
          limit: 50,
        })
        for (const msg of resp.messages) {
          this.addMessage(agentId, msg)
        }
        if (!resp.hasMore || resp.messages.length === 0)
          break
        cursor = resp.messages.at(-1)!.seq
      }
    },

    /** Trim oldest messages when total exceeds threshold. Sets hasMoreOlder=true. */
    trimOldMessages(agentId: string, maxCount: number) {
      setState('messagesByAgent', agentId, (prev = []) => {
        if (prev.length <= maxCount)
          return prev
        return prev.slice(prev.length - maxCount)
      })
      setState('hasMoreOlder', agentId, true)
    },

    /** Get the seq of the first message in the current window. */
    getFirstSeq(agentId: string): bigint {
      const messages = state.messagesByAgent[agentId]
      if (!messages || messages.length === 0)
        return 0n
      // Skip leading local messages (seq === 0n).
      for (let i = 0; i < messages.length; i++) {
        if (messages[i].seq !== 0n)
          return messages[i].seq
      }
      return 0n
    },

    hasOlderMessages(agentId: string): boolean {
      return state.hasMoreOlder[agentId] ?? false
    },

    isFetchingOlder(agentId: string): boolean {
      return state.fetchingOlder[agentId] ?? false
    },

    isInitialLoadComplete(agentId: string): boolean {
      return state.initialLoadComplete[agentId] ?? false
    },

    getMessageVersion(agentId: string): number {
      return state.messageVersion[agentId] ?? 0
    },

    /** Save scroll state for viewport restoration on tab switch. */
    saveViewportScroll(agentId: string, distFromBottom: number, atBottom: boolean) {
      setState('savedViewportScroll', agentId, { distFromBottom, atBottom })
    },

    getSavedViewportScroll(agentId: string): { distFromBottom: number, atBottom: boolean } | undefined {
      return state.savedViewportScroll[agentId]
    },

    clearSavedViewportScroll(agentId: string) {
      setState('savedViewportScroll', agentId, undefined!)
    },
  }
}
