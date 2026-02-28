import { globalStyle, style } from '@vanilla-extract/css'
import { codeViewContainer } from '~/components/chat/codeViewStyles.css'
import { spacing } from '~/styles/tokens'

export const container = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  overflow: 'hidden',
  position: 'relative',
})

export const content = style({
  flex: 1,
  overflow: 'auto',
  minHeight: 0,
})

export const statusBar = style({
  position: 'absolute',
  bottom: spacing.sm,
  right: spacing.md,
  display: 'flex',
  alignItems: 'center',
  gap: spacing.sm,
  padding: `${spacing.xs} ${spacing.sm}`,
  borderRadius: 'var(--radius-small)',
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  pointerEvents: 'none',
  opacity: 0.9,
})

export const statusMeta = style({
  whiteSpace: 'nowrap',
})

export const truncationWarning = style({
  color: 'var(--warning)',
  whiteSpace: 'nowrap',
})

export const loadingState = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  height: '100%',
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-7)',
})

export const errorState = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  height: '100%',
  color: 'var(--danger)',
  fontSize: 'var(--text-7)',
  padding: spacing.lg,
  textAlign: 'center',
})

export const imageSizeError = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  height: '100%',
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-7)',
  padding: spacing.lg,
  textAlign: 'center',
})

export const hexScroll = style({
  height: '100%',
  overflow: 'auto',
})

export const hexContainer = style({
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
  fontSize: 'var(--text-8)',
  lineHeight: 1.5,
  paddingInline: spacing.sm,
  whiteSpace: 'pre',
  width: 'max-content',
  marginInline: 'auto',
})

export const hexOffset = style({
  color: 'var(--faint-foreground)',
  userSelect: 'none',
})

export const hexSeparator = style({
  color: 'var(--faint-foreground)',
  userSelect: 'none',
})

export const hexAscii = style({
  color: 'var(--muted-foreground)',
})

export const textViewContainer = style({
  height: '100%',
  overflow: 'auto',
})

// Remove border/margin from code view when used inside the file viewer
// and move padding here so the scrollbar stays at the container edge.
globalStyle(`${textViewContainer} ${codeViewContainer}`, {
  border: 'none',
  borderRadius: 0,
  marginTop: 0,
  overflow: 'visible',
  paddingBlock: spacing.sm,
})

// Container for views that have a render/source toggle (markdown, SVG)
export const toggleViewContainer = style({
  position: 'relative',
  height: '100%',
  overflow: 'auto',
})

// Floating segmented toggle button at top-right
export const viewToggle = style({
  'position': 'absolute',
  'top': spacing.sm,
  'right': spacing.md,
  'zIndex': 10,
  'display': 'flex',
  'borderRadius': 'var(--radius-small)',
  'border': '1px solid var(--border)',
  'backgroundColor': 'var(--card)',
  'opacity': 0.8,
  'transition': 'opacity 0.15s',
  ':hover': {
    opacity: 1,
  },
})

export const viewToggleButton = style({
  all: 'unset',
  boxSizing: 'border-box',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: '28px',
  height: '28px',
  cursor: 'pointer',
  color: 'var(--muted-foreground)',
  transition: 'color 0.1s, background-color 0.1s',
  selectors: {
    '&:first-child': {
      borderRadius: 'var(--radius-small) 0 0 var(--radius-small)',
    },
    '&:last-child': {
      borderRadius: '0 var(--radius-small) var(--radius-small) 0',
    },
    '&:hover': {
      color: 'var(--foreground)',
    },
  },
})

export const viewToggleActive = style({
  backgroundColor: 'var(--accent)',
  color: 'var(--foreground)',
})

export const markdownContainer = style({
  padding: spacing.lg,
  overflow: 'auto',
  height: '100%',
})

// Side-by-side split view
export const splitContainer = style({
  display: 'flex',
  height: '100%',
})

export const splitPane = style({
  flex: 1,
  overflow: 'auto',
  minWidth: 0,
})

export const splitDivider = style({
  width: '1px',
  backgroundColor: 'var(--border)',
  flexShrink: 0,
})

export const splitPaneMarkdown = style({
  padding: spacing.lg,
})

export const splitPaneSource = style({})

// Remove border/margin from code view when used inside split pane
// and move padding here so the scrollbar stays at the pane edge.
globalStyle(`${splitPane} ${codeViewContainer}`, {
  border: 'none',
  borderRadius: 0,
  marginTop: 0,
  overflow: 'visible',
  paddingBlock: spacing.sm,
})

// Floating toolbar for image zoom controls (top-left)
export const imageToolbar = style({
  'position': 'absolute',
  'top': spacing.sm,
  'left': spacing.md,
  'zIndex': 10,
  'display': 'flex',
  'alignItems': 'center',
  'borderRadius': 'var(--radius-small)',
  'border': '1px solid var(--border)',
  'backgroundColor': 'var(--card)',
  'opacity': 0.8,
  'transition': 'opacity 0.15s',
  ':hover': {
    opacity: 1,
  },
})

export const imageToolbarButton = style({
  all: 'unset',
  boxSizing: 'border-box',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: '28px',
  height: '28px',
  cursor: 'pointer',
  color: 'var(--muted-foreground)',
  transition: 'color 0.1s, background-color 0.1s',
  selectors: {
    '&:first-child': {
      borderRadius: 'var(--radius-small) 0 0 var(--radius-small)',
    },
    '&:last-child': {
      borderRadius: '0 var(--radius-small) var(--radius-small) 0',
    },
    '&:hover': {
      color: 'var(--foreground)',
    },
  },
})

export const imageToolbarLabel = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: '48px',
  height: '28px',
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  userSelect: 'none',
  whiteSpace: 'nowrap',
})

export const imageToolbarTextButton = style({
  all: 'unset',
  boxSizing: 'border-box',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  height: '28px',
  paddingInline: spacing.xs,
  cursor: 'pointer',
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-8)',
  transition: 'color 0.1s, background-color 0.1s',
  borderRadius: '0 var(--radius-small) var(--radius-small) 0',
  selectors: {
    '&:hover': {
      color: 'var(--foreground)',
    },
  },
})

// Outer wrapper for image render area (toolbar + scroll container)
export const imageRenderContainer = style({
  position: 'relative',
  height: '100%',
})

// Scrollable wrapper for zoomed images
export const imageScrollContainer = style({
  overflow: 'auto',
  height: '100%',
})

// Centering wrapper for image (fit and zoomed)
export const imageZoomWrapper = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  minWidth: '100%',
  minHeight: '100%',
  width: 'max-content',
  padding: spacing.lg,
})

// Checkerboard transparency pattern + border for images
export const imageCheckerboard = style({
  backgroundImage: 'repeating-conic-gradient(rgba(128,128,128,0.15) 0% 25%, transparent 0% 50%)',
  backgroundSize: '16px 16px',
  border: '1px solid var(--border)',
})
