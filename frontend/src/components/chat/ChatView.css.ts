import { style } from '@vanilla-extract/css'
import { resizeHandleSelectors } from '~/styles/resizeHandle'

export const editorResizeHandle = style({
  height: '4px',
  flexShrink: 0,
  cursor: 'row-resize',
  position: 'relative',
  userSelect: 'none',
  margin: '-2px 0',
  zIndex: 5,
  selectors: resizeHandleSelectors('vertical'),
})

export const editorResizeHandleActive = style({
  selectors: {
    '&::before': {
      background: 'var(--primary) !important',
      height: '1px !important',
    },
  },
})

export const container = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  overflow: 'hidden',
})

export const messageListWrapper = style({
  position: 'relative',
  flex: 1,
  overflow: 'hidden',
  display: 'flex',
  flexDirection: 'column',
})

export const messageListSpacer = style({
  flex: 1,
})

export const messageListContent = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-5)',
})

export const messageList = style({
  flex: 1,
  overflowX: 'hidden',
  overflowY: 'auto',
  overflowAnchor: 'none',
  padding: 'var(--space-4) var(--space-4) var(--space-4) var(--space-6)',
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-3)',
})

export const loadingOlderIndicator = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  gap: 'var(--space-2)',
  padding: 'var(--space-3)',
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-7)',
})

export const inputArea = style({
  padding: 'var(--space-1) var(--space-3) var(--space-3)',
  flexShrink: 0,
})

export const footerBar = style({
  display: 'flex',
  justifyContent: 'space-between',
  alignItems: 'center',
  padding: 'var(--space-1) var(--space-1) var(--space-1) var(--space-3)',
  backgroundColor: 'var(--background)',
  flexShrink: 0,
})

export const footerBarLeft = style({
  display: 'flex',
  alignItems: 'center',
})

export const scrollToBottomButton = style({
  'position': 'absolute',
  'bottom': 'var(--space-3)',
  'left': '50%',
  'transform': 'translateX(-50%)',
  'zIndex': 10,
  'width': '36px',
  'height': '36px',
  'backgroundColor': 'var(--background)',
  'opacity': 0.8,
  ':hover': {
    opacity: 1,
  },
})

export const emptyChat = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  flex: 1,
  color: 'var(--faint-foreground)',
})

export const settingsTrigger = style({
  all: 'unset',
  boxSizing: 'border-box',
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  padding: '2px var(--space-1)',
  marginBottom: '3px',
  marginLeft: '-3px',
  fontSize: 'var(--text-8)',
  color: 'var(--faint-foreground)',
  cursor: 'pointer',
  borderRadius: 'var(--radius-small)',
  whiteSpace: 'nowrap',
  userSelect: 'none',
  selectors: {
    '&:hover': { color: 'var(--foreground)', backgroundColor: 'var(--card)' },
    '&[data-disabled]': { opacity: 0.5, cursor: 'default' },
  },
})

export const settingsMenu = style({
  backgroundColor: 'var(--background)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  padding: 'var(--space-1) var(--space-4) 0 var(--space-4)',
  zIndex: 300,
  minWidth: '180px',
  maxHeight: 'calc(100vh - var(--space-6) * 2)',
  overflowY: 'auto',
  boxShadow: 'var(--shadow-large)',
})

export const settingsMenuWide = style({
  'minWidth': '460px',
  '@media': {
    '(max-width: 639px)': {
      minWidth: 'auto',
    },
  },
})

export const settingsPanelColumns = style({
  'display': 'flex',
  'alignItems': 'flex-start',
  'gap': 'var(--space-6)',
  '@media': {
    '(max-width: 639px)': {
      flexDirection: 'column',
      gap: 'var(--space-1)',
    },
  },
})

export const settingsPanelColumn = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  flex: 1,
  minWidth: 0,
})

export const settingsPanelColumnPrimary = style({
  flex: 1.2,
})

export const settingsPanelColumnSlightlyWider = style({
  flex: 1.05,
})

export const settingsFieldset = style({
  paddingTop: 'var(--space-3)',
  minWidth: 0,
})

export const settingsFieldsetFirst = style({
  marginBlockStart: 'var(--space-2)',
})

export const settingsGroupLabel = style({
  padding: '0 var(--space-2)',
  fontSize: 'var(--text-8)',
  fontWeight: 600,
  color: 'var(--muted-foreground)',
  textTransform: 'uppercase',
  letterSpacing: '0.05em',
  whiteSpace: 'nowrap',
})

export const settingsRadioItem = style({
  'all': 'unset',
  'boxSizing': 'border-box',
  'display': 'flex',
  'alignItems': 'center',
  'gap': 'var(--space-2)',
  'padding': '3px var(--space-2)',
  'fontSize': 'var(--text-8)',
  'color': 'var(--foreground)',
  'cursor': 'pointer',
  'userSelect': 'none',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
})

