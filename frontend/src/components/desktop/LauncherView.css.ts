import { keyframes, style } from '@vanilla-extract/css'

export const container = style({
  '--container-gap': '2rem',
  'display': 'flex',
  'flexDirection': 'column',
  'alignItems': 'center',
  'justifyContent': 'center',
  'minHeight': '100vh',
  'maxWidth': '720px',
  'margin': '0 auto',
  'padding': '2rem',
  'gap': 'var(--container-gap)',
  'transition': 'opacity 0.3s ease',
} as any)

export const header = style({
  textAlign: 'center',
})

export const title = style({
  fontSize: '1.75rem',
  fontWeight: 600,
  marginBottom: '0.25rem',
})

export const subtitle = style({
  color: 'var(--muted-foreground)',
  fontSize: '0.9rem',
})

export const modeCards = style({
  display: 'grid',
  gridTemplateColumns: '1fr 1fr',
  gap: '1rem',
  width: '100%',
  maxWidth: '640px',
})

export const modeCard = style({
  'background': 'var(--card)',
  'border': '2px solid var(--border)',
  'borderRadius': '0.75rem',
  'padding': '1.5rem',
  'cursor': 'pointer',
  'transition': 'border-color 0.15s, box-shadow 0.15s',
  'display': 'flex',
  'flexDirection': 'column',
  'gap': '0.75rem',
  'textAlign': 'left',
  'minWidth': 0,
  'whiteSpace': 'normal',
  'color': 'var(--card-foreground)',
  ':hover': {
    borderColor: 'var(--muted-foreground)',
  },
})

export const modeCardSelected = style({
  borderColor: 'var(--primary)',
  boxShadow: '0 0 0 1px var(--primary)',
})

export const modeHeader = style({
  display: 'flex',
  alignItems: 'center',
  gap: '0.5rem',
})

export const modeIcon = style({
  fontSize: '1.5rem',
  lineHeight: 1,
})

export const modeTitle = style({
  fontSize: '1rem',
  fontWeight: 600,
})

export const modeDescription = style({
  fontSize: '0.8rem',
  color: 'var(--muted-foreground)',
  lineHeight: 1.5,
})

// Collapsible section — animated height via grid 0fr → 1fr
export const collapsible = style({
  display: 'grid',
  gridTemplateRows: '0fr',
  transition: 'grid-template-rows 0.3s ease, margin-top 0.3s ease',
  width: '100%',
  maxWidth: '640px',
  marginTop: 'calc(-1 * var(--container-gap))',
})

export const collapsibleVisible = style({
  gridTemplateRows: '1fr',
  marginTop: 0,
})

export const collapsibleInner = style({
  overflow: 'hidden',
  minHeight: 0,
})

export const label = style({
  display: 'block',
  fontSize: '0.85rem',
  fontWeight: 500,
  marginBottom: '0.4rem',
})

export const input = style({
  'width': '100%',
  'padding': '0.6rem 0.75rem',
  'border': '1px solid var(--input)',
  'borderRadius': '0.5rem',
  'background': 'var(--background)',
  'color': 'var(--foreground)',
  'fontSize': '0.9rem',
  'outline': 'none',
  'transition': 'border-color 0.15s',
  ':focus': {
    borderColor: 'var(--ring)',
    boxShadow: '0 0 0 2px rgba(13, 148, 136, 0.2)',
  },
})

export const connectSection = style({
  width: '100%',
  maxWidth: '640px',
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  gap: '0.75rem',
})

export const connectBtn = style({
  width: '100%',
  padding: '0.7rem 1.5rem',
  border: 'none',
  borderRadius: '0.5rem',
  background: 'var(--primary)',
  color: 'var(--primary-foreground)',
  fontSize: '0.95rem',
  fontWeight: 500,
  cursor: 'pointer',
  transition: 'opacity 0.15s',
  selectors: {
    '&:hover:not(:disabled)': {
      opacity: 0.9,
    },
    '&:disabled': {
      opacity: 0.5,
      cursor: 'not-allowed',
    },
  },
})

const spin = keyframes({
  to: { transform: 'rotate(360deg)' },
})

export const spinner = style({
  width: '24px',
  height: '24px',
  border: '3px solid var(--border)',
  borderTopColor: 'var(--primary)',
  borderRadius: '50%',
  animation: `${spin} 0.6s linear infinite`,
})

export const errorText = style({
  color: 'var(--danger)',
  fontSize: '0.85rem',
  textAlign: 'center',
  wordBreak: 'break-word',
})

export const fdaCard = style({
  background: 'var(--card)',
  border: '2px solid var(--border)',
  borderRadius: '0.75rem',
  padding: '1.25rem',
  display: 'flex',
  flexDirection: 'column',
  gap: '0.75rem',
})

export const fdaHeader = style({
  display: 'flex',
  alignItems: 'center',
  gap: '0.5rem',
})

export const fdaIcon = style({
  fontSize: '1.1rem',
  lineHeight: 1,
})

export const fdaTitle = style({
  fontSize: '0.95rem',
  fontWeight: 600,
})

export const fdaBody = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: '0.75rem',
})

export const fdaText = style({
  flex: 1,
  fontSize: '0.8rem',
  color: 'var(--muted-foreground)',
  lineHeight: 1.5,
})

export const fdaButton = style({
  'flexShrink': 0,
  'padding': '0.5rem 1rem',
  'border': '1px solid var(--border)',
  'borderRadius': '0.5rem',
  'background': 'var(--secondary)',
  'color': 'var(--secondary-foreground)',
  'fontSize': '0.85rem',
  'fontWeight': 500,
  'cursor': 'pointer',
  'transition': 'opacity 0.15s',
  'whiteSpace': 'nowrap',
  ':hover': {
    opacity: 0.85,
  },
})

export const versionText = style({
  color: 'var(--muted-foreground)',
  fontSize: '0.75rem',
})
