import { globalStyle } from '@vanilla-extract/css'
import { motion } from '~/styles/tokens'

/**
 * How long before the hold completes the reduced-motion step lands. The step is
 * feedback, not decoration, so it must arrive while the finger is still down and
 * early enough to read as "ready", but late enough that an ordinary tap never
 * flashes it.
 */
const REDUCED_MOTION_STEP_LEAD_MS = 120

/**
 * The accent tint that grows while a finger holds a row.
 *
 * Every rule keys on the `data-ctx-menu` attribute that
 * `attachContextMenuGesture` sets on the row (see ./contextMenuGesture.ts), not
 * on a class. The rows that mount the gesture assign their own `class`
 * reactively, and that assignment replaces the whole class list, so a class
 * added once at attach would not survive the row's next class change (a tab
 * becoming active, a chat row reclassifying mid-stream). An attribute does.
 *
 * The tint is a `::before` overlay at `z-index: -1` inside an isolated row, so it
 * paints ABOVE the row's own `background-color` and BELOW its content. That is the
 * one layer that composites with the hover, selected and drop-target backgrounds a
 * row already carries instead of replacing them.
 *
 * `box-shadow: inset 0 0 0 9999px` reaches the same layer with one property and no
 * stacking context, and it is the wrong choice here: `box-shadow` does not compose,
 * so a later rule that gives any of these rows a shadow would silently delete the
 * indicator with no failing test and no visible cause.
 *
 * Each state owns the transition INTO itself: the base rule fades the tint out on
 * release, and the `[data-press-hold]` rule ramps it in during the press. A single
 * transition on the base rule cannot express this, because the press needs the
 * whole hold duration and the release must be immediate.
 *
 * Reduced motion is the DEFAULT and the ramp is the opt-in, following the rule
 * recorded in ~/components/chat/ChatScrollRail.css.ts: two blocks setting the same
 * property at equal specificity would have to win by source order, which a key
 * reorder silently breaks. Under reduced motion the tint arrives as a single step
 * shortly before the hold completes -- `prefers-reduced-motion` asks for less
 * motion, not less information, and this indicator is the only signal the gesture
 * emits before the menu appears.
 *
 * (`globalStyle`'s rule type takes no nested `selectors` under `@media`, so each
 * media-scoped block is its own call with the query in the selector string.)
 */
globalStyle('[data-ctx-menu]', {
  position: 'relative',
  // Without this, `z-index: -1` on the pseudo-element escapes to the nearest
  // ancestor stacking context and paints behind the row's own background.
  isolation: 'isolate',
})

globalStyle('[data-ctx-menu]::before', {
  content: '""',
  position: 'absolute',
  inset: 0,
  zIndex: -1,
  borderRadius: 'inherit',
  pointerEvents: 'none',
  background: 'var(--accent)',
  opacity: 0,
  transition: 'opacity 1ms linear',
})

// `attachContextMenuGesture` sets the attribute on pointerdown and removes it
// when the gesture ends, so the ramp runs forward on press and unwinds on
// release with no second class to keep in step.
globalStyle('[data-ctx-menu][data-press-hold]::before', {
  opacity: 1,
  transition: `opacity 1ms linear ${motion.longPress - REDUCED_MOTION_STEP_LEAD_MS}ms`,
})

globalStyle('@media (prefers-reduced-motion: no-preference) { [data-ctx-menu]::before }', {
  transition: `opacity ${motion.fast}ms ease-out`,
})

globalStyle('@media (prefers-reduced-motion: no-preference) { [data-ctx-menu][data-press-hold]::before }', {
  transition: `opacity ${motion.longPress}ms linear`,
})

/**
 * The default variant: `attachContextMenuGesture` writes the `owned` value itself
 * (see ./contextMenuGesture.ts), so a row opts into the gesture and its paint with
 * one call and cannot receive half of it.
 *
 * This variant owns the long press outright. `-webkit-touch-callout` and
 * `user-select` are both off, because iOS raises the selection callout on a long
 * press over any selectable text and that callout would cover the menu. Every row
 * that uses it is chrome, not prose.
 */
globalStyle('[data-ctx-menu="owned"]', {
  WebkitTouchCallout: 'none',
  userSelect: 'none',
  WebkitUserSelect: 'none',
})

/**
 * The variant for an element whose text must stay selectable -- the chat message
 * rows, which the gesture marks `selectable`.
 *
 * The suppression is scoped to `(pointer: coarse)`: a phone trades partial-text
 * selection for the menu (which carries Copy and Quote in its place), while a mouse
 * -- including a hybrid laptop's, where `pointer` is `fine` and only `any-pointer`
 * is coarse -- keeps selection intact and reaches the menu through right-click.
 */
globalStyle('@media (pointer: coarse) { [data-ctx-menu="selectable"] }', {
  WebkitTouchCallout: 'none',
  userSelect: 'none',
  WebkitUserSelect: 'none',
})
