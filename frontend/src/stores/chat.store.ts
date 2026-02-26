import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { createStore } from 'solid-js/store'
import { agentClient } from '~/api/clients'
import { extractTodos, findLatestTodos, parseMessageContent } from '~/lib/messageParser'

export interface TodoItem {
  content: string
  status: 'pending' | 'in_progress' | 'completed'
  activeForm: string
}

interface ChatStoreState {
  messagesByAgent: Record<string, AgentChatMessage[]>
  streamingText: Record<string, string>
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
}

export function createChatStore() {
  const [state, setState] = createStore<ChatStoreState>({
    messagesByAgent: {},
    streamingText: {},
    messageErrors: {},
    todosByAgent: {},
    loading: false,
    hasMoreOlder: {},
    fetchingOlder: {},
    savedViewportScroll: {},
    initialLoadComplete: {},
  })

  /** Shared implementation for setMessages / loadInitialMessages. */
  function applyMessages(agentId: string, messages: AgentChatMessage[], hasMore: boolean) {
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

    setMessages(agentId: string, messages: AgentChatMessage[], hasMore = false) {
      applyMessages(agentId, messages, hasMore)
    },

    addMessage(agentId: string, message: AgentChatMessage) {
      setState('messagesByAgent', agentId, (prev = []) => {
        // Check if a message with this ID already exists (thread merge:
        // parent gets updated with child content and bumped seq).
        // Search from end — thread merges almost always affect recent messages.
        const existingIdx = prev.findLastIndex(m => m.id === message.id)
        if (existingIdx !== -1) {
          // Update in-place to avoid advancing lastSeq past messages
          // that haven't arrived yet (which would cause the seq-based
          // dedup below to incorrectly drop them).
          const updated = [...prev]
          updated[existingIdx] = message
          return updated
        }

        // Fast path: message is in order (most common case).
        if (prev.length === 0 || message.seq > prev[prev.length - 1].seq) {
          return [...prev, message]
        }

        // Dedup: skip if a message with this exact seq already exists.
        if (prev.findLastIndex(m => m.seq === message.seq) !== -1)
          return prev

        // Out-of-order message (e.g. catch-up replay after a thread merge
        // advanced lastSeq): insert in sorted position by seq.
        const insertAfter = prev.findLastIndex(m => m.seq < message.seq)
        const insertIdx = insertAfter + 1
        return [...prev.slice(0, insertIdx), message, ...prev.slice(insertIdx)]
      })

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
    },

    getLastSeq(agentId: string): bigint {
      const messages = state.messagesByAgent[agentId]
      if (!messages || messages.length === 0)
        return 0n
      return messages[messages.length - 1].seq
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
    },

    setStreamingText(agentId: string, text: string) {
      setState('streamingText', agentId, text)
    },

    clearStreamingText(agentId: string) {
      setState('streamingText', agentId, '')
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
    async loadInitialMessages(agentId: string): Promise<void> {
      if (state.initialLoadComplete[agentId])
        return
      setState('fetchingOlder', agentId, true)
      try {
        const resp = await agentClient.listAgentMessages({
          agentId,
          limit: 50,
        })
        applyMessages(agentId, resp.messages, resp.hasMore)
      }
      finally {
        setState('fetchingOlder', agentId, false)
      }
    },

    /** Fetch older messages before the current window. */
    async loadOlderMessages(agentId: string): Promise<void> {
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
        const resp = await agentClient.listAgentMessages({
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
      return messages[0].seq
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
