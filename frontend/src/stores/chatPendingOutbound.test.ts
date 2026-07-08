import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createPendingOutboundStore } from '~/stores/chatPendingOutbound'

function msg(localId: string, content: string) {
  return { localId, content, attachments: [] }
}

describe('chatPendingOutbound', () => {
  it('take returns [] for an agent with no queued messages', () =>
    createRoot((dispose) => {
      const store = createPendingOutboundStore()
      expect(store.take('a1')).toEqual([])
      dispose()
    }))

  it('enqueue accumulates in order; take drains and clears', () =>
    createRoot((dispose) => {
      const store = createPendingOutboundStore()
      store.enqueue('a1', msg('l1', 'one'))
      store.enqueue('a1', msg('l2', 'two'))
      expect(store.take('a1').map(m => m.localId)).toEqual(['l1', 'l2'])
      // Drained: a second take is empty.
      expect(store.take('a1')).toEqual([])
      dispose()
    }))

  it('keeps queues isolated per agent', () =>
    createRoot((dispose) => {
      const store = createPendingOutboundStore()
      store.enqueue('a1', msg('l1', 'a'))
      store.enqueue('a2', msg('l2', 'b'))
      expect(store.take('a1').map(m => m.localId)).toEqual(['l1'])
      expect(store.take('a2').map(m => m.localId)).toEqual(['l2'])
      dispose()
    }))
})
