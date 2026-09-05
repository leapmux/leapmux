import { style } from '@vanilla-extract/css'

// The rows of the "Open in ..." application list, shared by the title bar's
// split button and by every context menu that offers the same list. The visual
// language mirrors `~/components/shell/AgentProviderSelector.css.ts`: those two
// selectors are meant to read as one family.

// Only what is specific to THIS menu. Everything a menu item needs to look like
// one -- the reset, the type scale, the hover fill -- comes from the shared
// [role="menuitem"] rule in `~/styles/global.css.ts`, and the layout half
// (display, width, padding, cursor, focus outline) from Oat's own rule.
export const menuItem = style({
  justifyContent: 'space-between',
})

export const menuItemSelected = style({
  backgroundColor: 'var(--accent)',
})

export const menuItemValue = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  minWidth: 0,
})

export const check = style({
  color: 'var(--primary)',
  flexShrink: 0,
})

// Separator between the file manager, the editor list, and the trailing
// "Refresh" action. Margin is non-negative so the rule stays inside the menu's
// content area -- negative horizontal margins extend past the padding and
// surface as a horizontal scrollbar on the popover.
export const menuSeparator = style({
  margin: 'var(--space-1) 0',
  border: 'none',
  borderTop: '1px solid var(--border)',
})
