import { describe, expect, it, onTestFinished, vi } from 'vitest'
import { calcPopoverPosition } from './popoverPosition'

/** Create a mock DOM element with a given bounding rect (satisfies both Element and HTMLElement). */
function mockElement(rect: Partial<DOMRect>): HTMLElement {
  return {
    getBoundingClientRect: () => ({
      top: 0,
      left: 0,
      bottom: 0,
      right: 0,
      width: 0,
      height: 0,
      x: 0,
      y: 0,
      toJSON: () => {},
      ...rect,
    }),
  } as HTMLElement
}

describe('calcPopoverPosition', () => {
  it('positions below trigger when enough space', () => {
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('innerWidth', 1200)

    const trigger = mockElement({ top: 100, bottom: 130, left: 200 })
    const popover = mockElement({ width: 200, height: 150 })

    const result = calcPopoverPosition(trigger, popover)

    expect(result.top).toBe(130) // trigger bottom
    expect(result.left).toBe(200) // trigger left
    expect(result.flipped).toBe(false)
  })

  it('flips above trigger when not enough space below', () => {
    vi.stubGlobal('innerHeight', 400)
    vi.stubGlobal('innerWidth', 1200)

    // Trigger near the bottom of viewport
    const trigger = mockElement({ top: 300, bottom: 330, left: 50 })
    const popover = mockElement({ width: 200, height: 150 })

    const result = calcPopoverPosition(trigger, popover)

    // Should flip above: triggerTop - popoverHeight = 300 - 150 = 150
    expect(result.top).toBe(150)
    expect(result.flipped).toBe(true)
  })

  it('picks the side with more room when neither fits', () => {
    vi.stubGlobal('innerHeight', 200)
    vi.stubGlobal('innerWidth', 1200)

    // Trigger in the middle of a very small viewport, popover too tall for either side
    const trigger = mockElement({ top: 80, bottom: 110, left: 50 })
    const popover = mockElement({ width: 200, height: 150 })

    const result = calcPopoverPosition(trigger, popover)

    // Space above = 80, space below = 200 - 110 = 90; below wins.
    // Base top would be 110, but the popover (150) overflows the 200 viewport,
    // so it clamps to viewportHeight - popoverHeight = 50.
    expect(result.top).toBe(50)
    expect(result.flipped).toBe(false)
  })

  it('picks above when above has more room and neither fits', () => {
    vi.stubGlobal('innerHeight', 200)
    vi.stubGlobal('innerWidth', 1200)

    // Trigger near bottom, more space above
    const trigger = mockElement({ top: 150, bottom: 180, left: 50 })
    const popover = mockElement({ width: 200, height: 200 })

    const result = calcPopoverPosition(trigger, popover)

    // Space above = 150, space below = 200 - 180 = 20; above wins.
    // Base top would be -50 (150 - 200), but that's above the viewport, so it
    // clamps to 0.
    expect(result.top).toBe(0)
    expect(result.flipped).toBe(true)
  })

  it('clamps left when menu overflows right viewport edge', () => {
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('innerWidth', 500)

    // Trigger near right edge
    const trigger = mockElement({ top: 100, bottom: 130, left: 400 })
    const popover = mockElement({ width: 200, height: 150 })

    const result = calcPopoverPosition(trigger, popover)

    // 400 + 200 = 600 > 500, overflow = 100
    // left = max(0, 400 - 100) = 300
    expect(result.left).toBe(300)
  })

  it('clamps left to 0 when viewport is very narrow', () => {
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('innerWidth', 100)

    const trigger = mockElement({ top: 100, bottom: 130, left: 50 })
    const popover = mockElement({ width: 200, height: 150 })

    const result = calcPopoverPosition(trigger, popover)

    // 50 + 200 = 250 > 100, overflow = 150
    // left = max(0, 50 - 150) = max(0, -100) = 0
    expect(result.left).toBe(0)
  })

  it('placement "above" always positions above trigger', () => {
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('innerWidth', 1200)

    const trigger = mockElement({ top: 300, bottom: 330, left: 100 })
    const popover = mockElement({ width: 200, height: 100 })

    const result = calcPopoverPosition(trigger, popover, { placement: 'above' })

    expect(result.top).toBe(200) // 300 - 100
    expect(result.flipped).toBe(true)
  })

  it('respects offset option', () => {
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('innerWidth', 1200)

    const trigger = mockElement({ top: 100, bottom: 130, left: 200 })
    const popover = mockElement({ width: 200, height: 150 })

    const result = calcPopoverPosition(trigger, popover, { offset: 8 })

    expect(result.top).toBe(138) // trigger bottom + offset
  })

  it('respects offset option with placement above', () => {
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('innerWidth', 1200)

    const trigger = mockElement({ top: 300, bottom: 330, left: 100 })
    const popover = mockElement({ width: 200, height: 100 })

    const result = calcPopoverPosition(trigger, popover, { placement: 'above', offset: 8 })

    expect(result.top).toBe(192) // 300 - 100 - 8
  })

  it('does not shift horizontally when menu fits', () => {
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('innerWidth', 1200)

    const trigger = mockElement({ top: 100, bottom: 130, left: 200 })
    const popover = mockElement({ width: 200, height: 150 })

    const result = calcPopoverPosition(trigger, popover)

    expect(result.left).toBe(200) // unchanged
  })

  // A rect anchor is what a right-click or a touch long press supplies: the
  // pressed row's vertical band, with the pointer's x as the left edge.
  describe('rect anchor', () => {
    it('positions below the rect, left-aligned to its left edge', () => {
      vi.stubGlobal('innerHeight', 800)
      vi.stubGlobal('innerWidth', 1200)

      const popover = mockElement({ width: 200, height: 150 })

      const result = calcPopoverPosition({ top: 100, bottom: 122, left: 340 }, popover)

      expect(result.top).toBe(122) // the row's bottom, not the pointer's y
      expect(result.left).toBe(340) // the pointer's x
      expect(result.flipped).toBe(false)
    })

    it('flips above the rect when clipped at the bottom', () => {
      vi.stubGlobal('innerHeight', 400)
      vi.stubGlobal('innerWidth', 1200)

      const popover = mockElement({ width: 200, height: 150 })

      const result = calcPopoverPosition({ top: 300, bottom: 322, left: 50 }, popover)

      expect(result.top).toBe(150) // 300 - 150
      expect(result.flipped).toBe(true)
    })

    it('clamps a rect anchor near the right edge', () => {
      vi.stubGlobal('innerHeight', 800)
      vi.stubGlobal('innerWidth', 500)

      const popover = mockElement({ width: 200, height: 150 })

      const result = calcPopoverPosition({ top: 100, bottom: 122, left: 460 }, popover)

      // 460 + 200 = 660 > 500, overflow = 160 -> left = 300
      expect(result.left).toBe(300)
    })

    it('honours offset and placement for a rect anchor', () => {
      vi.stubGlobal('innerHeight', 800)
      vi.stubGlobal('innerWidth', 1200)

      const popover = mockElement({ width: 200, height: 100 })

      const result = calcPopoverPosition(
        { top: 300, bottom: 322, left: 100 },
        popover,
        { placement: 'above', offset: 8 },
      )

      expect(result.top).toBe(192) // 300 - 100 - 8
      expect(result.flipped).toBe(true)
    })
  })

  describe('visualViewport rectangle', () => {
    function stubViewport(opts: {
      innerWidth: number
      innerHeight: number
      visualViewport?: { width: number, height: number, offsetTop: number, offsetLeft: number } | null
      vvShift?: string
    }) {
      vi.stubGlobal('innerWidth', opts.innerWidth)
      vi.stubGlobal('innerHeight', opts.innerHeight)
      if (opts.visualViewport !== undefined)
        vi.stubGlobal('visualViewport', opts.visualViewport)
      if (opts.vvShift !== undefined)
        document.documentElement.style.setProperty('--vv-shift', opts.vvShift)
      onTestFinished(() => {
        vi.unstubAllGlobals()
        document.documentElement.style.removeProperty('--vv-shift')
      })
    }

    it('positions and clamps within visualViewport when iOS shifts offsetTop for the keyboard', () => {
      // Measured iOS Safari keyboard-up geometry: window.innerHeight matches
      // visualViewport.height (262), while the visible region sits at layout
      // y in [452, 714]. Clamping against [0, 262] pushed the popover off the
      // top of the visible screen.
      stubViewport({
        innerWidth: 390,
        innerHeight: 262,
        visualViewport: {
          width: 390,
          height: 262,
          offsetTop: 452,
          offsetLeft: 0,
        },
      })
      const trigger = mockElement({ top: 680, bottom: 704, left: 40 })
      const popover = mockElement({ width: 220, height: 200 })

      const result = calcPopoverPosition(trigger, popover)

      expect(result.top).toBe(480) // 680 - 200, inside [452, 714]
      expect(result.left).toBe(40)
      expect(result.flipped).toBe(true)
    })

    it('clamps to visualViewport.offsetTop when the popover is taller than the visible region', () => {
      stubViewport({
        innerWidth: 390,
        innerHeight: 262,
        visualViewport: {
          width: 390,
          height: 262,
          offsetTop: 452,
          offsetLeft: 0,
        },
      })
      const trigger = mockElement({ top: 680, bottom: 704, left: 40 })
      const popover = mockElement({ width: 220, height: 300 })

      const result = calcPopoverPosition(trigger, popover)

      expect(result.top).toBe(452)
      expect(result.flipped).toBe(true)
    })

    it('clamps horizontally within visualViewport offsetLeft and width', () => {
      stubViewport({
        innerWidth: 800,
        innerHeight: 800,
        visualViewport: {
          width: 300,
          height: 400,
          offsetTop: 0,
          offsetLeft: 50,
        },
      })
      const trigger = mockElement({ top: 100, bottom: 130, left: 260 })
      const popover = mockElement({ width: 200, height: 120 })

      const result = calcPopoverPosition(trigger, popover)

      // Visible horizontal band is [50, 350]; right edge clamps left to 150.
      expect(result.left).toBe(150)
    })

    it('follows negative --vv-shift on iOS 26 keyboard-down residual offsetTop', () => {
      // On iOS 26 after dismissing the keyboard (FB19889436), visualViewport.offsetTop
      // stays positive (120) while useVisualViewportInset translates body by -120px.
      // Triggers sit in [-120, 680], so popovers must clamp and flip against [-120, 680].
      stubViewport({
        innerWidth: 390,
        innerHeight: 800,
        visualViewport: {
          width: 390,
          height: 800,
          offsetTop: 120,
          offsetLeft: 0,
        },
        vvShift: '-120px',
      })

      // Top-bar trigger shifted to y = -110..-86 stays attached rather than clamping to +120.
      const topTrigger = mockElement({ top: -110, bottom: -86, left: 20 })
      const topPopover = mockElement({ width: 200, height: 120 })
      const topResult = calcPopoverPosition(topTrigger, topPopover)
      expect(topResult.top).toBe(-86)
      expect(topResult.flipped).toBe(false)

      // Bottom composer chip shifted to y = 630..654 flips above rather than overflowing below 680.
      const bottomTrigger = mockElement({ top: 630, bottom: 654, left: 20 })
      const bottomPopover = mockElement({ width: 200, height: 200 })
      const bottomResult = calcPopoverPosition(bottomTrigger, bottomPopover)
      expect(bottomResult.top).toBe(430)
      expect(bottomResult.flipped).toBe(true)
    })

    it('falls back atomically to window.innerWidth and innerHeight without stale visualViewport offsets', () => {
      stubViewport({
        innerWidth: 800,
        innerHeight: 500,
        visualViewport: {
          width: 0,
          height: 0,
          offsetTop: 452,
          offsetLeft: 80,
        },
      })
      const trigger = mockElement({ top: 420, bottom: 450, left: 100 })
      const popover = mockElement({ width: 200, height: 120 })

      const result = calcPopoverPosition(trigger, popover)

      expect(result.top).toBe(300)
      expect(result.left).toBe(100)
      expect(result.flipped).toBe(true)
    })

    it('handles an undefined window without throwing ReferenceError', () => {
      vi.stubGlobal('window', undefined)
      onTestFinished(() => {
        vi.unstubAllGlobals()
      })
      const trigger = mockElement({ top: 100, bottom: 130, left: 40 })
      const popover = mockElement({ width: 200, height: 120 })

      const result = calcPopoverPosition(trigger, popover)

      expect(result).toEqual({ top: 0, left: 0, flipped: true })
    })
  })
})
