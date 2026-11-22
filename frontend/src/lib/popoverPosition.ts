import { clamp } from '~/lib/clamp'
import { readViewportRect } from '~/lib/visualViewport'

export { readViewportRect, type ViewportRect } from '~/lib/visualViewport'

export interface PopoverPositionOptions {
  /** 'auto' = flip upward if clipped; 'above' = always position above trigger */
  placement?: 'auto' | 'above'
  /** Pixel gap between trigger and popover (default: 0) */
  offset?: number
  /** Additional horizontal shift in pixels after base positioning. */
  xOffset?: number
  /** Additional vertical shift in pixels after base positioning. */
  yOffset?: number
}

/**
 * A viewport rect that a popover points at, for an anchor that is not an
 * element -- a right-click or a touch long press, which anchors to the pressed
 * row's vertical band at the pointer's x.
 *
 * `DOMRect` satisfies this shape structurally, so an element anchor and a
 * pointer anchor run the same flip and clamp arithmetic below. Only these three
 * edges are read: the popover goes below `bottom` (flipping above `top` when
 * clipped), left-aligned to `left`.
 */
export interface PopoverAnchorRect {
  top: number
  bottom: number
  left: number
}

export type PopoverAnchor = Element | PopoverAnchorRect

/**
 * Clamp a popover's start edge within `[start, end]` so a popover of `size`
 * stays inside the span, pinning to `start` when `size > end - start`.
 */
function clampToSpan(position: number, size: number, start: number, end: number): number {
  return clamp(position, start, Math.max(start, end - size))
}

/**
 * Calculate the top/left for a fixed-position popover so it stays inside the
 * visible viewport.
 */
export function calcPopoverPosition(
  anchor: PopoverAnchor,
  popover: HTMLElement,
  options: PopoverPositionOptions = {},
): { top: number, left: number, flipped: boolean } {
  const { placement = 'auto', offset = 0, xOffset = 0, yOffset = 0 } = options
  const triggerRect = 'getBoundingClientRect' in anchor
    ? anchor.getBoundingClientRect()
    : anchor
  const popoverRect = popover.getBoundingClientRect()
  const viewport = readViewportRect()

  let top: number
  let flipped = false

  if (placement === 'above') {
    top = triggerRect.top - popoverRect.height - offset
    flipped = true
  }
  else {
    const belowTop = triggerRect.bottom + offset
    const belowBottom = belowTop + popoverRect.height

    if (belowBottom > viewport.bottom) {
      const aboveTop = triggerRect.top - popoverRect.height - offset
      if (aboveTop >= viewport.top) {
        top = aboveTop
        flipped = true
      }
      else {
        // Not enough space either way -- pick the side with more room
        const spaceBelow = viewport.bottom - triggerRect.bottom
        const spaceAbove = triggerRect.top - viewport.top
        if (spaceAbove > spaceBelow) {
          top = triggerRect.top - popoverRect.height - offset
          flipped = true
        }
        else {
          top = belowTop
        }
      }
    }
    else {
      top = belowTop
    }
  }

  // --- Horizontal positioning ---
  // Start aligned with trigger's left edge, then clamp to the visible viewport.
  let left = clampToSpan(triggerRect.left, popoverRect.width, viewport.left, viewport.right)

  top = clampToSpan(top + yOffset, popoverRect.height, viewport.top, viewport.bottom)
  left = clampToSpan(left + xOffset, popoverRect.width, viewport.left, viewport.right)

  return { top, left, flipped }
}
