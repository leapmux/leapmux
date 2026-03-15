import { globalStyle, style } from '@vanilla-extract/css'
import { resizeHandleSelectors } from '~/styles/resizeHandle'
import { headerHeight, iconSize } from '~/styles/tokens'

/** Inner flex-column wrapper for the expanded sidebar. */
export const sidebarInner = style({
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  overflow: 'hidden',
  minHeight: 0,
})

/** Collapsible pane: collapsed state (header only). */
export const collapsiblePane = style({
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
  minHeight: 0,
  flex: '0 0 auto',
  border: 'none',
  borderRadius: 0,
})

/** Expanded pane grows to fill available space. */
export const collapsiblePaneExpanded = style({
  flex: 1,
})

/** Content wrapper inside each collapsible pane. */
export const collapsibleContent = style({
  display: 'grid',
  gridTemplateRows: '0fr',
  visibility: 'hidden',
  overflow: 'hidden',
  minHeight: 0,
  margin: 0,
  padding: 0,
})

/** Expanded content — grid row grows to fill available space. */
export const collapsibleContentExpanded = style({
  gridTemplateRows: '1fr',
  visibility: 'visible',
  flex: 1,
})

/** Inner flex wrapper preserving the original content layout. */
export const collapsibleContentInner = style({
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
  minHeight: 0,
})

export const sidebarTitle = style({
  fontSize: 'var(--text-7)',
  fontWeight: 600,
  color: 'var(--muted-foreground)',
  textTransform: 'uppercase',
  letterSpacing: '0.5px',
  lineHeight: iconSize.container.md,
})

export const sidebarContent = style({
  flex: 1,
  minHeight: 0,
  overflow: 'auto',
})

export const sidebarHeaderActions = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  marginRight: '-4px',
})

export const collapsibleTrigger = style({
  'display': 'flex',
  'alignItems': 'center',
  'gap': 'var(--space-1)',
  'padding': '0 var(--space-3) 0 var(--space-4)',
  'minHeight': headerHeight,
  'borderBottom': '1px solid var(--border)',
  'flexShrink': 0,
  'position': 'relative',
  'cursor': 'pointer',
  'userSelect': 'none',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
  '::after': {
    content: '""',
    width: '1em',
    height: '1em',
    flexShrink: 0,
    backgroundColor: 'currentColor',
    maskImage: `url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'%3E%3Cpath d='m6 9 6 6 6-6'/%3E%3C/svg%3E")`,
    maskSize: 'contain',
    maskRepeat: 'no-repeat',
    transition: 'transform var(--transition-fast)',
  },
})

/** Non-interactive trigger (used when the pane is the only one visible). */
export const collapsibleTriggerStatic = style({
  'cursor': 'default',
  ':hover': {
    backgroundColor: 'unset',
  },
  '::after': {
    display: 'none',
  },
})

/** Right-sidebar triggers keep the original --space-4 right padding. */
export const collapsibleTriggerRight = style({
  paddingRight: 'var(--space-4)',
})

/** Hide the OAT accordion chevron on a section header (e.g. first right-sidebar section). */
export const collapsibleTriggerNoChevron = style({
  '::after': {
    display: 'none',
  },
})

/** Make the title expand so action buttons stay grouped on the right. */
globalStyle(`${collapsibleTrigger} > ${sidebarTitle}`, {
  flex: 1,
})

/** Rotate chevron when the pane is expanded. */
globalStyle(`${collapsiblePaneExpanded} > ${collapsibleTrigger}::after`, {
  transform: 'rotate(180deg)',
})

/** Hide bottom border on collapsed pane triggers. */
globalStyle(`${collapsiblePane}:not(${collapsiblePaneExpanded}) > ${collapsibleTrigger}`, {
  borderBottom: 'none',
})

/** Add top border to non-first pane triggers. */
globalStyle(`${collapsiblePane} ~ ${collapsiblePane} > ${collapsibleTrigger}`, {
  borderTop: '1px solid var(--border)',
})

export const paneResizeHandle = style({
  height: '4px',
  flexShrink: 0,
  cursor: 'row-resize',
  position: 'relative',
  userSelect: 'none',
  margin: '-2px 0',
  zIndex: 5,
  selectors: resizeHandleSelectors('vertical'),
})

export const paneResizeHandleActive = style({
  selectors: {
    '&::before': {
      background: 'var(--primary) !important',
      height: '1px !important',
    },
  },
})

/** Narrow icon rail shown when a sidebar is collapsed. */
export const sidebarRail = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  gap: 'var(--space-1)',
  padding: 'var(--space-1) 0 var(--space-2)',
  width: '100%',
  height: '100%',
  backgroundColor: 'var(--card)',
  overflow: 'hidden',
})

/** Left rail variant. */
export const sidebarRailLeft = style({})

/** Right rail variant. */
export const sidebarRailRight = style({})

/** Container for a badge section in collapsed rail (e.g. todo progress). */
export const railBadgeSection = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  gap: '2px',
})

/** Small muted counter text (e.g. "3/5") in collapsed rail. */
export const railBadgeText = style({
  fontSize: 'var(--text-8)',
  color: 'var(--faint-foreground)',
  lineHeight: 1,
})

export const marginTopAuto = style({
  marginTop: 'auto',
})

/**
 *  Bottom section in expanded sidebar (non-collapsible, pushed to bottom).
 *  Matches collapsibleTrigger height so rail-only sections like UserMenu
 *  align visually with the collapsible section headers above.
 */
export const bottomSection = style({
  marginTop: 'auto',
  flexShrink: 0,
  padding: '0 var(--space-4)',
  minHeight: headerHeight,
  display: 'flex',
  alignItems: 'center',
  borderTop: '1px solid var(--border)',
})

/** Drag handle for section headers (visible on hover, absolutely positioned). */
export const sectionDragHandle = style({
  'position': 'absolute',
  'left': 0,
  'top': 0,
  'bottom': 0,
  'width': 'var(--space-4)',
  'display': 'flex',
  'alignItems': 'center',
  'justifyContent': 'center',
  'cursor': 'grab',
  'opacity': 0,
  'transition': 'opacity 0.15s',
  'color': 'var(--muted-foreground)',
  'zIndex': 1,
  ':active': {
    cursor: 'grabbing',
  },
})

/** Show drag handle on trigger hover. */
globalStyle(`${collapsibleTrigger}:hover ${sectionDragHandle}`, {
  opacity: 0.6,
})
globalStyle(`${collapsibleTrigger}:hover ${sectionDragHandle}:hover`, {
  opacity: 1,
})

/** Visual state while a section is being dragged. */
export const collapsiblePaneDragging = style({
  opacity: 0.5,
})

/** Horizontal line indicating where a dragged section will be inserted. */
export const dropIndicatorLine = style({
  height: '2px',
  backgroundColor: 'var(--primary)',
  flexShrink: 0,
  borderRadius: '1px',
  margin: '-1px 0',
  position: 'relative',
  zIndex: 10,
  pointerEvents: 'none',
})

/** Placeholder shown when a sidebar has no sections. */
export const emptyDropZone = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  flex: 1,
  minHeight: '60px',
  color: 'var(--faint-foreground)',
  fontSize: 'var(--text-7)',
  fontStyle: 'italic',
  border: '2px dashed var(--border)',
  borderRadius: '4px',
  margin: 'var(--space-2)',
})

/** Active state when a section drag is in progress over an empty sidebar. */
export const emptyDropZoneActive = style({
  borderColor: 'var(--primary)',
  color: 'var(--muted-foreground)',
  backgroundColor: 'color-mix(in srgb, var(--primary) 5%, transparent)',
})
