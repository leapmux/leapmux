import { globalStyle, style } from '@vanilla-extract/css'

export const questionGroup = style({
  marginBottom: 'var(--space-3)',
})

export const questionLabel = style({
  fontSize: 'var(--text-7)',
  fontWeight: 'var(--font-normal)',
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

export const optionContent = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '1px',
})

export const optionLabel = style({
  fontWeight: 'var(--font-normal)',
})

export const optionDescription = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})

export const bannerCodeBlock = style({
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
  'fontWeight': 'var(--font-normal)',
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
  fontWeight: 'var(--font-bold)',
})

export const questionPageHeader = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  marginBottom: 'var(--space-1)',
})

// Control request content in MarkdownEditor banner slot
export const controlBanner = style({
  position: 'relative',
  padding: 'var(--space-2) var(--space-3)',
  fontSize: 'var(--text-7)',
  backgroundColor: 'var(--lm-warning-subtle)',
  borderBottom: '1px solid var(--border)',
  flexShrink: 0,
  maxHeight: '200px',
  overflowY: 'auto',
})

export const controlBannerActions = style({
  position: 'absolute',
  top: 'var(--space-1)',
  right: 'var(--space-1)',
  opacity: 0,
  transition: 'opacity var(--transition)',
})

globalStyle(`${controlBanner}:hover .${controlBannerActions}`, {
  opacity: 1,
})

export const controlBannerTitle = style({
  fontSize: 'var(--text-7)',
  fontWeight: 'var(--font-bold)',
  color: 'var(--foreground)',
  marginBottom: 'var(--space-1)',
})

// Control-request action footer: a full-width row below the editor inside the
// composer box, using a three-zone [secondary | pagination | primary] grid.
// Secondary actions stay at the left end. Request decisions stay at the right
// end. Pagination dots stay between them.
//
// NO top border. The line above this row belongs to the composer box, which
// draws it as `editorSeparator` for every expanded action row -- the compact
// Interrupt/Send cluster included. A border here as well painted a SECOND line
// a couple of pixels from the first, because a control request forces the
// expanded layout and therefore always renders both.
export const controlFooter = style({
  display: 'grid',
  gridTemplateColumns: '1fr auto 1fr',
  alignItems: 'center',
  gap: 'var(--space-1)',
  padding: 'var(--space-1) var(--space-2)',
  backgroundColor: 'var(--background)',
  flexShrink: 0,
  flexGrow: 1,
  minWidth: 0,
})

// All three zones pin their own column. Auto-placement would put a zone's column
// at the mercy of which OTHER zones the caller passed: with `secondary` omitted,
// an auto-placed centre becomes the first item and lands in column 1, so the
// pagination would sit inside the left half rather than in the middle.
export const controlFooterLeft = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  justifyContent: 'flex-start',
  gridColumn: 1,
})

export const controlFooterCentre = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  justifyContent: 'center',
  gridColumn: 2,
})

export const controlFooterRight = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  justifyContent: 'flex-end',
  gridColumn: 3,
})

export const controlRequestSwitches = style({
  display: 'flex',
  flexDirection: 'column',
  marginRight: 'var(--space-1)',
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

export const bannerReason = style({
  fontSize: 'var(--text-7)',
  color: 'var(--foreground)',
  marginBottom: 'var(--space-2)',
})

export const bannerHint = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
})
