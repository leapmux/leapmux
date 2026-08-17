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
