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
 *  - `display: flex` applies only under `:popover-open`. An author `display` set unconditionally
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

/**
 * `popoverBase` plus a column layout clamped to the viewport.
 *
 * A popover sizes to its content, and content that grows without limit (a long
 * rate-limit list, a long list of to-dos, a long option catalog) has nothing
 * else to stop it running off the screen. Clamp both axes and scroll the
 * overflow instead.
 *
 * Deliberately NOT folded into `popoverBase`, although every consumer of this
 * class composes that one. `popoverBase` also carries the link and code-language
 * popovers in `~/components/chat/markdownEditor/MarkdownEditor.css.ts`, and that
 * file records why `overflow-y` must not reach them: with `overflow-y` inherited
 * from the popover chrome, CSS computes `overflow-x` to `auto` as well, and the
 * link card grew a horizontal scrollbar that pushed its remove button out of
 * view. One of them also sets a competing `max-width` of its own.
 */
export const popoverColumnClamp = style([popoverBase, {
  flexDirection: 'column',
  maxWidth: 'calc(100vw - var(--space-4) * 2)',
  maxHeight: 'calc(100vh - var(--space-6) * 2)',
  overflowY: 'auto',
}])

/**
 * The class list for a popover whose content is a CARD -- labelled rows, a list,
 * a panel -- and not a list of menu items. Apply it whole:
 * `<DropdownMenu as="div" class={popoverCard}>`.
 *
 * It is a class LIST, and both parts are load-bearing. Oat's own `card` rule
 * supplies the inset (`var(--space-6)` on each side), so every card popover uses
 * the standard card padding and follows Oat if that value changes -- two
 * surfaces of the SAME card cannot drift apart, which is what the agent-info
 * card did while each call site set its own padding. `popoverColumnClamp` adds
 * what a popover needs on top of a card: the positioning reset and the viewport
 * clamp.
 *
 * Exported as one string so that no call site can apply half of it.
 */
export const popoverCard = `card ${popoverColumnClamp}`
