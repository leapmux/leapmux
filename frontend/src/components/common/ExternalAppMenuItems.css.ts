import { style } from '@vanilla-extract/css'

// The rows of the "Open in ..." application list, shared by the title bar's
// split button and by every context menu that offers the same list.
//
// There is one class, and there is deliberately nothing else here. The rows are
// `DropdownMenuCheckableItem`s, which own their own layout, their radio
// indicator and their checked styling; the separators are bare `<hr>` elements,
// which `~/styles/global.css.ts` already sizes for every menu in the app
// (`ot-dropdown hr`). A local separator class beat that element selector on
// specificity and set half its margin, so this one menu's rules sat out of step
// with every other menu -- for a rule it restated rather than changed.

/** The refresh action, whose spinner needs the icon to keep its own size. */
export const refreshRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  minWidth: 0,
})
