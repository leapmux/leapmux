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
 * Calculate the top/left for a fixed-position popover so it doesn't
 * overflow the bottom of the viewport.
 */
export function calcPopoverPosition(
  anchor: PopoverAnchor,
  popover: HTMLElement,
  options: PopoverPositionOptions = {},
): { top: number, left: number, flipped: boolean } {
  const { placement = 'auto', offset = 0, xOffset = 0, yOffset = 0 } = options
  const triggerRect = 'getBoundingClientRect' in anchor ? anchor.getBoundingClientRect() : anchor
  const popoverRect = popover.getBoundingClientRect()
  const viewportHeight = window.innerHeight

  let top: number
  let flipped = false

  if (placement === 'above') {
    top = triggerRect.top - popoverRect.height - offset
    flipped = true
  }
  else {
    const belowTop = triggerRect.bottom + offset
    const belowBottom = belowTop + popoverRect.height

    if (belowBottom > viewportHeight) {
      const aboveTop = triggerRect.top - popoverRect.height - offset
      if (aboveTop >= 0) {
        top = aboveTop
        flipped = true
      }
      else {
        // Not enough space either way -- pick the side with more room
        const spaceBelow = viewportHeight - triggerRect.bottom
        const spaceAbove = triggerRect.top
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
  // Start aligned with trigger's left edge, then clamp to viewport.
  let left = triggerRect.left
  const viewportWidth = window.innerWidth

  const rightOverflow = left + popoverRect.width - viewportWidth
  if (rightOverflow > 0) {
    left = Math.max(0, left - rightOverflow)
  }
  if (left < 0) {
    left = 0
  }

  top += yOffset
  left += xOffset

  if (left + popoverRect.width > viewportWidth) {
    left = Math.max(0, viewportWidth - popoverRect.width)
  }
  if (left < 0) {
    left = 0
  }
  if (top + popoverRect.height > viewportHeight) {
    top = Math.max(0, viewportHeight - popoverRect.height)
  }
  if (top < 0) {
    top = 0
  }

  return { top, left, flipped }
}
