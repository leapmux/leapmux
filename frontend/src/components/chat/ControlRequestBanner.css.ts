import { globalStyle, style } from '@vanilla-extract/css'
import { hideNativeScrollbar } from '~/styles/scrollbar'

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

// Control request content occupies the MarkdownEditor banner slot.
export const controlBanner = style({
  position: 'relative',
  padding: 'var(--space-2) var(--space-3)',
  fontSize: 'var(--text-7)',
  backgroundColor: 'var(--lm-warning-subtle)',
  borderBottom: '1px solid var(--border)',
  flexShrink: 0,
  maxHeight: '200px',
  overflowY: 'auto',
  selectors: {
    '&:has([data-question-preview], [data-elicitation-form], [data-control-json])': {
      maxHeight: 'min(50dvh, 32rem)',
    },
  },
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

/**
 * The footer spans the composer below the editor.
 * Secondary actions stay left, pagination stays central, and decisions stay right.
 *
 * The composer draws the separator. Another border here would draw two lines.
 * Vertical padding would raise Pause above the adjacent [+] button.
 * The footer slot's bottom offset supplies the space below this row.
 *
 * The row must shrink to fit the composer. Otherwise, the editor clips controls
 * that overflow to the left, and the user cannot reach them.
 *
 * Automatic tracks reserve no space for absent secondary actions or pagination.
 * Equal outer tracks would reserve half the row for an absent left zone.
 */
export const controlFooter = style({
  display: 'grid',
  gridTemplateColumns: 'auto auto 1fr',
  alignItems: 'center',
  gap: 'var(--space-1)',
  padding: '0 var(--space-2)',
  backgroundColor: 'var(--background)',
  flexGrow: 1,
  minWidth: 0,
})

/**
 * Equal outer tracks centre pagination when the caller supplies it.
 * The base layout avoids those tracks when the centre is empty.
 */
export const controlFooterCentred = style({
  gridTemplateColumns: '1fr auto 1fr',
})

// Explicit columns keep pagination central when the caller omits secondary actions.
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

// The complete decision strip scrolls so options cannot shrink to zero width.
// Safe alignment keeps overflowing controls reachable from the left edge.
// A zero minimum width lets the grid restrict this strip to the composer.
export const controlFooterRight = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  justifyContent: 'safe flex-end',
  gridColumn: 3,
  minWidth: 0,
  overflowX: 'auto',
  scrollbarWidth: 'none',
  WebkitOverflowScrolling: 'touch',
  touchAction: 'pan-x',
})

// Hide the native scrollbar so it does not consume the compact row's height.
hideNativeScrollbar(controlFooterRight)

// Switches, request choices, and session permissions precede the decisions.
// Each control keeps its natural width within the shared scroll strip.
export const controlRequestSwitches = style({
  display: 'flex',
  flexDirection: 'row',
  alignItems: 'center',
  gap: 'var(--space-1)',
  marginRight: 'var(--space-1)',
  flex: '1 0 auto',
})

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
