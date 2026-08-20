import { style } from '@vanilla-extract/css'
import { menuSectionHeader } from '~/styles/shared.css'
import { breakpoints } from '~/styles/tokens'

/**
 * The Preferences dialog layout: a fixed nav column and a scrolling panel
 * column, each its own scroll container. Written from scratch for the
 * category-navigation dialog (the old two-tab layout's styles are gone with
 * its components).
 */
export const layout = style({
  'display': 'grid',
  'gridTemplateColumns': '260px 1fr',
  'gap': 'var(--space-6)',
  'flex': 1,
  'minHeight': 0,
  // Grid items default to min-width: auto (min-content). Phone-band
  // controls and number inputs are wider than the dialog; without this the
  // layout grows past the dialog's right edge instead of shrinking.
  'minWidth': 0,
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      gridTemplateColumns: '1fr',
      // Nav sizes to its content; the panel fills what is left and scrolls.
      // Default align-content:stretch grew BOTH auto rows and left a gap
      // between the section picker and a short settings list (the list
      // looked vertically centered in the dialog).
      gridTemplateRows: 'auto 1fr',
      gap: 'var(--space-3)',
    },
  },
})

/**
 * The nav column: its own scroll container on desktop.
 *
 * A flex column so the tab list below the search box can claim the height
 * that is left, however few tabs it holds. As a block container the list
 * ended at its last tab and the rest of the column was dead space.
 */
export const navColumn = style({
  'display': 'flex',
  'flexDirection': 'column',
  'minHeight': 0,
  'minWidth': 0,
  'overflowY': 'auto',
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      overflowY: 'visible',
    },
  },
})

/** The panel column: its own scroll container. */
export const panelColumn = style({
  minHeight: 0,
  minWidth: 0,
  overflowY: 'auto',
  overflowX: 'hidden',
  paddingRight: 'var(--space-2)',
})

export const nav = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  // Clear space at the two ends of the list: under the search box above it,
  // and below the last tab. `gap` cannot supply either -- it only spaces
  // items apart from each other, never from the edges of the column.
  padding: 'var(--space-3) 0',
  // Fill the column's remaining height rather than ending at the last tab.
  // A list longer than the column still overflows and scrolls, because the
  // default `min-height: auto` refuses to shrink a flex item below its
  // content.
  flex: 1,
})

/** Phone-band section picker: oat-styled trigger (mirrors form select chrome). */
export const navSelect = style({
  'width': '100%',
  'marginBottom': 'var(--space-1)',
  'padding': 'var(--space-2) var(--space-3)',
  'fontSize': 'var(--text-7)',
  'lineHeight': 'var(--leading-normal)',
  'backgroundColor': 'var(--background)',
  'color': 'var(--foreground)',
  'border': '1px solid var(--input)',
  'borderRadius': 'var(--radius-medium)',
  'transition': 'border-color var(--transition-fast), box-shadow var(--transition-fast)',
  'display': 'flex',
  'alignItems': 'center',
  'justifyContent': 'space-between',
  'gap': 'var(--space-3)',
  'textAlign': 'left',
  ':focus-visible': {
    outline: 'none',
    borderColor: 'var(--ring)',
    boxShadow: '0 0 0 2px rgb(from var(--ring) r g b / 0.2)',
  },
})

export const navSelectValue = style({
  minWidth: 0,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
})

export const navSelectChevron = style({
  color: 'var(--muted-foreground)',
  flexShrink: 0,
})

/** Compact section menu: themed popover, not the OS native picker. */
export const navMenu = style({
  margin: 0,
  minWidth: '16rem',
  maxWidth: 'min(24rem, calc(100vw - 2rem))',
  padding: 'var(--space-1)',
  backgroundColor: 'var(--background)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  boxShadow: 'var(--shadow-medium)',
})

export const navButton = style({
  'textAlign': 'start',
  'padding': 'var(--space-2) var(--space-3)',
  'backgroundColor': 'transparent',
  'color': 'var(--muted-foreground)',
  'border': 'none',
  'borderRadius': 'var(--radius-medium)',
  'cursor': 'pointer',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
})

export const navButtonActive = style({
  backgroundColor: 'var(--card)',
  color: 'var(--foreground)',
  fontWeight: 'var(--font-medium)',
})

/**
 * A section header in the desktop tab list, drawn as a dropdown menu draws
 * one. The typography comes from `menuSectionHeader` rather than a second
 * copy of it, so this list and the compact DropdownMenu that replaces it
 * below `sm` cannot drift apart -- that menu already uses the shared style
 * for the same two headers.
 *
 * The header carries its own padding, so the leading PREFERENCES header
 * sits flush under the search box with no margin to reset. `navSeparator`
 * supplies the space above ADMINISTRATION.
 */
export const navDivider = style([menuSectionHeader, {
  userSelect: 'none',
}])

/**
 * The rule above a section header, matching the `<hr>` a dropdown menu
 * draws between its sections.
 *
 * The margin is stated here because the global `ot-dropdown hr` override
 * cannot reach it: this list is a `role="tablist"`, not a dropdown, so
 * without this it would inherit Oat's base `hr` margin of `var(--space-8)`
 * (2rem) and push the admin sections far down the column.
 *
 * It is one step SMALLER than the `var(--space-2)` that override uses,
 * because `nav` is a flex column whose `var(--space-1)` gap adds to each
 * side. The rule lands at the same `var(--space-2)` of clear space as the
 * menu's. Retune this if that gap changes.
 */
export const navSeparator = style({
  margin: 'var(--space-1) 0',
})

export const searchInput = style({
  width: '100%',
  marginBottom: 'var(--space-3)',
  // A flex item shrinks along the main axis by default, and the column's
  // main axis is vertical. Without this a short dialog squashes the search
  // box instead of scrolling the tab list under it.
  flexShrink: 0,
})

export const searchResults = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
})

export const searchGroupTitle = style({
  fontSize: 'var(--text-8)',
  color: 'var(--faint-foreground)',
  textTransform: 'uppercase',
  letterSpacing: '0.08em',
  marginTop: 'var(--space-2)',
})

export const searchResultButton = style({
  'display': 'block',
  'width': '100%',
  'textAlign': 'start',
  'padding': 'var(--space-2) var(--space-3)',
  'backgroundColor': 'transparent',
  'color': 'var(--foreground)',
  'border': 'none',
  'borderRadius': 'var(--radius-medium)',
  'cursor': 'pointer',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
})

export const searchResultHelp = style({
  display: 'block',
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})
