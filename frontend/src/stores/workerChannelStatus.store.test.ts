import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ChannelError } from '~/lib/channel'
import { createWorkerChannelStatusStore } from './workerChannelStatus.store'

// Minimal mock ChannelManager with observability hooks
function createMockChannelManager() {
  const stateCallbacks = new Set<() => void>()
  const errorCallbacks = new Set<(workerId: string, error: ChannelError) => void>()
  const openChannels = new Set<string>()

  return {
    onStateChange(cb: () => void) {
      stateCallbacks.add(cb)
      return () => {
        stateCallbacks.delete(cb)
      }
    },
    onChannelError(cb: (workerId: string, error: ChannelError) => void) {
      errorCallbacks.add(cb)
      return () => {
        errorCallbacks.delete(cb)
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
    _emitError(workerId: string) {
      const err = new ChannelError('rpc', 'test error')
      for (const cb of errorCallbacks) cb(workerId, err)
    },
  }
}

describe('workerChannelStatusStore', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('should start workers as disconnected', () => {
    createRoot((dispose) => {
      const cm = createMockChannelManager()
      const store = createWorkerChannelStatusStore(cm as any)
      store.setWorkerIds(['w1', 'w2'])

      expect(store.getStatus('w1')).toBe('disconnected')
      expect(store.getStatus('w2')).toBe('disconnected')
      dispose()
    })
  })

  it('should transition to connected when channel opens', () => {
    createRoot((dispose) => {
      const cm = createMockChannelManager()
      const store = createWorkerChannelStatusStore(cm as any)
      store.setWorkerIds(['w1'])

      cm._setOpen('w1')
      expect(store.getStatus('w1')).toBe('connected')
      dispose()
    })
  })

  it('should transition to disconnected when channel closes', () => {
    createRoot((dispose) => {
      const cm = createMockChannelManager()
      const store = createWorkerChannelStatusStore(cm as any)
      store.setWorkerIds(['w1'])

      cm._setOpen('w1')
      expect(store.getStatus('w1')).toBe('connected')

      cm._setClosed('w1')
      expect(store.getStatus('w1')).toBe('disconnected')
      dispose()
    })
  })

  it('should transition to warning on non-transport error', () => {
    createRoot((dispose) => {
      const cm = createMockChannelManager()
      const store = createWorkerChannelStatusStore(cm as any)
      store.setWorkerIds(['w1'])

      cm._setOpen('w1')
      cm._emitError('w1')
      expect(store.getStatus('w1')).toBe('warning')
      dispose()
    })
  })

  it('should expire warning back to connected after 10s', () => {
    createRoot((dispose) => {
      const cm = createMockChannelManager()
      const store = createWorkerChannelStatusStore(cm as any)
      store.setWorkerIds(['w1'])

      cm._setOpen('w1')
      cm._emitError('w1')
      expect(store.getStatus('w1')).toBe('warning')

      vi.advanceTimersByTime(10_000)
      expect(store.getStatus('w1')).toBe('connected')
      dispose()
    })
  })

  it('should expire warning to disconnected if channel closed', () => {
    createRoot((dispose) => {
      const cm = createMockChannelManager()
      const store = createWorkerChannelStatusStore(cm as any)
      store.setWorkerIds(['w1'])

      cm._setOpen('w1')
      cm._emitError('w1')
      cm._setClosed('w1')
      // Still warning because error hasn't expired yet
      // (but channel is closed, so after expiry it should be disconnected)
      // Actually after _setClosed, the state refresh happens and
      // since there's still a warning entry, it stays 'warning'
      expect(store.getStatus('w1')).toBe('warning')

      vi.advanceTimersByTime(10_000)
      expect(store.getStatus('w1')).toBe('disconnected')
      dispose()
    })
  })

  it('should track multiple workers independently', () => {
    createRoot((dispose) => {
      const cm = createMockChannelManager()
      const store = createWorkerChannelStatusStore(cm as any)
      store.setWorkerIds(['w1', 'w2'])

      cm._setOpen('w1')
      expect(store.getStatus('w1')).toBe('connected')
      expect(store.getStatus('w2')).toBe('disconnected')

      cm._setOpen('w2')
      expect(store.getStatus('w1')).toBe('connected')
      expect(store.getStatus('w2')).toBe('connected')

      cm._emitError('w1')
      expect(store.getStatus('w1')).toBe('warning')
      expect(store.getStatus('w2')).toBe('connected')
      dispose()
    })
  })
})
