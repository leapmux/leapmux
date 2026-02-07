import { describe, expect, it, vi } from 'vitest'
import { calcPopoverPosition } from './popoverPosition'

/** Helper to create a mock trigger element with a given bounding rect. */
function mockTrigger(rect: Partial<DOMRect>): Element {
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
  } as Element
}

/** Helper to create a mock popover element with a given bounding rect. */
function mockPopover(rect: Partial<DOMRect>): HTMLElement {
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

    const trigger = mockTrigger({ top: 100, bottom: 130, left: 200 })
    const popover = mockPopover({ width: 200, height: 150 })

    const result = calcPopoverPosition(trigger, popover)

    expect(result.top).toBe(130) // trigger bottom
    expect(result.left).toBe(200) // trigger left
    expect(result.flipped).toBe(false)
  })

  it('flips above trigger when not enough space below', () => {
    vi.stubGlobal('innerHeight', 400)
    vi.stubGlobal('innerWidth', 1200)

    // Trigger near the bottom of viewport
    const trigger = mockTrigger({ top: 300, bottom: 330, left: 50 })
    const popover = mockPopover({ width: 200, height: 150 })

    const result = calcPopoverPosition(trigger, popover)

    // Should flip above: triggerTop - popoverHeight = 300 - 150 = 150
    expect(result.top).toBe(150)
    expect(result.flipped).toBe(true)
  })

  it('picks the side with more room when neither fits', () => {
    vi.stubGlobal('innerHeight', 200)
    vi.stubGlobal('innerWidth', 1200)

    // Trigger in the middle of a very small viewport, popover too tall for either side
    const trigger = mockTrigger({ top: 80, bottom: 110, left: 50 })
    const popover = mockPopover({ width: 200, height: 150 })

    const result = calcPopoverPosition(trigger, popover)

    // Space above = 80, space below = 200 - 110 = 90
    // Below has more room, so place below
    expect(result.top).toBe(110)
    expect(result.flipped).toBe(false)
  })

  it('picks above when above has more room and neither fits', () => {
    vi.stubGlobal('innerHeight', 200)
    vi.stubGlobal('innerWidth', 1200)

    // Trigger near bottom, more space above
    const trigger = mockTrigger({ top: 150, bottom: 180, left: 50 })
    const popover = mockPopover({ width: 200, height: 200 })

    const result = calcPopoverPosition(trigger, popover)

    // Space above = 150, space below = 200 - 180 = 20
    // Above has more room
    expect(result.top).toBe(150 - 200) // -50
    expect(result.flipped).toBe(true)
  })

  it('clamps left when menu overflows right viewport edge', () => {
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('innerWidth', 500)

    // Trigger near right edge
    const trigger = mockTrigger({ top: 100, bottom: 130, left: 400 })
    const popover = mockPopover({ width: 200, height: 150 })

    const result = calcPopoverPosition(trigger, popover)

    // 400 + 200 = 600 > 500, overflow = 100
    // left = max(0, 400 - 100) = 300
    expect(result.left).toBe(300)
  })

  it('clamps left to 0 when viewport is very narrow', () => {
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('innerWidth', 100)

    const trigger = mockTrigger({ top: 100, bottom: 130, left: 50 })
    const popover = mockPopover({ width: 200, height: 150 })

    const result = calcPopoverPosition(trigger, popover)

    // 50 + 200 = 250 > 100, overflow = 150
    // left = max(0, 50 - 150) = max(0, -100) = 0
    expect(result.left).toBe(0)
  })

  it('placement "above" always positions above trigger', () => {
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('innerWidth', 1200)

    const trigger = mockTrigger({ top: 300, bottom: 330, left: 100 })
    const popover = mockPopover({ width: 200, height: 100 })

    const result = calcPopoverPosition(trigger, popover, { placement: 'above' })

    expect(result.top).toBe(200) // 300 - 100
    expect(result.flipped).toBe(true)
  })

  it('respects offset option', () => {
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('innerWidth', 1200)

    const trigger = mockTrigger({ top: 100, bottom: 130, left: 200 })
    const popover = mockPopover({ width: 200, height: 150 })

    const result = calcPopoverPosition(trigger, popover, { offset: 8 })

    expect(result.top).toBe(138) // trigger bottom + offset
  })

  it('respects offset option with placement above', () => {
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('innerWidth', 1200)

    const trigger = mockTrigger({ top: 300, bottom: 330, left: 100 })
    const popover = mockPopover({ width: 200, height: 100 })

    const result = calcPopoverPosition(trigger, popover, { placement: 'above', offset: 8 })

    expect(result.top).toBe(192) // 300 - 100 - 8
  })

  it('does not shift horizontally when menu fits', () => {
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('innerWidth', 1200)

    const trigger = mockTrigger({ top: 100, bottom: 130, left: 200 })
    const popover = mockPopover({ width: 200, height: 150 })

    const result = calcPopoverPosition(trigger, popover)

    expect(result.left).toBe(200) // unchanged
  })
})
