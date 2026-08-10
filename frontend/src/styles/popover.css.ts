import { style } from '@vanilla-extract/css'

/**
 * The base class a `popover="auto"` element needs when it is positioned by JS (an
 * explicit top/left written by our own positioning code) rather than by the UA default.
 * Compose it into each popover style via `style([popoverBase, { ...own }])` -- as a
 * COMPOSED class (not a spread rule) so both classes land on the element and a consumer's
 * own `&:popover-open` block (a grid display, an opacity/transform reveal) ADDS to the
 * base's `display: flex` instead of shallow-overriding it.
 *
 * Two rules, both load-bearing:
 *
 *  - `position: fixed; margin: 0` resets the UA popover defaults (`inset: 0; margin: auto`).
 *    Without the `margin: 0`, `margin: auto` re-centers the popover in the viewport even
 *    after our code sets top/left -- which clipped it and left a large dead area.
 *  - `display: flex` is gated on `:popover-open`. An author `display` set unconditionally
 *    beats the UA `[popover]:not(:popover-open) { display: none }` rule (author origin wins
 *    over UA regardless of specificity), so a bare `display: flex` would keep the popover
 *    laid out + visible (and, being `position: fixed`, covering the page) after it closes.
 *  - `pointer-events: none` while CLOSED. Oat's own `ot-dropdown [popover]` rule animates
 *    the close with `display` and `overlay` in `allow-discrete`, so for the length of that
 *    transition a closed popover is still laid out, still in the top layer, and still
 *    hit-testable — it swallows the very next click, wherever the user aimed it. A popover
 *    anchored over its own trigger therefore could not be reopened: the click that should
 *    have reopened it landed on the fading corpse instead. Gating hit-testing on the open
 *    state fixes that without giving up the reveal animation.
 *
 * Single-sourced here so a new popover can't re-discover the "stays visible after close" /
 * "margin:auto re-centers" / "eats the next click while closing" bugs the hard way.
 */
export const popoverBase = style({
  position: 'fixed',
  margin: 0,
  selectors: {
    '&:popover-open': {
      display: 'flex',
    },
    '&:not(:popover-open)': {
      pointerEvents: 'none',
    },
  },
})
