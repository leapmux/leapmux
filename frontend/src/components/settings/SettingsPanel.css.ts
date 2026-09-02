import { globalStyle, style } from '@vanilla-extract/css'
import { row } from './SettingRow.css'

/** One category's settings list — no panel padding; rows carry their own. */
export const panel = style({
  display: 'flex',
  flexDirection: 'column',
  // Beat oat's `@layer components` `[role="tabpanel"] { padding: space-4 0 }`.
  // vanilla-extract emits unlayered rules, so restating padding wins without a
  // specificity fight (the same mechanic as `~/components/common/ErrorFallback.css.ts`).
  padding: 0,
  // Beat Oat's `[role="tabpanel"]:not([hidden]) { animation: fade-in-up .3s both }`
  // the same way, and this one is not cosmetic.
  //
  // `fade-in-up` ends on `transform: translateY(0)`, and `animation-fill-mode:
  // both` RETAINS that final keyframe -- so the panel keeps an identity
  // transform for ever. A transform, identity or not, makes an element the
  // containing block for every `position: fixed` descendant, and this panel
  // holds the Preferences dialog's menus. `calcPopoverPosition` places those in
  // VIEWPORT coordinates, so they resolve against the panel instead: the damage
  // is hidden while the popover is in the top layer and appears on the way out,
  // as a menu that jumps by the dialog's own offset while it closes.
  //
  // Dropped rather than replaced with an opacity-only fade: Oat ships one
  // keyframe set and a second copy here would be a fade this project then owns.
  animation: 'none',
})

// Drop the list's outer top padding so the first row sits flush with the panel
// column (and with a leading restart alert when one is present).
globalStyle(`${panel} > .${row}:first-child`, {
  paddingTop: 0,
})

globalStyle(`${panel} > [role="alert"] + .${row}`, {
  paddingTop: 0,
})

globalStyle(`${panel} > [role="alert"]`, {
  marginBottom: 'var(--space-3)',
})
