export interface PopoverPositionOptions {
  /** 'auto' = flip upward if clipped; 'above' = always position above trigger */
  placement?: 'auto' | 'above'
  /** Pixel gap between trigger and popover (default: 0) */
  offset?: number
}

/**
 * Calculate the top/left for a fixed-position popover so it doesn't
 * overflow the bottom of the viewport.
 */
export function calcPopoverPosition(
  trigger: Element,
  popover: HTMLElement,
  options: PopoverPositionOptions = {},
): { top: number, left: number, flipped: boolean } {
  const { placement = 'auto', offset = 0 } = options
  const triggerRect = trigger.getBoundingClientRect()
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

  return { top, left, flipped }
}

/**
 * Position a popover directly above a trigger element.
 * Convenience wrapper for imperative use (e.g. the link popover).
 */
export function positionPopoverAbove(
  trigger: Element,
  popover: HTMLElement,
  offset = 4,
): void {
  const triggerRect = trigger.getBoundingClientRect()
  const popoverRect = popover.getBoundingClientRect()
  popover.style.top = `${triggerRect.top - popoverRect.height - offset}px`
  popover.style.left = `${triggerRect.left}px`
  popover.setAttribute('data-flipped', '')
}
