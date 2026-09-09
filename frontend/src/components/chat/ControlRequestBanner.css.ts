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
/**
 * The action row of a control request.
 *
 * NO vertical padding. The row is the only child of the composer's footer slot
 * that sets its own height, and the slot centres what it holds -- so padding
 * here made the slot taller than the Pause button inside it, and that button
 * then sat 4px above the `[+]` anchored to the same bottom line. The slot's own
 * `bottom` offset is what separates the row from the box edge.
 *
 * It SHRINKS. `flex-shrink: 0` pinned the row at its max-content width, so a
 * row wider than the composer overflowed to the LEFT -- `justify-content:
 * flex-end` pushes the overflow that way -- and the editor's `overflow: hidden`
 * clipped it. On a phone the whole allow-choice group sat off the left edge,
 * invisible and impossible to tap. `min-width: 0` alone could not help, because
 * a shrink factor of zero refuses to shrink at all.
 *
 * The tracks are `auto auto 1fr`, so a zone the caller omits reserves nothing.
 * `1fr auto 1fr` gave the empty left zone an equal share of the row, which left
 * a decision row half the width it had.
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
 * The track shape that CENTRES the middle zone, for a row that fills it.
 *
 * Equal outer tracks are what put the centre in the middle, and they are also
 * what wastes a row that has no middle -- so the base rule above omits them and
 * this restores them exactly where the centring is the point.
 */
export const controlFooterCentred = style({
  gridTemplateColumns: '1fr auto 1fr',
})

// All three zones pin their own column. Auto-placement would put a zone's column
// dependent on which OTHER zones the caller passed: with `secondary` omitted,
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

// `minWidth: 0`, so the `1fr` track can actually constrain this zone. A grid
// item's automatic minimum size is its min-content width, and the decision
// buttons never wrap, so without this the track grew to fit them and the row
// overflowed the composer instead of compressing.
export const controlFooterRight = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  justifyContent: 'flex-end',
  gridColumn: 3,
  minWidth: 0,
})

/**
 * The leading options cluster of a decision row: the request's switches, then
 * the allow-choice pill group, then the permission pill group, on ONE line ahead
 * of the decision buttons. A pill group is button-high, so nothing here needs
 * the second line the switch COLUMN used to occupy.
 *
 * It SCROLLS sideways, and it is the only part of the row that gives way. The
 * decision buttons keep their size, because Allow and Reject must stay readable
 * and reachable at every width; this cluster takes what is left and the user
 * swipes it. The alternative -- letting the pills compress -- squeezed a group
 * to two pixels on a phone, which states nothing and answers nothing.
 *
 * The scrollbar is HIDDEN, as the tab strip hides its own (`tabList` in
 * `~/components/shell/TabBar.css.ts`). A scrollbar inside a 28px row would eat
 * most of it, and this is a swipe surface rather than a scroll region.
 */
export const controlRequestSwitches = style({
  display: 'flex',
  flexDirection: 'row',
  alignItems: 'center',
  gap: 'var(--space-1)',
  marginRight: 'var(--space-1)',
  minWidth: 0,
  flex: '1 1 auto',
  overflowX: 'auto',
  scrollbarWidth: 'none',
  WebkitOverflowScrolling: 'touch',
  touchAction: 'pan-x',
})

hideNativeScrollbar(controlRequestSwitches)

// A pill group keeps its natural width and the cluster around it scrolls, so
// `flexShrink: 0`. It used to shrink, and the group's own `max-width: 100%` then
// resolved against a box the row had already squeezed to nothing.
export const controlRequestPill = style({
  display: 'flex',
  flexShrink: 0,
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
