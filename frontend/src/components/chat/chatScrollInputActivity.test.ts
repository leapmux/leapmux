import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { PRESS_SLOP_PX } from '~/components/common/contextMenuGesture'
import { createScrollInputActivity } from './chatScrollInputActivity'

/** The disposers of the roots of the test, so that a failed test leaves no window listener. */
const disposers: (() => void)[] = []

/** A tracker inside a reactive root, which adds its window listeners at the mount. */
function mountActivity() {
  const onInputEnded = vi.fn()
  let dispose!: () => void
  const activity = createRoot((d) => {
    dispose = d
    return createScrollInputActivity({ onInputEnded })
  })
  disposers.push(dispose)
  return { activity, onInputEnded, dispose }
}

afterEach(() => {
  for (const dispose of disposers.splice(0))
    dispose()
})

/** A `pointerdown` of the list. */
function pointerDown(pointerId: number, init: { isPrimary?: boolean, pointerType?: string } = {}): PointerEvent {
  return new PointerEvent('pointerdown', {
    pointerId,
    isPrimary: init.isPrimary ?? true,
    pointerType: init.pointerType ?? 'mouse',
  })
}

/** Dispatch `event` from the document body, so it reaches the capture listeners of the window. */
function fromBody(event: Event): void {
  document.body.dispatchEvent(event)
}

/** A touch event whose `touches` list holds the fingers at `points`. */
function touchesAt(...points: [x: number, y: number][]): TouchEvent {
  return {
    touches: points.map(([clientX, clientY]) => ({ clientX, clientY })),
  } as unknown as TouchEvent
}

