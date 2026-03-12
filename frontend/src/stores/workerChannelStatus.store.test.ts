import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createWorkerChannelStatusStore } from './workerChannelStatus.store'

// Minimal mock ChannelManager with observability hooks
function createMockChannelManager() {
  const stateCallbacks = new Set<() => void>()
  const openChannels = new Set<string>()

  return {
    onStateChange(cb: () => void) {
      stateCallbacks.add(cb)
      return () => {
        stateCallbacks.delete(cb)
      }
    },
    hasOpenChannel(workerId: string) {
      return openChannels.has(workerId)
    },
    // Test helpers
    _setOpen(workerId: string) {
      openChannels.add(workerId)
      for (const cb of stateCallbacks) cb()
    },
    _setClosed(workerId: string) {
      openChannels.delete(workerId)
      for (const cb of stateCallbacks) cb()
    },
  }
}

describe('workerChannelStatusStore', () => {
  it('should report disconnected when no channel is open', () => {
    createRoot((dispose) => {
      const cm = createMockChannelManager()
      const store = createWorkerChannelStatusStore(cm as any)

      expect(store.getStatus('w1')).toBe('disconnected')
      expect(store.getStatus('w2')).toBe('disconnected')
      dispose()
    })
  })

  it('should transition to connected when channel opens', () => {
    createRoot((dispose) => {
      const cm = createMockChannelManager()
      const store = createWorkerChannelStatusStore(cm as any)

      cm._setOpen('w1')
      expect(store.getStatus('w1')).toBe('connected')
      dispose()
    })
  })

  it('should transition to disconnected when channel closes', () => {
    createRoot((dispose) => {
      const cm = createMockChannelManager()
      const store = createWorkerChannelStatusStore(cm as any)

      cm._setOpen('w1')
      expect(store.getStatus('w1')).toBe('connected')

      cm._setClosed('w1')
      expect(store.getStatus('w1')).toBe('disconnected')
      dispose()
    })
  })

  it('should track multiple workers independently', () => {
    createRoot((dispose) => {
      const cm = createMockChannelManager()
      const store = createWorkerChannelStatusStore(cm as any)

      cm._setOpen('w1')
      expect(store.getStatus('w1')).toBe('connected')
      expect(store.getStatus('w2')).toBe('disconnected')

      cm._setOpen('w2')
      expect(store.getStatus('w1')).toBe('connected')
      expect(store.getStatus('w2')).toBe('connected')
      dispose()
    })
  })
})
