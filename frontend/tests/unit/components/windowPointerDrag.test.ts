import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useWindowPointerDrag } from '~/components/shell/windowPointerDrag'

function pointerEvent(type: string, init?: PointerEventInit): PointerEvent {
  return new PointerEvent(type, { bubbles: true, ...init })
}

describe('useWindowPointerDrag', () => {
  it('start() registers pointermove + pointerup on document', () => {
    createRoot((dispose) => {
      const drag = useWindowPointerDrag()
      const onMove = vi.fn()
      drag.start({ onMove })
      document.dispatchEvent(pointerEvent('pointermove'))
      expect(onMove).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  it('pointerup after a move invokes onUp once and detaches listeners', () => {
    createRoot((dispose) => {
      const drag = useWindowPointerDrag()
      const onMove = vi.fn()
      const onUp = vi.fn()
      drag.start({ onMove, onUp })
      document.dispatchEvent(pointerEvent('pointermove'))
      document.dispatchEvent(pointerEvent('pointerup'))
      expect(onUp).toHaveBeenCalledTimes(1)
      // Subsequent moves should be ignored — the pointerup teardown
      // unregisters the listener.
      document.dispatchEvent(pointerEvent('pointermove'))
      expect(onMove).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  it('bare pointerup with no preceding pointermove skips onUp', () => {
    // A click on the drag surface (no movement) should not pay the cost
    // of onUp side effects (debounced persist, focus rebroadcast, etc.).
    createRoot((dispose) => {
      const drag = useWindowPointerDrag()
      const onMove = vi.fn()
      const onUp = vi.fn()
      drag.start({ onMove, onUp })
      document.dispatchEvent(pointerEvent('pointerup'))
      expect(onMove).not.toHaveBeenCalled()
      expect(onUp).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('pointercancel after a move is treated as a natural drag end', () => {
    createRoot((dispose) => {
      const drag = useWindowPointerDrag()
      const onMove = vi.fn()
      const onUp = vi.fn()
      drag.start({ onMove, onUp })
      document.dispatchEvent(pointerEvent('pointermove'))
      document.dispatchEvent(pointerEvent('pointercancel'))
      expect(onUp).toHaveBeenCalledTimes(1)
      // Listeners should be gone — neither move nor a follow-up
      // pointerup re-fires onUp.
      document.dispatchEvent(pointerEvent('pointermove'))
      document.dispatchEvent(pointerEvent('pointerup'))
      expect(onMove).toHaveBeenCalledTimes(1)
      expect(onUp).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  it('pointercancel with no preceding move skips onUp', () => {
    createRoot((dispose) => {
      const drag = useWindowPointerDrag()
      const onMove = vi.fn()
      const onUp = vi.fn()
      drag.start({ onMove, onUp })
      document.dispatchEvent(pointerEvent('pointercancel'))
      expect(onMove).not.toHaveBeenCalled()
      expect(onUp).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('a second start() cancels the first drag without firing its onUp', () => {
    createRoot((dispose) => {
      const drag = useWindowPointerDrag()
      const firstOnMove = vi.fn()
      const firstOnUp = vi.fn()
      const secondOnMove = vi.fn()
      drag.start({ onMove: firstOnMove, onUp: firstOnUp })
      drag.start({ onMove: secondOnMove })
      document.dispatchEvent(pointerEvent('pointermove'))
      expect(firstOnMove).not.toHaveBeenCalled()
      expect(firstOnUp).not.toHaveBeenCalled()
      expect(secondOnMove).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  it('cancel() removes listeners without firing onUp', () => {
    createRoot((dispose) => {
      const drag = useWindowPointerDrag()
      const onMove = vi.fn()
      const onUp = vi.fn()
      drag.start({ onMove, onUp })
      drag.cancel()
      expect(onUp).not.toHaveBeenCalled()
      document.dispatchEvent(pointerEvent('pointermove'))
      document.dispatchEvent(pointerEvent('pointerup'))
      expect(onMove).not.toHaveBeenCalled()
      expect(onUp).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('owner disposal tears down the active drag', () => {
    let drag!: ReturnType<typeof useWindowPointerDrag>
    const onMove = vi.fn()
    const onUp = vi.fn()
    const dispose = createRoot((dispose) => {
      drag = useWindowPointerDrag()
      drag.start({ onMove, onUp })
      return dispose
    })
    dispose()
    document.dispatchEvent(pointerEvent('pointermove'))
    document.dispatchEvent(pointerEvent('pointerup'))
    expect(onMove).not.toHaveBeenCalled()
    expect(onUp).not.toHaveBeenCalled()
  })

  describe('onFinish', () => {
    it('fires on bare-click pointerup even when onUp is suppressed', () => {
      // Cursor / dragging-indicator cleanup that callers register in
      // pointerdown must fire on every pointerup, including bare clicks.
      createRoot((dispose) => {
        const drag = useWindowPointerDrag()
        const onUp = vi.fn()
        const onFinish = vi.fn()
        drag.start({ onMove: vi.fn(), onUp, onFinish })
        document.dispatchEvent(pointerEvent('pointerup'))
        expect(onUp).not.toHaveBeenCalled()
        expect(onFinish).toHaveBeenCalledTimes(1)
        dispose()
      })
    })

    it('fires after onUp when a move precedes pointerup', () => {
      createRoot((dispose) => {
        const drag = useWindowPointerDrag()
        const order: string[] = []
        drag.start({
          onMove: vi.fn(),
          onUp: () => order.push('up'),
          onFinish: () => order.push('finish'),
        })
        document.dispatchEvent(pointerEvent('pointermove'))
        document.dispatchEvent(pointerEvent('pointerup'))
        expect(order).toEqual(['up', 'finish'])
        dispose()
      })
    })

    it('fires on pointercancel with no preceding move', () => {
      createRoot((dispose) => {
        const drag = useWindowPointerDrag()
        const onFinish = vi.fn()
        drag.start({ onMove: vi.fn(), onFinish })
        document.dispatchEvent(pointerEvent('pointercancel'))
        expect(onFinish).toHaveBeenCalledTimes(1)
        dispose()
      })
    })

    it('does not fire on cancel() (hard abort)', () => {
      createRoot((dispose) => {
        const drag = useWindowPointerDrag()
        const onFinish = vi.fn()
        drag.start({ onMove: vi.fn(), onFinish })
        drag.cancel()
        expect(onFinish).not.toHaveBeenCalled()
        dispose()
      })
    })

    it('does not fire on owner disposal', () => {
      let drag!: ReturnType<typeof useWindowPointerDrag>
      const onFinish = vi.fn()
      const dispose = createRoot((dispose) => {
        drag = useWindowPointerDrag()
        drag.start({ onMove: vi.fn(), onFinish })
        return dispose
      })
      dispose()
      expect(onFinish).not.toHaveBeenCalled()
    })

    it('does not fire on a superseding start() (the previous drag is hard-cancelled)', () => {
      createRoot((dispose) => {
        const drag = useWindowPointerDrag()
        const firstFinish = vi.fn()
        drag.start({ onMove: vi.fn(), onFinish: firstFinish })
        drag.start({ onMove: vi.fn() })
        expect(firstFinish).not.toHaveBeenCalled()
        dispose()
      })
    })
  })

  describe('coalesce: true', () => {
    let rafCallbacks: FrameRequestCallback[] = []
    let nextRafId = 0
    let originalRaf: typeof globalThis.requestAnimationFrame
    let originalCaf: typeof globalThis.cancelAnimationFrame
    const flushFrame = () => {
      const cbs = rafCallbacks
      rafCallbacks = []
      for (const cb of cbs)
        cb(performance.now())
    }
    beforeEach(() => {
      rafCallbacks = []
      nextRafId = 0
      originalRaf = globalThis.requestAnimationFrame
      originalCaf = globalThis.cancelAnimationFrame
      globalThis.requestAnimationFrame = ((cb: FrameRequestCallback) => {
        const id = ++nextRafId
        rafCallbacks.push(cb)
        return id
      }) as typeof globalThis.requestAnimationFrame
      globalThis.cancelAnimationFrame = (() => {
        // Tests dispatch frames manually; cancellation just flushes the
        // queue without invoking pending callbacks.
        rafCallbacks = []
      }) as typeof globalThis.cancelAnimationFrame
    })
    afterEach(() => {
      globalThis.requestAnimationFrame = originalRaf
      globalThis.cancelAnimationFrame = originalCaf
    })

    it('coalesces multiple pointermoves into one onMove per frame, with the latest event', () => {
      createRoot((dispose) => {
        const drag = useWindowPointerDrag()
        const onMove = vi.fn()
        drag.start({ coalesce: true, onMove })
        document.dispatchEvent(pointerEvent('pointermove', { clientX: 1 }))
        document.dispatchEvent(pointerEvent('pointermove', { clientX: 2 }))
        document.dispatchEvent(pointerEvent('pointermove', { clientX: 3 }))
        expect(onMove).not.toHaveBeenCalled()
        flushFrame()
        expect(onMove).toHaveBeenCalledTimes(1)
        expect(onMove.mock.calls[0][0].clientX).toBe(3)
        dispose()
      })
    })

    it('flushes the pending event synchronously before onUp fires on pointerup', () => {
      createRoot((dispose) => {
        const drag = useWindowPointerDrag()
        const onMove = vi.fn()
        const onUp = vi.fn(() => {
          // onUp must observe the final move already dispatched.
          expect(onMove).toHaveBeenCalledTimes(1)
        })
        drag.start({ coalesce: true, onMove, onUp })
        document.dispatchEvent(pointerEvent('pointermove', { clientX: 5 }))
        document.dispatchEvent(pointerEvent('pointerup'))
        expect(onMove).toHaveBeenCalledTimes(1)
        expect(onMove.mock.calls[0][0].clientX).toBe(5)
        expect(onUp).toHaveBeenCalledTimes(1)
        dispose()
      })
    })

    it('cancel() drops the pending event without invoking onMove', () => {
      createRoot((dispose) => {
        const drag = useWindowPointerDrag()
        const onMove = vi.fn()
        drag.start({ coalesce: true, onMove })
        document.dispatchEvent(pointerEvent('pointermove'))
        drag.cancel()
        flushFrame()
        expect(onMove).not.toHaveBeenCalled()
        dispose()
      })
    })
  })
})
