import { style } from '@vanilla-extract/css'

// Split-button container: icon+label face on the left, chevron face on the right.
// The two faces share a single border so the visual reads as one control.
export const splitButton = style({
  'display': 'inline-flex',
  'alignItems': 'stretch',
  'height': '24px',
  'borderRadius': 'var(--radius-small)',
  'overflow': 'hidden',
  'color': 'var(--muted-foreground)',
  // Faint outline only — the titlebar is busy enough already.
  ':hover': {
    color: 'var(--foreground)',
  },
})

// OAT's @layer base applies `border-radius: var(--radius-medium)` to every
// <button>; if we don't reset it the chevron face's hover background paints
// with 6px-rounded corners, which on an 18×24 hit area reads as a "bubble".
// Zeroing it here lets the splitButton container's own rounded corners be
// the only visible corner radius.
const buttonReset = {
  appearance: 'none' as const,
  background: 'none',
  border: 'none',
  borderRadius: 0,
  padding: 0,
  margin: 0,
  font: 'inherit',
  color: 'inherit',
  cursor: 'pointer',
}

export const mainFace = style({
  ...buttonReset,
  'display': 'inline-flex',
  'alignItems': 'center',
  'gap': 'var(--space-1)',
  // A little breathing room on the right so the label isn't hard up against
  // the chevron, but tighter than var(--space-2) so the two faces still
  // read as a single control.
  'padding': '0 var(--space-1) 0 var(--space-2)',
  'fontSize': 'var(--text-7)',
  'whiteSpace': 'nowrap',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
})

export const chevronFace = style({
  ...buttonReset,
  'display': 'inline-flex',
  'alignItems': 'center',
  'justifyContent': 'center',
  // Just enough to give the icon a comfortable hit target without a
  // visible gap between it and the label.
  'width': '16px',
  'paddingRight': 'var(--space-1)',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
  ':disabled': {
    cursor: 'default',
    opacity: 0.7,
  },
})

// Menu visual language mirrors `AgentProviderSelector.css.ts`. We don't import
// from there — the trigger styling above is bespoke to the split-button — but
// the popover / item look is intentionally identical so the two selectors
// feel like the same family.
export const menu = style({
  margin: 0,
  minWidth: '12rem',
  padding: 'var(--space-1)',
  backgroundColor: 'var(--background)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  boxShadow: 'var(--shadow-medium)',
  // Override browser defaults for <menu>, which would otherwise apply
  // `padding-inline-start: 40px` and disc list-style.
  listStyle: 'none',
})

// Only what is specific to THIS menu. Everything a menu item needs to look like
// one -- the reset, the type scale, the hover fill -- now comes from the shared
// [role="menuitem"] rule in the global stylesheet, and the layout half
// (display, width, padding, cursor, focus outline) from Oat's own rule. This
// block used to restate all of it: a private workaround for the Oat coupling
// that shared rule now owns, and a second copy that could only drift from it.
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

// Separator between the editor list and the trailing "Refresh" action.
// Margin is non-negative so the rule stays inside the menu's content area —
// negative horizontal margins extend past the padding and surface as a
// horizontal scrollbar on the popover.
export const menuSeparator = style({
  margin: 'var(--space-1) 0',
  border: 'none',
  borderTop: '1px solid var(--border)',
})
