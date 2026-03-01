import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, ContentCompression, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { createChatStore } from '~/stores/chat.store'

// Mock agentClient for loadInitialMessages / loadOlderMessages
const mockListAgentMessages = vi.fn()
vi.mock('~/api/clients', () => ({
  agentClient: {
    listAgentMessages: (...args: unknown[]) => mockListAgentMessages(...args),
  },
}))

function makeMessage(id: string, seq: bigint, deliveryError = '') {
  return create(AgentChatMessageSchema, {
    id,
    role: MessageRole.USER,
    content: new TextEncoder().encode(`{"content":"test"}`),
    seq,
    deliveryError,
  })
}

/** Build a wrapped assistant message containing a TodoWrite tool_use. */
function makeTodoWriteMessage(
  id: string,
  seq: bigint,
  todos: Array<{ content: string, status: string, activeForm: string }>,
) {
  const wrapped = {
    old_seqs: [],
    messages: [{
      type: 'assistant',
      message: {
        content: [{
          type: 'tool_use',
          name: 'TodoWrite',
          input: { todos },
        }],
      },
    }],
  }
  return create(AgentChatMessageSchema, {
    id,
    role: MessageRole.ASSISTANT,
    content: new TextEncoder().encode(JSON.stringify(wrapped)),
    contentCompression: ContentCompression.NONE,
    seq,
  })
}

