import { style } from '@vanilla-extract/css'
import { resizeHandleSelectors } from '~/styles/tokens'

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
  gap: 'var(--space-3)',
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
  padding: 'var(--space-1) var(--space-2)',
  backgroundColor: 'var(--background)',
  flexShrink: 0,
})

export const footerBarLeft = style({
  display: 'flex',
  alignItems: 'center',
})

export const sendButton = style({
  'all': 'unset',
  'boxSizing': 'border-box',
  'display': 'flex',
  'alignItems': 'center',
  'justifyContent': 'center',
  'gap': 'var(--space-1)',
  'padding': 'var(--space-1) var(--space-2)',
  'fontSize': 'var(--text-7)',
  'fontWeight': 400,
  'borderRadius': 'var(--radius-small)',
  'backgroundColor': 'var(--primary)',
  'color': '#fff',
  'cursor': 'pointer',
  ':hover': {
    backgroundColor: 'var(--primary)',
  },
})

export const sendButtonDisabled = style({
  'backgroundColor': 'var(--card)',
  'color': 'var(--faint-foreground)',
  'cursor': 'default',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
})

export const emptyChat = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  flex: 1,
  color: 'var(--faint-foreground)',
})

export const interruptButton = style({
  'border': 'none',
  'background': 'none',
  'font': 'inherit',
  'boxSizing': 'border-box',
  'display': 'flex',
  'alignItems': 'center',
  'gap': 'var(--space-1)',
  'padding': 'var(--space-1) var(--space-2)',
  'fontSize': 'var(--text-7)',
  'fontWeight': 400,
  'borderRadius': 'var(--radius-small)',
  'color': 'var(--muted-foreground)',
  'cursor': 'pointer',
  'vars': {
    '--color-shift-from': 'var(--card)',
    '--color-shift-to': 'var(--card)',
  },
  ':hover': {
    backgroundColor: 'var(--card)',
    color: 'var(--foreground)',
  },
})

export const settingsTrigger = style({
  all: 'unset',
  boxSizing: 'border-box',
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  padding: '2px var(--space-1)',
  marginBottom: '3px',
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
  boxShadow: 'var(--shadow-large)',
})

export const settingsGroupLabel = style({
  padding: 'var(--space-1) var(--space-2)',
  fontSize: 'var(--text-8)',
  fontWeight: 600,
  color: 'var(--muted-foreground)',
  textTransform: 'uppercase',
  letterSpacing: '0.05em',
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

export const editorPanelWrapper = style({
  flexShrink: 0,
})
