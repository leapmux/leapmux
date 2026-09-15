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

globalStyle(`${optionList} fieldset`, { minWidth: 0 })

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
  minWidth: 0,
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
  flexWrap: 'wrap',
  maxWidth: '100%',
  alignItems: 'center',
  gap: '2px',
  justifyContent: 'flex-end',
})

export const paginationItem = style({
  'all': 'unset',
  'flexShrink': 0,
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

// Control request content occupies the MarkdownEditor banner slot.
export const controlBanner = style({
  position: 'relative',
  padding: 'var(--space-2) var(--space-3)',
  fontSize: 'var(--text-7)',
  backgroundColor: 'var(--lm-warning-subtle)',
  borderBottom: '1px solid var(--border)',
  flexShrink: 0,
  maxHeight: 'min(50dvh, 32rem)',
  overflowY: 'auto',
})

export const controlBannerActions = style({
  position: 'absolute',
  top: 'var(--space-1)',
  right: 'var(--space-1)',
})

/**
 * The hover-revealed half of the banner's actions.
 *
 * The opacity sits on each BUTTON rather than on the row, because a row at opacity 0
 * hides every child whatever the child's own opacity says -- and one of them must stay
 * visible. Copying the raw frame is a power-user affordance and keeps the hover; the
 * stop does not, because a reader looking for a way out of a turn that waits for
 * them cannot be asked to discover it by hovering.
 */
export const controlBannerHoverAction = style({
  opacity: 0,
  transition: 'opacity var(--transition)',
})

globalStyle(`${controlBanner}:hover .${controlBannerHoverAction}`, {
  opacity: 1,
})

export const controlBannerTitle = style({
  fontSize: 'var(--text-7)',
  fontWeight: 'var(--font-bold)',
  color: 'var(--foreground)',
  marginBottom: 'var(--space-1)',
})

// All action groups wrap and align to the right edge of the composer footer.
// The editor measures the resulting height and reserves space above the footer.
//
// The side padding is the row's own gutter. The footer SLOT insets the whole
// row, so without this the first and the last control of a decision row sat
// var(--space-2) closer to the composer edge than every other control there.
export const controlFooter = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  justifyContent: 'flex-end',
  gap: 'var(--space-1)',
  padding: '0 var(--space-2)',
  backgroundColor: 'var(--background)',
  flexGrow: 1,
  minWidth: 0,
})

const actionGroup = {
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  justifyContent: 'flex-end',
  gap: 'var(--space-1)',
  minWidth: 0,
  maxWidth: '100%',
} as const

export const controlFooterSecondary = style(actionGroup)
export const controlFooterNavigation = style(actionGroup)
export const controlFooterDecisions = style(actionGroup)

// Scope choices keep their natural width. Wrapped rows share the decisions' right edge.
export const controlRequestSwitches = style([actionGroup, {
  flex: '1 0 auto',
}])

// Pill groups keep their natural width instead of compressing their options.
export const controlRequestPill = style({
  display: 'flex',
  flexShrink: 0,
})

export const bannerReason = style({
  fontSize: 'var(--text-7)',
  color: 'var(--foreground)',
  marginBottom: 'var(--space-2)',
})

const captionText = {
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
}

export const bannerDetail = style(captionText)

export const bannerHint = style({
  ...captionText,
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
})