describe('createChatStore', () => {
  it('should initialize with empty state', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      expect(store.state.messagesByAgent).toEqual({})
      expect(store.state.messageErrors).toEqual({})
      expect(store.state.loading).toBe(false)
      dispose()
    })
  })

  it('should return empty array for unknown agent', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      expect(store.getMessages('unknown')).toEqual([])
      dispose()
    })
  })

  it('should set and clear streaming text', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.setStreamingText('a1', 'Hello')
      expect(store.state.streamingText.a1).toBe('Hello')
      store.clearStreamingText('a1')
      expect(store.state.streamingText.a1).toBe('')
      dispose()
    })
  })

  it('should set and clear message errors', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.setMessageError('msg1', 'offline')
      expect(store.state.messageErrors.msg1).toBe('offline')
      store.clearMessageError('msg1')
      expect(store.state.messageErrors.msg1).toBeUndefined()
      dispose()
    })
  })

  it('should seed messageErrors from deliveryError in addMessage', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.addMessage('agent1', makeMessage('msg1', 1n, 'worker offline'))
      expect(store.state.messageErrors.msg1).toBe('worker offline')
      dispose()
    })
  })

  it('should update message in-place on thread merge (same ID)', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.addMessage('agent1', makeMessage('msg1', 1n))
      store.addMessage('agent1', makeMessage('msg2', 2n))

      // Thread merge: same ID as msg1, bumped seq
      const merged = makeMessage('msg1', 3n)
      store.addMessage('agent1', merged)

      const msgs = store.getMessages('agent1')
      expect(msgs).toHaveLength(2)
      // The merged message should be at its original position with updated seq
      expect(msgs[0].id).toBe('msg1')
      expect(msgs[0].seq).toBe(3n)
      expect(msgs[1].id).toBe('msg2')
      dispose()
    })
  })

  it('should not set error for message without deliveryError', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.addMessage('agent1', makeMessage('msg2', 1n, ''))
      expect(store.state.messageErrors.msg2).toBeUndefined()
      dispose()
    })
  })

  it('should seed messageErrors from deliveryError in setMessages', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.setMessages('agent1', [
        makeMessage('msg1', 1n, 'error1'),
        makeMessage('msg2', 2n, ''),
      ])
      expect(store.state.messageErrors.msg1).toBe('error1')
      expect(store.state.messageErrors.msg2).toBeUndefined()
      dispose()
    })
  })

  it('should remove message and clear error on removeMessage', () => {
    createRoot((dispose) => {
      const store = createChatStore()
      store.addMessage('agent1', makeMessage('msg1', 1n, 'error'))
      expect(store.getMessages('agent1')).toHaveLength(1)
      expect(store.state.messageErrors.msg1).toBe('error')

      store.removeMessage('agent1', 'msg1')
      expect(store.getMessages('agent1')).toHaveLength(0)
      expect(store.state.messageErrors.msg1).toBeUndefined()
      dispose()
    })
  })

  describe('windowed pagination', () => {
    describe('setMessages with hasMore', () => {
      it('should set hasMoreOlder and initialLoadComplete', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m2', 2n)], true)
          expect(store.hasOlderMessages('a1')).toBe(true)
          expect(store.isInitialLoadComplete('a1')).toBe(true)
          dispose()
        })
      })

      it('should default hasMore to false', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)])
          expect(store.hasOlderMessages('a1')).toBe(false)
          expect(store.isInitialLoadComplete('a1')).toBe(true)
          dispose()
        })
      })
    })

    describe('getFirstSeq', () => {
      it('should return first message seq', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 5n), makeMessage('m2', 6n)])
          expect(store.getFirstSeq('a1')).toBe(5n)
          dispose()
        })
      })

      it('should return 0n for empty agent', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          expect(store.getFirstSeq('a1')).toBe(0n)
          dispose()
        })
      })
    })

    describe('getLastSeq', () => {
      it('should return last message seq', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 5n), makeMessage('m2', 10n)])
          expect(store.getLastSeq('a1')).toBe(10n)
          dispose()
        })
      })

      it('should return 0n for empty agent', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          expect(store.getLastSeq('a1')).toBe(0n)
          dispose()
        })
      })
    })

    describe('trimOldMessages', () => {
      it('should trim oldest messages when exceeding threshold', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          const messages = Array.from({ length: 200 }, (_, i) =>
            makeMessage(`m${i}`, BigInt(i + 1)))
          store.setMessages('a1', messages)
          expect(store.getMessages('a1')).toHaveLength(200)

          store.trimOldMessages('a1', 150)
          const trimmed = store.getMessages('a1')
          expect(trimmed).toHaveLength(150)
          // Should keep the latest 150 (seq 51-200)
          expect(trimmed[0].seq).toBe(51n)
          expect(trimmed[trimmed.length - 1].seq).toBe(200n)
          expect(store.hasOlderMessages('a1')).toBe(true)
          dispose()
        })
      })

      it('should not trim when below threshold', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          const messages = Array.from({ length: 100 }, (_, i) =>
            makeMessage(`m${i}`, BigInt(i + 1)))
          store.setMessages('a1', messages)
          store.trimOldMessages('a1', 150)
          expect(store.getMessages('a1')).toHaveLength(100)
          dispose()
        })
      })
    })

    describe('viewport save/restore', () => {
      it('should save and retrieve viewport scroll state', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.saveViewportScroll('a1', 120, false)
          const saved = store.getSavedViewportScroll('a1')
          expect(saved).toEqual({ distFromBottom: 120, atBottom: false })
          dispose()
        })
      })

      it('should save at-bottom state', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.saveViewportScroll('a1', 0, true)
          const saved = store.getSavedViewportScroll('a1')
          expect(saved).toEqual({ distFromBottom: 0, atBottom: true })
          dispose()
        })
      })

      it('should clear saved viewport scroll state', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          store.saveViewportScroll('a1', 120, false)
          store.clearSavedViewportScroll('a1')
          expect(store.getSavedViewportScroll('a1')).toBeUndefined()
          dispose()
        })
      })

      it('should return undefined for unsaved agent', () => {
        createRoot((dispose) => {
          const store = createChatStore()
          expect(store.getSavedViewportScroll('unknown')).toBeUndefined()
          dispose()
        })
      })
    })

    describe('loadInitialMessages', () => {
      it('should fetch and set messages with hasMore', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          const messages = Array.from({ length: 50 }, (_, i) =>
            makeMessage(`m${i}`, BigInt(i + 1)))
          mockListAgentMessages.mockResolvedValueOnce({ messages, hasMore: true })

          await store.loadInitialMessages('a1')
          expect(store.getMessages('a1')).toHaveLength(50)
          expect(store.hasOlderMessages('a1')).toBe(true)
          expect(store.isInitialLoadComplete('a1')).toBe(true)
          expect(mockListAgentMessages).toHaveBeenCalledWith({ agentId: 'a1', limit: 50 })
          dispose()
        })
      })

      it('should skip if already loaded', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          mockListAgentMessages.mockResolvedValueOnce({ messages: [makeMessage('m1', 1n)], hasMore: false })

          await store.loadInitialMessages('a1')
          const callCount = mockListAgentMessages.mock.calls.length

          await store.loadInitialMessages('a1')
          expect(mockListAgentMessages.mock.calls.length).toBe(callCount) // No new call
          dispose()
        })
      })

      it('should track fetching state', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          let resolveFn: (value: unknown) => void
          const promise = new Promise(resolve => (resolveFn = resolve))
          mockListAgentMessages.mockReturnValueOnce(promise)

          const loadPromise = store.loadInitialMessages('a1')
          expect(store.isFetchingOlder('a1')).toBe(true)

          resolveFn!({ messages: [], hasMore: false })
          await loadPromise

          expect(store.isFetchingOlder('a1')).toBe(false)
          dispose()
        })
      })
    })

    describe('loadOlderMessages', () => {
      it('should prepend older messages', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          // Set initial messages (seq 51-100)
          const initial = Array.from({ length: 50 }, (_, i) =>
            makeMessage(`m${i + 50}`, BigInt(i + 51)))
          store.setMessages('a1', initial, true)

          // Mock older messages (seq 1-50)
          const older = Array.from({ length: 50 }, (_, i) =>
            makeMessage(`m${i}`, BigInt(i + 1)))
          mockListAgentMessages.mockResolvedValueOnce({ messages: older, hasMore: false })

          await store.loadOlderMessages('a1')
          const msgs = store.getMessages('a1')
          expect(msgs).toHaveLength(100)
          expect(msgs[0].seq).toBe(1n)
          expect(msgs[99].seq).toBe(100n)
          expect(store.hasOlderMessages('a1')).toBe(false)
          expect(mockListAgentMessages).toHaveBeenCalledWith({
            agentId: 'a1',
            beforeSeq: 51n,
            limit: 50,
          })
          dispose()
        })
      })

      it('should not fetch when hasMoreOlder is false', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)], false)

          const callCount = mockListAgentMessages.mock.calls.length
          await store.loadOlderMessages('a1')
          expect(mockListAgentMessages.mock.calls.length).toBe(callCount)
          dispose()
        })
      })

      it('should not fetch when already fetching', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          const initial = [makeMessage('m1', 10n)]
          store.setMessages('a1', initial, true)

          let resolveFn: (value: unknown) => void
          const promise = new Promise(resolve => (resolveFn = resolve))
          mockListAgentMessages.mockReturnValueOnce(promise)

          const loadPromise = store.loadOlderMessages('a1')
          const callCount = mockListAgentMessages.mock.calls.length

          // Second call should be a no-op
          await store.loadOlderMessages('a1')
          expect(mockListAgentMessages.mock.calls.length).toBe(callCount)

          resolveFn!({ messages: [], hasMore: false })
          await loadPromise
          dispose()
        })
      })

      it('should deduplicate overlapping seqs', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m5', 5n), makeMessage('m6', 6n)], true)

          // Return messages that overlap with existing ones
          const older = [makeMessage('m4', 4n), makeMessage('m5_dup', 5n)]
          mockListAgentMessages.mockResolvedValueOnce({ messages: older, hasMore: false })

          await store.loadOlderMessages('a1')
          const msgs = store.getMessages('a1')
          // Should have m4, m5, m6 — not m5_dup
          expect(msgs).toHaveLength(3)
          expect(msgs[0].seq).toBe(4n)
          expect(msgs[1].seq).toBe(5n)
          expect(msgs[2].seq).toBe(6n)
          dispose()
        })
      })
    })

    describe('loadNewerMessages', () => {
      it('should fetch a single batch of newer messages', async () => {
        await createRoot(async (dispose) => {
          const store = createChatStore()
          // Pre-load messages seq 1-50
          const initial = Array.from({ length: 50 }, (_, i) =>
            makeMessage(`m${i + 1}`, BigInt(i + 1)))
          store.setMessages('a1', initial)

          // Mock returns seq 51-75 with hasMore: false
          const newer = Array.from({ length: 25 }, (_, i) =>
            makeMessage(`m${i + 51}`, BigInt(i + 51)))
          mockListAgentMessages.mockResolvedValueOnce({ messages: newer, hasMore: false })

          await store.loadNewerMessages('a1', 50n)
          const msgs = store.getMessages('a1')
          expect(msgs).toHaveLength(75)
          expect(msgs[0].seq).toBe(1n)
          expect(msgs[74].seq).toBe(75n)
          expect(mockListAgentMessages).toHaveBeenLastCalledWith({
            agentId: 'a1',
            afterSeq: 50n,
            limit: 50,
          })
          dispose()
        })
      })

      it('should fetch multiple batches until hasMore is false', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          // Pre-load messages seq 1-50
          const initial = Array.from({ length: 50 }, (_, i) =>
            makeMessage(`m${i + 1}`, BigInt(i + 1)))
          store.setMessages('a1', initial)

          // First batch: seq 51-100, hasMore: true
          const batch1 = Array.from({ length: 50 }, (_, i) =>
            makeMessage(`m${i + 51}`, BigInt(i + 51)))
          mockListAgentMessages.mockResolvedValueOnce({ messages: batch1, hasMore: true })

          // Second batch: seq 101-120, hasMore: false
          const batch2 = Array.from({ length: 20 }, (_, i) =>
            makeMessage(`m${i + 101}`, BigInt(i + 101)))
          mockListAgentMessages.mockResolvedValueOnce({ messages: batch2, hasMore: false })

          await store.loadNewerMessages('a1', 50n)
          const msgs = store.getMessages('a1')
          expect(msgs).toHaveLength(120)
          expect(msgs[0].seq).toBe(1n)
          expect(msgs[119].seq).toBe(120n)

          // Verify two calls with correct cursors
          expect(mockListAgentMessages).toHaveBeenCalledTimes(2)
          expect(mockListAgentMessages).toHaveBeenNthCalledWith(1, {
            agentId: 'a1',
            afterSeq: 50n,
            limit: 50,
          })
          expect(mockListAgentMessages).toHaveBeenNthCalledWith(2, {
            agentId: 'a1',
            afterSeq: 100n,
            limit: 50,
          })
          dispose()
        })
      })

      it('should stop when abort signal is triggered', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)])

          const controller = new AbortController()

          // First batch returns hasMore: true but we abort before the second
          const batch1 = Array.from({ length: 50 }, (_, i) =>
            makeMessage(`m${i + 2}`, BigInt(i + 2)))
          mockListAgentMessages.mockImplementation(async () => {
            // Abort after the first call completes
            controller.abort()
            return { messages: batch1, hasMore: true }
          })

          await store.loadNewerMessages('a1', 1n, controller.signal)

          // Should have made only one call because signal was aborted
          expect(mockListAgentMessages).toHaveBeenCalledTimes(1)
          dispose()
        })
      })

      it('should handle no new messages', async () => {
        await createRoot(async (dispose) => {
          mockListAgentMessages.mockClear()
          const store = createChatStore()
          store.setMessages('a1', [makeMessage('m1', 1n)])

          mockListAgentMessages.mockResolvedValueOnce({ messages: [], hasMore: false })

          await store.loadNewerMessages('a1', 1n)
          expect(store.getMessages('a1')).toHaveLength(1)
          expect(mockListAgentMessages).toHaveBeenCalledTimes(1)
          dispose()
        })
      })
    })
  })

  describe('todo extraction', () => {
    const sampleTodos = [
      { content: 'Write tests', status: 'completed', activeForm: 'Writing tests' },
      { content: 'Deploy', status: 'in_progress', activeForm: 'Deploying' },
    ]

    it('should extract todos from setMessages', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        store.setMessages('a1', [
          makeMessage('m1', 1n),
          makeTodoWriteMessage('m2', 2n, sampleTodos),
          makeMessage('m3', 3n),
        ])
        expect(store.getTodos('a1')).toEqual(sampleTodos)
        dispose()
      })
    })

    it('should extract todos from addMessage', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        store.addMessage('a1', makeTodoWriteMessage('m1', 1n, sampleTodos))
        expect(store.getTodos('a1')).toEqual(sampleTodos)
        dispose()
      })
    })

    it('should extract todos from loadInitialMessages', async () => {
      await createRoot(async (dispose) => {
        const store = createChatStore()
        const messages = [
          makeMessage('m1', 1n),
          makeTodoWriteMessage('m2', 2n, sampleTodos),
          makeMessage('m3', 3n),
        ]
        mockListAgentMessages.mockResolvedValueOnce({ messages, hasMore: false })

        await store.loadInitialMessages('a1')
        expect(store.getTodos('a1')).toEqual(sampleTodos)
        dispose()
      })
    })

    it('should extract todos from loadOlderMessages when none found yet', async () => {
      await createRoot(async (dispose) => {
        const store = createChatStore()
        // Initial messages have no TodoWrite
        store.setMessages('a1', [makeMessage('m10', 10n)], true)
        expect(store.getTodos('a1')).toEqual([])

        // Older messages contain a TodoWrite
        const older = [makeTodoWriteMessage('m5', 5n, sampleTodos)]
        mockListAgentMessages.mockResolvedValueOnce({ messages: older, hasMore: false })

        await store.loadOlderMessages('a1')
        expect(store.getTodos('a1')).toEqual(sampleTodos)
        dispose()
      })
    })

    it('should not overwrite existing todos from loadOlderMessages', async () => {
      await createRoot(async (dispose) => {
        const store = createChatStore()
        const newerTodos = [{ content: 'New task', status: 'pending', activeForm: 'New' }]
        // Initial messages have a TodoWrite
        store.setMessages('a1', [makeTodoWriteMessage('m10', 10n, newerTodos)], true)
        expect(store.getTodos('a1')).toEqual(newerTodos)

        // Older messages also contain a TodoWrite — should NOT overwrite
        const olderTodos = [{ content: 'Old task', status: 'completed', activeForm: 'Old' }]
        const older = [makeTodoWriteMessage('m5', 5n, olderTodos)]
        mockListAgentMessages.mockResolvedValueOnce({ messages: older, hasMore: false })

        await store.loadOlderMessages('a1')
        expect(store.getTodos('a1')).toEqual(newerTodos)
        dispose()
      })
    })

    it('should return empty array for agent with no todos', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        store.setMessages('a1', [makeMessage('m1', 1n)])
        expect(store.getTodos('a1')).toEqual([])
        dispose()
      })
    })
  })

  describe('messageVersion', () => {
    it('should return 0 for unknown agent', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        expect(store.getMessageVersion('unknown')).toBe(0)
        dispose()
      })
    })

    it('should increment on addMessage with a new message', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        expect(store.getMessageVersion('a1')).toBe(0)

        store.addMessage('a1', makeMessage('m1', 1n))
        expect(store.getMessageVersion('a1')).toBe(1)

        store.addMessage('a1', makeMessage('m2', 2n))
        expect(store.getMessageVersion('a1')).toBe(2)
        dispose()
      })
    })

    it('should increment on thread merge (same ID, updated content)', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        store.addMessage('a1', makeMessage('m1', 1n))
        expect(store.getMessageVersion('a1')).toBe(1)

        // Thread merge: same ID as m1, bumped seq
        store.addMessage('a1', makeMessage('m1', 3n))
        expect(store.getMessageVersion('a1')).toBe(2)
        dispose()
      })
    })

    it('should not increment on setMessages', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        store.setMessages('a1', [makeMessage('m1', 1n), makeMessage('m2', 2n)])
        expect(store.getMessageVersion('a1')).toBe(0)
        dispose()
      })
    })

    it('should track versions independently per agent', () => {
      createRoot((dispose) => {
        const store = createChatStore()
        store.addMessage('a1', makeMessage('m1', 1n))
        store.addMessage('a2', makeMessage('m2', 2n))
        store.addMessage('a1', makeMessage('m3', 3n))

        expect(store.getMessageVersion('a1')).toBe(2)
        expect(store.getMessageVersion('a2')).toBe(1)
        dispose()
      })
    })
  })
})
