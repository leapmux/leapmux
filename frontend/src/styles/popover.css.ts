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
 *
 * Single-sourced here so a new popover can't re-discover the "stays visible after close" /
 * "margin:auto re-centers" bugs the hard way.
 */
export const popoverBase = style({
  position: 'fixed',
  margin: 0,
  selectors: {
    '&:popover-open': {
      display: 'flex',
    },
  },
})
