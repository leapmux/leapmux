import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createRenderReadinessScope, createRowReadinessRegistry } from './renderReadiness'

describe('createRenderReadinessScope', () => {
  it('is ready with no tracked body', () => {
    createRoot((dispose) => {
      expect(createRenderReadinessScope().pending()).toBe(false)
      dispose()
    })
  })

  it('is pending while any tracked body is pending', () => {
    createRoot((dispose) => {
      const scope = createRenderReadinessScope()
      const [first, setFirst] = createSignal(true)
      const [second, setSecond] = createSignal(false)
      scope.track(first)
      scope.track(second)

      expect(scope.pending()).toBe(true)
      setSecond(true)
      setFirst(false)
      expect(scope.pending()).toBe(true)
      setSecond(false)
      expect(scope.pending()).toBe(false)
      dispose()
    })
  })

  it('drops a body when its owner is disposed, even while it is still pending', () => {
    createRoot((dispose) => {
      const scope = createRenderReadinessScope()
      const disposeBody = createRoot((disposeBodyRoot) => {
        scope.track(() => true)
        return disposeBodyRoot
      })
      expect(scope.pending()).toBe(true)

      disposeBody()
      expect(scope.pending()).toBe(false)
      dispose()
    })
  })
})

describe('createRowReadinessRegistry', () => {
  it('is not ready for a row that has not mounted yet', () => {
    createRoot((dispose) => {
      expect(createRowReadinessRegistry().isReady('m1')).toBe(false)
      dispose()
    })
  })

  it('is ready for a mounted row once no body it holds is pending', () => {
    createRoot((dispose) => {
      const registry = createRowReadinessRegistry()
      const [pending, setPending] = createSignal(true)
      createRoot(() => {
        registry.trackRow('m1').track(pending)
      })
      createRoot(() => {
        registry.trackRow('m2')
      })

      expect(registry.isReady('m1')).toBe(false)
      expect(registry.isReady('m2')).toBe(true)
      setPending(false)
      expect(registry.isReady('m1')).toBe(true)
      dispose()
    })
  })

  it('is not ready again after the row unmounts', () => {
    createRoot((dispose) => {
      const registry = createRowReadinessRegistry()
      const unmount = createRoot((disposeRow) => {
        registry.trackRow('m1')
        return disposeRow
      })
      expect(registry.isReady('m1')).toBe(true)

      unmount()
      expect(registry.isReady('m1')).toBe(false)
      dispose()
    })
  })

  it('keeps a remounted pending row not ready when the old copy cleans up after the new one mounts', () => {
    // A row can remount under a new element before the old element's cleanup runs.
    createRoot((dispose) => {
      const registry = createRowReadinessRegistry()
      const unmountOld = createRoot((disposeRow) => {
        registry.trackRow('m1')
        return disposeRow
      })
      createRoot(() => {
        registry.trackRow('m1').track(() => true)
      })

      unmountOld()
      expect(registry.isReady('m1')).toBe(false)
      dispose()
    })
  })

  it('is not ready while any one of two mounted copies holds a pending body', () => {
    createRoot((dispose) => {
      const registry = createRowReadinessRegistry()
      createRoot(() => {
        registry.trackRow('m1')
      })
      const [pending, setPending] = createSignal(true)
      createRoot(() => {
        registry.trackRow('m1').track(pending)
      })

      expect(registry.isReady('m1')).toBe(false)
      setPending(false)
      expect(registry.isReady('m1')).toBe(true)
      dispose()
    })
  })

  it('updates in the same pass as the body report, before any effect phase', () => {
    createRoot((dispose) => {
      const registry = createRowReadinessRegistry()
      const [pending, setPending] = createSignal(false)
      createRoot(() => {
        registry.trackRow('m1').track(pending)
      })
      setPending(true)
      // Read synchronously after the write: a computed already propagated it.
      expect(registry.isReady('m1')).toBe(false)
      dispose()
    })
  })
})
