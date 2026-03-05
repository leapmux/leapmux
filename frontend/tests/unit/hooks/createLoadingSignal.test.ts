import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'

describe('createLoadingSignal', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('should start in non-loading state', () => {
    createRoot((dispose) => {
      const signal = createLoadingSignal()
      expect(signal.loading()).toBe(false)
      dispose()
    })
  })

  it('should be loading after start()', () => {
    createRoot((dispose) => {
      const signal = createLoadingSignal()
      signal.start()
      expect(signal.loading()).toBe(true)
      dispose()
    })
  })

  it('should remain loading during debounce window even after stop()', () => {
    createRoot((dispose) => {
      const signal = createLoadingSignal()
      signal.start()
      expect(signal.loading()).toBe(true)

      // Call stop() immediately — within the debounce window.
      // loading() should still be true because the debounce hasn't fired yet.
      signal.stop()
      expect(signal.loading()).toBe(true)

      // After the debounce period (1000ms), the deferred stop takes effect.
      vi.advanceTimersByTime(1000)
      expect(signal.loading()).toBe(false)
      dispose()
    })
  })

  it('should stop immediately when debounce has already elapsed', () => {
    createRoot((dispose) => {
      const signal = createLoadingSignal()
      signal.start()
      expect(signal.loading()).toBe(true)

      // Advance past the debounce window.
      vi.advanceTimersByTime(1000)
      expect(signal.loading()).toBe(true)

      // Now stop() should take effect immediately.
      signal.stop()
      expect(signal.loading()).toBe(false)
      dispose()
    })
  })

  it('should auto-stop after timeout', () => {
    createRoot((dispose) => {
      const signal = createLoadingSignal(5000)
      signal.start()
      expect(signal.loading()).toBe(true)

      vi.advanceTimersByTime(5000)
      expect(signal.loading()).toBe(false)
      dispose()
    })
  })

  it('loading() returns true during debounce — guards statusChange from overwriting settings', () => {
    // This test validates the invariant used by the statusChange handler:
    // when settingsLoading.loading() is true, incoming statusChange events
    // should not overwrite optimistically-set settings fields.
    createRoot((dispose) => {
      const settingsLoading = createLoadingSignal()

      // Simulate: user changes permission mode → optimistic update + start()
      settingsLoading.start()
      expect(settingsLoading.loading()).toBe(true)

      // Simulate: statusChange arrives from agent restart (within debounce).
      // The guard checks loading() — it should be true, so settings are skipped.
      const pendingSettings = settingsLoading.loading()
      expect(pendingSettings).toBe(true)

      // Simulate: the statusChange handler calls stop() (as the old code did).
      // Even so, loading should still be true during the debounce window.
      settingsLoading.stop()
      expect(settingsLoading.loading()).toBe(true)

      // After debounce, loading clears.
      vi.advanceTimersByTime(1000)
      expect(settingsLoading.loading()).toBe(false)
      dispose()
    })
  })
})
