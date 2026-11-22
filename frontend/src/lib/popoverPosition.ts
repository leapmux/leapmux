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

interface ViewportRect {
  top: number
  left: number
  width: number
  height: number
  bottom: number
  right: number
}

/**
 * Resolve the visible viewport rectangle in layout-viewport coordinates.
 *
 * On iOS Safari with the on-screen keyboard up, `window.innerHeight` shrinks to
 * `visualViewport.height` while `visualViewport.offsetTop` shifts the visible
 * band down in layout coordinates. Top-layer `position: fixed` popovers and
 * `getBoundingClientRect()` both use layout coordinates, so clamping against
 * `[0, window.innerHeight]` pushes a popover above the visible band. Using the
 * `visualViewport` rectangle keeps popovers inside the region actually on screen.
 */
function readViewportRect(): ViewportRect {
  const vv = typeof window !== 'undefined'
    ? window.visualViewport
    : null
  const width = vv && Number.isFinite(vv.width) && vv.width > 0
    ? vv.width
    : window.innerWidth
  const height = vv && Number.isFinite(vv.height) && vv.height > 0
    ? vv.height
    : window.innerHeight
  const left = vv && Number.isFinite(vv.offsetLeft)
    ? vv.offsetLeft
    : 0
  const top = vv && Number.isFinite(vv.offsetTop)
    ? vv.offsetTop
    : 0
  return {
    top,
    left,
    width,
    height,
    bottom: top + height,
    right: left + width,
  }
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
  let left = triggerRect.left

  const rightOverflow = left + popoverRect.width - viewport.right
  if (rightOverflow > 0) {
    left = Math.max(viewport.left, left - rightOverflow)
  }
  if (left < viewport.left) {
    left = viewport.left
  }

  top += yOffset
  left += xOffset

  if (left + popoverRect.width > viewport.right) {
    left = Math.max(viewport.left, viewport.right - popoverRect.width)
  }
  if (left < viewport.left) {
    left = viewport.left
  }
  if (top + popoverRect.height > viewport.bottom) {
    top = Math.max(viewport.top, viewport.bottom - popoverRect.height)
  }
  if (top < viewport.top) {
    top = viewport.top
  }

  return { top, left, flipped }
}