// Searchable select: current value display
export const searchableSelectCurrent = style({
  padding: '3px var(--space-2)',
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})

// Searchable select: scrollable list
export const searchableSelectListbox = style({
  // Height fits 5 items (compact — selected value + filter input take extra space).
  // Each item is 1lh tall + 6px vertical padding → calc(1lh + 6px) per item.
  // Font-size must match items so 1lh resolves to the correct item line-height.
  fontSize: 'var(--text-8)',
  minHeight: 'calc((1lh + 6px) * 7)',
  maxHeight: 'calc((1lh + 6px) * 7)',
  overflowY: 'auto',
})

// Searchable select: item
export const searchableSelectItem = style({
  'display': 'flex',
  'alignItems': 'center',
  'justifyContent': 'space-between',
  'gap': 'var(--space-2)',
  'padding': '3px var(--space-2)',
  'fontSize': 'var(--text-8)',
  'color': 'var(--foreground)',
  'cursor': 'pointer',
  'userSelect': 'none',
  'whiteSpace': 'nowrap',
  'borderRadius': 'var(--radius-small)',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
})

// Searchable select: highlighted item (keyboard navigation)
export const searchableSelectItemHighlighted = style({
  backgroundColor: 'var(--muted)',
})

// Searchable select: currently selected item
export const searchableSelectItemSelected = style({
  fontWeight: 600,
})

// Searchable select: secondary text (right-aligned, muted)
export const searchableSelectItemSecondary = style({
  fontFamily: 'var(--font-mono)',
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  marginLeft: 'auto',
  flexShrink: 0,
})

// Searchable select: filter input container
export const searchableSelectControl = style({
  padding: '3px var(--space-2)',
  borderTop: '1px solid var(--border)',
  marginTop: 'var(--space-1)',
})

// Searchable select: filter input
export const searchableSelectInput = style({
  'all': 'unset',
  'boxSizing': 'border-box',
  'width': '100%',
  'fontSize': 'var(--text-8)',
  'color': 'var(--foreground)',
  '::placeholder': {
    color: 'var(--faint-foreground)',
  },
})

export const footerBarRight = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
})

export const infoTrigger = style({
  all: 'unset',
  boxSizing: 'border-box',
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  gap: 'var(--space-1)',
  padding: '2px',
  fontSize: 'var(--text-8)',
  color: 'var(--faint-foreground)',
  cursor: 'pointer',
  borderRadius: 'var(--radius-small)',
  vars: {
    '--context-grid-inactive': 'var(--border)',
    '--context-grid-warning': 'var(--warning)',
  },
  selectors: {
    '&:hover': { color: 'var(--foreground)', backgroundColor: 'var(--card)', vars: { '--context-grid-inactive': 'var(--border)', '--context-grid-warning': 'var(--warning)' } } as Record<string, unknown>,
  },
})

export const infoRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
})

export const infoLabel = style({
  fontSize: 'var(--text-8)',
  fontWeight: 600,
  color: 'var(--muted-foreground)',
  whiteSpace: 'nowrap',
})

export const infoValue = style({
  fontSize: 'var(--text-8)',
  color: 'var(--foreground)',
  fontFamily: 'monospace',
  wordBreak: 'break-all',
})

export const infoValueText = style({
  fontSize: 'var(--text-8)',
  color: 'var(--foreground)',
  wordBreak: 'break-all',
})

export const infoCopyButton = style({
  'all': 'unset',
  'boxSizing': 'border-box',
  'display': 'inline-flex',
  'alignItems': 'center',
  'justifyContent': 'center',
  'padding': '2px',
  'cursor': 'pointer',
  'borderRadius': 'var(--radius-small)',
  'color': 'var(--faint-foreground)',
  'flexShrink': 0,
  ':hover': {
    color: 'var(--foreground)',
    backgroundColor: 'var(--card)',
  },
})

export const infoRows = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
})

export const infoSeparator = style({
  height: '1px',
  backgroundColor: 'var(--border)',
  margin: 'var(--space-1) 0',
})

export const infoContextUsage = style({
  fontSize: 'var(--text-8)',
  color: 'var(--foreground)',
  maxHeight: '300px',
  overflowY: 'auto',
  lineHeight: '1.4',
})

export const rateLimitCountdown = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  fontFamily: 'var(--font-mono)',
  whiteSpace: 'nowrap',
})

export const messageRow = style({
  display: 'flex',
})

// Tighten the gap for messages with span lines (they belong to a visual group).
export const messageRowWithSpanLines = style({
  marginTop: 'calc(-1 * (var(--space-5) - var(--space-2)))',
})

export const messageRowContent = style({
  flex: 1,
  minWidth: 0,
})

export const editorPanelWrapper = style({
  flexShrink: 0,
})