describe('createScrollInputActivity', () => {
  it('starts with no input down', () => {
    const { activity } = mountActivity()
    expect(activity.isActive()).toBe(false)
  })

  describe('pointers', () => {
    it('ends a pointer at its release anywhere in the window', () => {
      const { activity, onInputEnded } = mountActivity()
      activity.onPointerDown(pointerDown(1))
      expect(activity.isActive()).toBe(true)

      fromBody(new PointerEvent('pointerup', { pointerId: 1, bubbles: true }))

      expect(activity.isActive()).toBe(false)
      expect(onInputEnded).toHaveBeenCalledOnce()
    })

    it('ends a pointer at pointercancel', () => {
      const { activity, onInputEnded } = mountActivity()
      activity.onPointerDown(pointerDown(1))

      fromBody(new PointerEvent('pointercancel', { pointerId: 1, bubbles: true }))

      expect(activity.isActive()).toBe(false)
      expect(onInputEnded).toHaveBeenCalledOnce()
    })

    it('ends the pointer in the capture phase, before a listener of the target', () => {
      const { activity } = mountActivity()
      activity.onPointerDown(pointerDown(1))
      const target = document.createElement('div')
      document.body.append(target)
      let activeAtTarget: boolean | undefined
      target.addEventListener('pointerup', () => {
        activeAtTarget = activity.isActive()
      })

      target.dispatchEvent(new PointerEvent('pointerup', { pointerId: 1, bubbles: true }))

      expect(activeAtTarget).toBe(false)
      target.remove()
    })

    it('reports no end for the release of a pointer that it does not track', () => {
      const { activity, onInputEnded } = mountActivity()
      activity.onPointerDown(pointerDown(1))

      fromBody(new PointerEvent('pointerup', { pointerId: 2, bubbles: true }))

      expect(activity.isActive()).toBe(true)
      expect(onInputEnded).not.toHaveBeenCalled()
    })

    it('stays active while a second pointer stays down', () => {
      const { activity, onInputEnded } = mountActivity()
      activity.onPointerDown(pointerDown(1))
      activity.onPointerDown(pointerDown(2, { isPrimary: false, pointerType: 'touch' }))

      fromBody(new PointerEvent('pointerup', { pointerId: 1, bubbles: true }))
      expect(activity.isActive()).toBe(true)
      expect(onInputEnded).not.toHaveBeenCalled()

      fromBody(new PointerEvent('pointerup', { pointerId: 2, bubbles: true }))
      expect(activity.isActive()).toBe(false)
      expect(onInputEnded).toHaveBeenCalledOnce()
    })

    it('drops a pointer whose release did not arrive when a primary pointer goes down', () => {
      const { activity } = mountActivity()
      activity.onPointerDown(pointerDown(1))
      activity.onPointerDown(pointerDown(3))

      fromBody(new PointerEvent('pointerup', { pointerId: 3, bubbles: true }))

      expect(activity.isActive()).toBe(false)
    })

    it('ends a mouse at contextmenu', () => {
      const { activity, onInputEnded } = mountActivity()
      activity.onPointerDown(pointerDown(1, { pointerType: 'mouse' }))

      fromBody(new MouseEvent('contextmenu', { bubbles: true }))

      expect(activity.isActive()).toBe(false)
      expect(onInputEnded).toHaveBeenCalledOnce()
    })

    it('keeps a touch or a pen down at contextmenu, as a long press opens the menu', () => {
      for (const pointerType of ['touch', 'pen']) {
        const { activity, onInputEnded } = mountActivity()
        activity.onPointerDown(pointerDown(1, { pointerType }))

        fromBody(new MouseEvent('contextmenu', { bubbles: true }))

        expect(activity.isActive()).toBe(true)
        expect(onInputEnded).not.toHaveBeenCalled()
      }
    })

    it('ends a pointer whose capture ends with no button down', () => {
      const { activity, onInputEnded } = mountActivity()
      activity.onPointerDown(pointerDown(1, { pointerType: 'pen' }))

      fromBody(new PointerEvent('lostpointercapture', { pointerId: 1, buttons: 0, bubbles: true }))

      expect(activity.isActive()).toBe(false)
      expect(onInputEnded).toHaveBeenCalledOnce()
    })

    it('keeps a pointer whose capture ends while a button stays down', () => {
      const { activity, onInputEnded } = mountActivity()
      activity.onPointerDown(pointerDown(1, { pointerType: 'pen' }))

      fromBody(new PointerEvent('lostpointercapture', { pointerId: 1, buttons: 1, bubbles: true }))

      expect(activity.isActive()).toBe(true)
      expect(onInputEnded).not.toHaveBeenCalled()
    })

    it('ends every pointer at a blur of the window', () => {
      const { activity, onInputEnded } = mountActivity()
      activity.onPointerDown(pointerDown(1))
      activity.onPointerDown(pointerDown(2, { isPrimary: false, pointerType: 'touch' }))

      window.dispatchEvent(new FocusEvent('blur'))

      expect(activity.isActive()).toBe(false)
      expect(onInputEnded).toHaveBeenCalledOnce()
    })

    it('reports no end at a blur of the window with no input down', () => {
      const { activity, onInputEnded } = mountActivity()

      window.dispatchEvent(new FocusEvent('blur'))

      expect(activity.isActive()).toBe(false)
      expect(onInputEnded).not.toHaveBeenCalled()
    })

    it('reports no end at a blur of the window while a finger stays down', () => {
      const { activity, onInputEnded } = mountActivity()
      activity.onPointerDown(pointerDown(1, { pointerType: 'touch' }))
      activity.onTouchStart(touchesAt([10, 10]))

      window.dispatchEvent(new FocusEvent('blur'))

      // The blur ends the pointer, and the touch list still holds the finger.
      expect(activity.isActive()).toBe(true)
      expect(onInputEnded).not.toHaveBeenCalled()
    })

    it('ends a pointer that went down twice at its one release', () => {
      const { activity, onInputEnded } = mountActivity()
      activity.onPointerDown(pointerDown(2, { isPrimary: false, pointerType: 'touch' }))
      activity.onPointerDown(pointerDown(2, { isPrimary: false, pointerType: 'touch' }))

      fromBody(new PointerEvent('pointerup', { pointerId: 2, bubbles: true }))

      expect(activity.isActive()).toBe(false)
      expect(onInputEnded).toHaveBeenCalledOnce()
    })

    it('removes its window listeners at the cleanup of its owner', () => {
      const { activity, onInputEnded, dispose } = mountActivity()
      activity.onPointerDown(pointerDown(1))

      dispose()
      fromBody(new PointerEvent('pointerup', { pointerId: 1, bubbles: true }))
      window.dispatchEvent(new FocusEvent('blur'))

      expect(onInputEnded).not.toHaveBeenCalled()
    })
  })

  describe('touches', () => {
    it('stays active until the last finger lifts', () => {
      const { activity, onInputEnded } = mountActivity()
      activity.onTouchStart(touchesAt([10, 10]))
      activity.onTouchStart(touchesAt([10, 10], [50, 50]))
      expect(activity.isActive()).toBe(true)

      activity.onTouchEnd(touchesAt([50, 50]))
      expect(activity.isActive()).toBe(true)
      expect(onInputEnded).not.toHaveBeenCalled()

      activity.onTouchEnd(touchesAt())
      expect(activity.isActive()).toBe(false)
      expect(onInputEnded).toHaveBeenCalledOnce()
    })

    it('reports no end while a pointer stays down', () => {
      const { activity, onInputEnded } = mountActivity()
      activity.onPointerDown(pointerDown(1, { pointerType: 'mouse' }))
      activity.onTouchStart(touchesAt([10, 10]))

      activity.onTouchEnd(touchesAt())

      expect(activity.isActive()).toBe(true)
      expect(onInputEnded).not.toHaveBeenCalled()
    })

    it('corrects a missed touch end at the next touch event', () => {
      const { activity } = mountActivity()
      activity.onTouchStart(touchesAt([10, 10]))
      // The end of that touch did not arrive. The next gesture lists one finger again, so
      // its end leaves no input down, where a counter would stay at one.
      activity.onTouchStart(touchesAt([20, 20]))
      activity.onTouchEnd(touchesAt())
      expect(activity.isActive()).toBe(false)
    })
  })

  describe('isVerticalDrag', () => {
    it('finds a drag of one finger past the slop', () => {
      const { activity } = mountActivity()
      activity.onTouchStart(touchesAt([10, 10]))
      expect(activity.isVerticalDrag(touchesAt([10, 10 + PRESS_SLOP_PX]))).toBe(false)
      expect(activity.isVerticalDrag(touchesAt([10, 11 + PRESS_SLOP_PX]))).toBe(true)
      expect(activity.isVerticalDrag(touchesAt([10, 9 - PRESS_SLOP_PX]))).toBe(true)
    })

    it('finds no drag for a swipe that travels farther sideways', () => {
      const { activity } = mountActivity()
      activity.onTouchStart(touchesAt([10, 10]))
      expect(activity.isVerticalDrag(touchesAt([90, 30]))).toBe(false)
    })

    it('finds no drag for a diagonal travel of equal distances', () => {
      const { activity } = mountActivity()
      const travel = PRESS_SLOP_PX + 20
      activity.onTouchStart(touchesAt([10, 10]))
      expect(activity.isVerticalDrag(touchesAt([10 + travel, 10 + travel]))).toBe(false)
      expect(activity.isVerticalDrag(touchesAt([10 + travel, 11 + travel]))).toBe(true)
    })

    it('finds no drag for two fingers', () => {
      const { activity } = mountActivity()
      activity.onTouchStart(touchesAt([10, 10], [10, 200]))
      expect(activity.isVerticalDrag(touchesAt([10, 60], [10, 150]))).toBe(false)
    })

    it('finds no drag with no touch', () => {
      const { activity } = mountActivity()
      expect(activity.isVerticalDrag(touchesAt([10, 60]))).toBe(false)
    })

    it('measures from where the count of fingers last changed', () => {
      const { activity } = mountActivity()
      activity.onTouchStart(touchesAt([10, 10], [10, 200]))
      // One finger lifts at its place: the travel counts from the remaining finger.
      activity.onTouchEnd(touchesAt([10, 200]))
      expect(activity.isVerticalDrag(touchesAt([10, 200 + PRESS_SLOP_PX]))).toBe(false)
      expect(activity.isVerticalDrag(touchesAt([10, 201 + PRESS_SLOP_PX]))).toBe(true)
    })
  })
})
