import { globalStyle, style } from '@vanilla-extract/css'

export const banner = style({
  position: 'relative',
  padding: 'var(--space-4)',
  borderRadius: 'var(--radius-medium)',
  border: '1px solid var(--border)',
  borderLeft: '3px solid var(--warning)',
  backgroundColor: 'var(--lm-warning-subtle)',
  alignSelf: 'stretch',
})

export const bannerTitle = style({
  fontSize: 'var(--text-7)',
  fontWeight: 600,
  color: 'var(--foreground)',
  marginBottom: 'var(--space-2)',
})

export const bannerContent = style({
  fontSize: 'var(--text-7)',
  color: 'var(--foreground)',
  lineHeight: 1.6,
  marginBottom: 'var(--space-3)',
  maxHeight: '300px',
  overflowY: 'auto',
})

export const bannerActions = style({
  display: 'flex',
  gap: 'var(--space-2)',
  justifyContent: 'flex-end',
})

export const questionGroup = style({
  marginBottom: 'var(--space-3)',
})

export const questionLabel = style({
  fontSize: 'var(--text-7)',
  fontWeight: 400,
  color: 'var(--foreground)',
  marginBottom: 'var(--space-1)',
})

export const optionList = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
})

export const optionItem = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: 'var(--space-2)',
  padding: 'var(--space-1)',
  borderRadius: 'var(--radius-small)',
  cursor: 'pointer',
  fontSize: 'var(--text-7)',
  color: 'var(--foreground)',
  selectors: {
    '&:hover': {
      backgroundColor: 'var(--card)',
    },
  },
})

export const optionRadio = style({
  marginTop: '2px',
  flexShrink: 0,
  accentColor: 'var(--primary)',
})

export const optionContent = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '1px',
})

export const optionLabel = style({
  fontWeight: 400,
})

export const optionDescription = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})

export const toolSummary = style({
  fontSize: 'var(--text-7)',
  color: 'var(--muted-foreground)',
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-word',
})

export const paginationContainer = style({
  display: 'flex',
  alignItems: 'center',
  gap: '2px',
  justifyContent: 'center',
})

export const paginationItem = style({
  'all': 'unset',
  'boxSizing': 'border-box',
  'display': 'inline-flex',
  'alignItems': 'center',
  'justifyContent': 'center',
  'width': '22px',
  'height': '22px',
  'borderRadius': 'var(--radius-small)',
  'fontSize': 'var(--text-8)',
  'fontWeight': 400,
  'cursor': 'pointer',
  'border': `1px solid transparent`,
  'color': 'var(--muted-foreground)',
  'backgroundColor': 'transparent',
  'transition': 'color var(--transition-fast), border-color var(--transition-fast), background-color var(--transition-fast)',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transition: 'none',
    },
  },
})

export const paginationItemCurrent = style({
  'border': '1px solid var(--primary)',
  'color': 'var(--primary)',
  'backgroundColor': 'var(--secondary)',
  ':hover': {
    backgroundColor: 'var(--secondary)',
  },
})

export const paginationItemAnswered = style({
  color: 'var(--success)',
  fontWeight: 600,
})

export const questionPageHeader = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  marginBottom: 'var(--space-1)',
})

// Control request content in MarkdownEditor banner slot
export const controlBanner = style({
  padding: 'var(--space-2) var(--space-3)',
  fontSize: 'var(--text-7)',
  backgroundColor: 'var(--lm-warning-subtle)',
  borderBottom: '1px solid var(--border)',
  flexShrink: 0,
  maxHeight: '200px',
  overflowY: 'auto',
})

export const controlBannerTitle = style({
  fontSize: 'var(--text-7)',
  fontWeight: 600,
  color: 'var(--foreground)',
  marginBottom: 'var(--space-1)',
})

// Multi-question footer layout: [Stop] [YOLO]  [Pagination]  [Submit]
export const controlFooter = style({
  display: 'grid',
  gridTemplateColumns: '1fr auto 1fr',
  alignItems: 'center',
  gap: 'var(--space-1)',
  padding: 'var(--space-1) var(--space-2)',
  backgroundColor: 'var(--background)',
  flexShrink: 0,
  flex: 1,
  minWidth: 0,
})

export const controlFooterLeft = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  justifyContent: 'flex-start',
})

export const controlFooterRight = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  justifyContent: 'flex-end',
  gridColumn: 3,
})

export const collapsibleToggle = style({
  'all': 'unset',
  'display': 'inline',
  'fontSize': 'var(--text-8)',
  'color': 'var(--muted-foreground)',
  'cursor': 'pointer',
  'textDecoration': 'underline',
  'textDecorationStyle': 'dotted',
  'textUnderlineOffset': '2px',
  ':hover': {
    color: 'var(--foreground)',
  },
})

// Apply markdown content styling inside the banner
globalStyle(`${bannerContent} code`, {
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
})

globalStyle(`${bannerContent} pre`, {
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
})
