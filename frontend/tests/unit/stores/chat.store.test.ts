import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, MessageRole } from '~/generated/leapmux/v1/agent_pb'
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
  })
})
