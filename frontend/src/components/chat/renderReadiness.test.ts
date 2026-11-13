import { createComputed, createRoot, createSignal, untrack } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { createRenderReadinessScope, createRowReadinessRegistry, useRenderReadiness, useReplacingReadiness } from './renderReadiness'

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

  it('is measurable with no tracked body, and neither pending nor replacing', () => {
    createRoot((dispose) => {
      const scope = createRenderReadinessScope()
      expect(scope.measurable()).toBe(true)
      expect(scope.replacing()).toBe(false)
      dispose()
    })
  })

  it('keeps a copy that replaces its content out of pending, and not measurable until it shows the new content', () => {
    createRoot((dispose) => {
      const scope = createRenderReadinessScope()
      const [replacing, setReplacing] = createSignal(true)
      scope.trackReplacing(replacing)

      expect(scope.pending()).toBe(false)
      expect(scope.replacing()).toBe(true)
      expect(scope.measurable()).toBe(false)
      setReplacing(false)
      expect(scope.replacing()).toBe(false)
      expect(scope.measurable()).toBe(true)
      dispose()
    })
  })

  it('is not measurable while any tracked body is pending', () => {
    createRoot((dispose) => {
      const scope = createRenderReadinessScope()
      const [pending, setPending] = createSignal(true)
      scope.track(pending)

      expect(scope.measurable()).toBe(false)
      expect(scope.replacing()).toBe(false)
      setPending(false)
      expect(scope.measurable()).toBe(true)
      dispose()
    })
  })

  it('runs an onMeasurable callback each time the copy turns measurable after it was pending', () => {
    const [pending, setPending] = createSignal(true)
    const callback = vi.fn()
    // The root runs its effects once its body returns, as a mounted row does.
    const dispose = createRoot((disposeRoot) => {
      const scope = createRenderReadinessScope()
      scope.track(pending)
      scope.onMeasurable(callback)
      return disposeRoot
    })

    expect(callback).not.toHaveBeenCalled()
    setPending(false)
    expect(callback).toHaveBeenCalledOnce()
    setPending(true)
    expect(callback).toHaveBeenCalledOnce()
    setPending(false)
    expect(callback).toHaveBeenCalledTimes(2)
    dispose()
  })

  it('runs an onMeasurable callback when the copy shows the content that replaced the old, and not when the copy stays pending', () => {
    const [pending, setPending] = createSignal(false)
    const [replacing, setReplacing] = createSignal(true)
    const callback = vi.fn()
    const dispose = createRoot((disposeRoot) => {
      const scope = createRenderReadinessScope()
      scope.track(pending)
      scope.trackReplacing(replacing)
      scope.onMeasurable(callback)
      return disposeRoot
    })

    setPending(true)
    setReplacing(false)
    expect(callback).not.toHaveBeenCalled()
    setPending(false)
    expect(callback).toHaveBeenCalledOnce()
    setReplacing(true)
    setReplacing(false)
    expect(callback).toHaveBeenCalledTimes(2)
    dispose()
  })

  it('does not run an onMeasurable callback for a copy that is measurable from the start', () => {
    const callback = vi.fn()
    createRoot((dispose) => {
      createRenderReadinessScope().onMeasurable(callback)
      dispose()
    })

    expect(callback).not.toHaveBeenCalled()
  })

  it('stops running an onMeasurable callback once its owner is disposed', () => {
    const [pending, setPending] = createSignal(true)
    const scope = createRoot(() => {
      const created = createRenderReadinessScope()
      created.track(pending)
      return created
    })
    const callback = vi.fn()
    createRoot((dispose) => {
      scope.onMeasurable(callback)
      dispose()
    })

    setPending(false)
    expect(callback).not.toHaveBeenCalled()
  })

  it('drops a replacing report when its owner is disposed, even while it still replaces', () => {
    createRoot((dispose) => {
      const scope = createRenderReadinessScope()
      const disposeBody = createRoot((disposeBodyRoot) => {
        scope.trackReplacing(() => true)
        return disposeBodyRoot
      })
      expect(scope.measurable()).toBe(false)

      disposeBody()
      expect(scope.replacing()).toBe(false)
      expect(scope.measurable()).toBe(true)
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

describe('useRenderReadiness', () => {
  it('reports the body to the scope of its context, and draws no skeleton of its own', () => {
    createRoot((dispose) => {
      const scope = createRenderReadinessScope()
      const [pending, setPending] = createSignal(true)

      expect(useRenderReadiness(() => ({ readiness: scope }), pending)).toBe(false)
      expect(scope.pending()).toBe(true)
      setPending(false)
      expect(scope.pending()).toBe(false)
      dispose()
    })
  })

  it('asks a body outside a chat row to draw its own skeleton', () => {
    createRoot((dispose) => {
      expect(useRenderReadiness(() => undefined, () => true)).toBe(true)
      expect(useRenderReadiness(() => ({}), () => true)).toBe(true)
      dispose()
    })
  })

  it('reads the context once, so a later context change does not report the body twice', () => {
    createRoot((dispose) => {
      const first = createRenderReadinessScope()
      const second = createRenderReadinessScope()
      const [context, setContext] = createSignal({ readiness: first })
      useRenderReadiness(context, () => true)
      setContext({ readiness: second })

      expect(first.pending()).toBe(true)
      expect(second.pending()).toBe(false)
      dispose()
    })
  })

  it('ends the report when the owner of the body is disposed', () => {
    const scope = createRoot(() => createRenderReadinessScope())
    createRoot((dispose) => {
      useRenderReadiness(() => ({ readiness: scope }), () => true)
      expect(scope.pending()).toBe(true)
      dispose()
    })

    expect(scope.pending()).toBe(false)
  })
})

describe('useReplacingReadiness', () => {
  it('reports the body to the scope of its context while it replaces its content', () => {
    createRoot((dispose) => {
      const scope = createRenderReadinessScope()
      const [replacing, setReplacing] = createSignal(true)

      useReplacingReadiness(() => ({ readiness: scope }), replacing)

      expect(scope.replacing()).toBe(true)
      expect(scope.pending()).toBe(false)
      setReplacing(false)
      expect(scope.replacing()).toBe(false)
      expect(scope.measurable()).toBe(true)
      dispose()
    })
  })

  it('reports nothing outside a readiness scope, where no reader waits', () => {
    createRoot((dispose) => {
      expect(() => {
        useReplacingReadiness(() => undefined, () => true)
        useReplacingReadiness(() => ({}), () => true)
      }).not.toThrow()
      dispose()
    })
  })

  it('reads the context once, so a later context change does not report the body twice', () => {
    createRoot((dispose) => {
      const first = createRenderReadinessScope()
      const second = createRenderReadinessScope()
      const [context, setContext] = createSignal({ readiness: first })
      useReplacingReadiness(context, () => true)
      setContext({ readiness: second })

      expect(first.replacing()).toBe(true)
      expect(second.replacing()).toBe(false)
      dispose()
    })
  })

  it('ends the report when the owner of the body is disposed', () => {
    const scope = createRoot(() => createRenderReadinessScope())
    createRoot((dispose) => {
      useReplacingReadiness(() => ({ readiness: scope }), () => true)
      expect(scope.replacing()).toBe(true)
      dispose()
    })

    expect(scope.replacing()).toBe(false)
  })
})

describe('createRowReadinessRegistry', () => {
  // A copy that replaces its content keeps showing complete content, so the row
  // stays visible. Only the measurer waits for it (see RowContentReadiness).
  it('keeps a mounted row ready while a body replaces its content', () => {
    createRoot((dispose) => {
      const registry = createRowReadinessRegistry()
      createRoot(() => {
        registry.trackRow('m1').trackReplacing(() => true)
      })

      expect(registry.isReady('m1')).toBe(true)
      dispose()
    })
  })

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

  it('re-runs only the reader of the row whose readiness changes', () => {
    // Each visible row's style effect reads its own row's readiness. A mount, an
    // unmount or a body report of one row must not re-run the other rows' effects.
    createRoot((dispose) => {
      const registry = createRowReadinessRegistry()
      const ids = Array.from({ length: 41 }, (_, i) => `m${i}`)
      const [pending, setPending] = createSignal(false)
      const unmountRow = new Map<string, () => void>()
      for (const id of ids.slice(0, 40)) {
        unmountRow.set(id, createRoot((disposeRow) => {
          const scope = registry.trackRow(id)
          if (id === 'm5')
            scope.track(pending)
          return disposeRow
        }))
      }
      const runs = new Map(ids.map(id => [id, 0]))
      const ready = new Map<string, boolean>()
      for (const id of ids) {
        createComputed(() => {
          ready.set(id, registry.isReady(id))
          runs.set(id, (runs.get(id) ?? 0) + 1)
        })
      }
      const rerunIds = (): string[] => {
        const rerun = [...runs].filter(([, count]) => count > 0).map(([id]) => id)
        runs.forEach((_, id) => runs.set(id, 0))
        return rerun
      }
      rerunIds()

      createRoot(() => {
        registry.trackRow('m40')
      })
      expect(rerunIds()).toEqual(['m40'])
      expect(ready.get('m40')).toBe(true)

      unmountRow.get('m0')?.()
      expect(rerunIds()).toEqual(['m0'])
      expect(ready.get('m0')).toBe(false)

      setPending(true)
      expect(rerunIds()).toEqual(['m5'])
      expect(ready.get('m5')).toBe(false)
      setPending(false)
      expect(rerunIds()).toEqual(['m5'])
      expect(ready.get('m5')).toBe(true)
      dispose()
    })
  })

  it('keeps notifying a reader of a row after another reader of that row ends', () => {
    createRoot((dispose) => {
      const registry = createRowReadinessRegistry()
      const ready: boolean[] = []
      const disposeFirstReader = createRoot((disposeReader) => {
        createComputed(() => registry.isReady('m1'))
        return disposeReader
      })
      createComputed(() => ready.push(registry.isReady('m1')))
      disposeFirstReader()

      createRoot(() => {
        registry.trackRow('m1')
      })
      expect(ready).toEqual([false, true])
      dispose()
    })
  })

  it('notifies a reader through an unmount and a later mount of its row', () => {
    createRoot((dispose) => {
      const registry = createRowReadinessRegistry()
      const ready: boolean[] = []
      createComputed(() => ready.push(registry.isReady('m1')))
      const mount = () => createRoot((disposeRow) => {
        registry.trackRow('m1')
        return disposeRow
      })

      const unmountFirst = mount()
      unmountFirst()
      const unmountSecond = mount()
      expect(ready).toEqual([false, true, false, true])
      unmountSecond()
      expect(ready).toEqual([false, true, false, true, false])
      dispose()
    })
  })

  it('reports a row as mounted while any copy of it is mounted, whether or not it is ready', () => {
    createRoot((dispose) => {
      const registry = createRowReadinessRegistry()
      const mounted: boolean[] = []
      createComputed(() => mounted.push(registry.isMounted('m1')))
      const unmountFirst = createRoot((disposeRow) => {
        registry.trackRow('m1').track(() => true)
        return disposeRow
      })
      const unmountSecond = createRoot((disposeRow) => {
        registry.trackRow('m1')
        return disposeRow
      })
      expect(registry.isReady('m1')).toBe(false)

      unmountFirst()
      expect(mounted.at(-1)).toBe(true)
      unmountSecond()
      expect(mounted.at(-1)).toBe(false)
      expect(mounted[0]).toBe(false)
      dispose()
    })
  })

  it('answers an untracked read without a reader', () => {
    createRoot((dispose) => {
      const registry = createRowReadinessRegistry()
      expect(untrack(() => registry.isReady('m1'))).toBe(false)
      const unmount = createRoot((disposeRow) => {
        registry.trackRow('m1')
        return disposeRow
      })
      expect(untrack(() => registry.isReady('m1'))).toBe(true)
      unmount()
      expect(untrack(() => registry.isReady('m1'))).toBe(false)
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
