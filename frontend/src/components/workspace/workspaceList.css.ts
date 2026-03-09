import { globalStyle, style } from '@vanilla-extract/css'
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

export const sectionName = style({
  flex: 1,
  fontSize: 'var(--text-8)',
  fontWeight: 600,
  color: 'var(--muted-foreground)',
  textTransform: 'uppercase',
  letterSpacing: '0.5px',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  minWidth: 0,
})

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
  transition: 'opacity 0.15s',
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
  'transition': 'opacity 0.15s',
  ':hover': {
    color: 'var(--foreground)',
  },
  'selectors': {
    [`${sectionHeader}:hover &`]: {
      opacity: 1,
    },
  },
})

export const sectionItems = style({
  display: 'flex',
  flexDirection: 'column',
  minWidth: '100%',
  width: 'max-content',
})

export const item = style({
  'display': 'flex',
  'alignItems': 'center',
  'padding': 'var(--space-1) var(--space-3)',
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

export const itemTitle = style({
  fontSize: 'var(--text-7)',
  fontWeight: 400,
  color: 'var(--foreground)',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  minWidth: 0,
})

export const itemRenameInput = style({
  'flex': 1,
  'fontSize': 'var(--text-7)',
  'fontWeight': 400,
  'color': 'var(--foreground)',
  'backgroundColor': 'var(--background)',
  'border': '1px solid var(--ring)',
  'borderRadius': 'var(--radius-small)',
  'padding': '0 var(--space-1)',
  'outline': 'none',
  'minWidth': 0,
  ':focus': {
    boxShadow: '0 0 0 2px var(--ring)',
  },
})

export const itemActions = style({
  display: 'flex',
  alignItems: 'center',
  flexShrink: 0,
  marginLeft: 'auto',
  position: 'sticky',
  right: 'var(--space-2)',
})

/** Give itemActions a background on hover so it covers scrolled text underneath. */
globalStyle(`${item}:hover ${itemActions}`, {
  backgroundColor: 'var(--card)',
})

/** Match active item background. */
globalStyle(`${item}${itemActive} ${itemActions}`, {
  backgroundColor: 'var(--secondary)',
})

/** Match active + hover item background. */
globalStyle(`${item}${itemActive}:hover ${itemActions}`, {
  backgroundColor: 'var(--muted)',
})

export const itemMenuTrigger = style({
  opacity: 0,
  transition: 'opacity 0.15s',
  selectors: {
    [`${item}:hover &`]: {
      opacity: 1,
    },
    '&[aria-expanded="true"]': {
      opacity: 1,
    },
  },
})

export const sharedBadge = style({
  fontSize: 'var(--text-8)',
  color: 'var(--primary)',
  fontWeight: 400,
  flexShrink: 0,
})

export const emptySection = style({
  padding: 'var(--space-2) var(--space-4)',
  paddingLeft: 'var(--space-4)',
  fontSize: 'var(--text-7)',
  color: 'var(--faint-foreground)',
  fontStyle: 'italic',
})

export const itemDragging = style({
  opacity: 0.4,
})

export const sectionHeaderDropTarget = style({
  backgroundColor: 'var(--secondary)',
})

export const dragOverlay = style({
  padding: 'var(--space-1) var(--space-3)',
  paddingLeft: 'var(--space-4)',
  fontSize: 'var(--text-7)',
  fontWeight: 400,
  color: 'var(--foreground)',
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-small)',
  boxShadow: 'var(--shadow-large)',
  whiteSpace: 'nowrap',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  maxWidth: '200px',
})
