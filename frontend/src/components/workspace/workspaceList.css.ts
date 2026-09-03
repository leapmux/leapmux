import { globalStyle, style } from '@vanilla-extract/css'
import { menuTrigger, sidebarActions } from '~/components/tree/sidebarActions.css'
import { clippedText } from '~/styles/shared.css'
import { iconSize } from '~/styles/tokens'

export const list = style({
  display: 'flex',
  flexDirection: 'column',
  padding: 'var(--space-1) 0',
})

export const sectionHeader = style({
  'display': 'flex',
  'alignItems': 'center',
  'gap': 'var(--space-1)',
  'padding': 'var(--space-1) var(--space-3)',
  'cursor': 'pointer',
  'userSelect': 'none',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
})

export const sectionChevron = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: '16px',
  height: '16px',
  flexShrink: 0,
  color: 'var(--faint-foreground)',
  transition: 'transform 0.15s',
})

export const sectionChevronOpen = style([sectionChevron, {
  transform: 'rotate(90deg)',
}])

export const sectionName = style([clippedText, {
  flex: 1,
  fontSize: 'var(--text-8)',
  fontWeight: 'var(--font-bold)',
  color: 'var(--muted-foreground)',
  textTransform: 'uppercase',
  letterSpacing: '0.5px',
}])

export const sectionActions = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  position: 'relative',
  width: '20px',
  flexShrink: 0,
})

export const sectionCount = style({
  fontSize: 'var(--text-8)',
  color: 'var(--faint-foreground)',
  transition: 'opacity var(--transition)',
  selectors: {
    [`${sectionHeader}:hover &.canToggle`]: {
      opacity: 0,
    },
  },
})

export const sectionAddButton = style({
  'all': 'unset',
  'display': 'flex',
  'alignItems': 'center',
  'justifyContent': 'center',
  'width': iconSize.container.sm,
  'height': iconSize.container.sm,
  'borderRadius': '3px',
  'flexShrink': 0,
  'color': 'var(--faint-foreground)',
  'cursor': 'pointer',
  'position': 'absolute',
  'opacity': 0,
  'transition': 'opacity var(--transition)',
  ':hover': {
    color: 'var(--foreground)',
  },
  'selectors': {
    [`${sectionHeader}:hover &`]: {
      opacity: 1,
    },
  },
})

/**
 * The workspace tree's row container, which sizes to its WIDEST row.
 *
 * `width: max-content` is deliberate: the tab tree indents each level, so a deep
 * branch runs past the sidebar, and the enclosing scroller must be able to
 * reveal it sideways.
 *
 * A section whose rows should CLIP must not use this container. The width makes
 * the row grow instead of the label shrinking, so the ellipsis never appears and
 * the whole section scrolls sideways -- which is what the workers list did while
 * it rendered in here. It has its own container now: `workerItems` in
 * `~/components/workers/workerSection.css.ts`.
 */
export const sectionItems = style({
  display: 'flex',
  flexDirection: 'column',
  minWidth: '100%',
  width: 'max-content',
})

export const item = style({
  'display': 'flex',
  'alignItems': 'center',
  'padding': 'var(--space-1) var(--space-2)',
  'paddingLeft': 'var(--space-1)',
  'cursor': 'pointer',
  'gap': 'var(--space-1)',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
})

export const itemActive = style({
  'backgroundColor': 'var(--secondary)',
  'paddingRight': 0,
  ':hover': {
    backgroundColor: 'var(--muted)',
  },
})

globalStyle(`${item}${itemActive}::after`, {
  content: '""',
  position: 'sticky',
  right: 0,
  width: '2px',
  alignSelf: 'stretch',
  margin: 'calc(-1 * var(--space-1)) 0',
  backgroundColor: 'var(--primary)',
  flexShrink: 0,
})

// Wraps the workspace title, shared-badge, and diff-stats-badge together so
// a single tooltip can cover the pair. flex: 0 1 auto keeps the wrapper
// sized to its content (shrinking with ellipsis when the row is narrow)
// so the tooltip centers over the visible label, not trailing empty space.
export const itemLabel = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  flex: '0 1 auto',
  minWidth: 0,
  overflow: 'hidden',
})

export const itemTitle = style([clippedText, {
  fontSize: 'var(--text-7)',
  fontWeight: 'var(--font-normal)',
  color: 'var(--foreground)',
}])

export const itemRenameInput = style({
  'flex': 1,
  'fontSize': 'var(--text-7)',
  'fontWeight': 'var(--font-normal)',
  'color': 'var(--foreground)',
  'backgroundColor': 'var(--background)',
  'border': '1px solid var(--ring)',
  'borderRadius': 'var(--radius-small)',
  'padding': '0 var(--space-1)',
  'outline': 'none',
  'minWidth': 0,
  ':focus-visible': {
    boxShadow: '0 0 0 2px var(--ring)',
  },
})

export const itemDragging = style({
  opacity: 0.4,
})

export const itemDropTarget = style({
  outline: '2px dashed var(--primary)',
  outlineOffset: '-2px',
})

/** Suppress sidebarActions hover when the item is a drop target. */
globalStyle(`${item}${itemDropTarget}:hover > ${sidebarActions}`, {
  backgroundColor: 'transparent',
})

globalStyle(`${item}${itemDropTarget}:hover > ${sidebarActions} ${menuTrigger}`, {
  opacity: 0,
})

/**
 * The per-section filter box, shown by the header menu's "Filter workspaces"
 * toggle.
 *
 * It sits in the section BODY rather than inside the menu popover, which keeps
 * the menu a plain `menu` (a `div` popover would force a per-item close) and
 * leaves the box usable while the menu is closed.
 */
export const filterRow = style({
  padding: 'var(--space-1) var(--space-2)',
})

export const filterInput = style({
  'width': '100%',
  'fontSize': 'var(--text-7)',
  'color': 'var(--foreground)',
  'backgroundColor': 'var(--background)',
  'border': '1px solid var(--border)',
  'borderRadius': 'var(--radius-small)',
  'padding': '0 var(--space-2)',
  'outline': 'none',
  'minWidth': 0,
  ':focus-visible': {
    boxShadow: '0 0 0 2px var(--ring)',
  },
})

export const emptySection = style({
  padding: 'var(--space-2) var(--space-4)',
  paddingLeft: 'var(--space-4)',
  fontSize: 'var(--text-7)',
  color: 'var(--faint-foreground)',
  fontStyle: 'italic',
})

export const sectionHeaderDropTarget = style({
  backgroundColor: 'var(--secondary)',
})

export const dragOverlay = style([clippedText, {
  padding: 'var(--space-1) var(--space-3)',
  paddingLeft: 'var(--space-4)',
  fontSize: 'var(--text-7)',
  fontWeight: 'var(--font-normal)',
  color: 'var(--foreground)',
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-small)',
  boxShadow: 'var(--shadow-large)',
  maxWidth: '200px',
}])
