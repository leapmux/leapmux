import { globalStyle, style, styleVariants } from '@vanilla-extract/css'

/**
 * The colour of each series, in order.
 *
 * LeapMux's own palette rather than the one the configuration carries. A colour the
 * model picked was chosen for a white canvas, and the transcript renders in both
 * themes -- a pale yellow that reads on paper disappears against the dark background.
 * The row must stay readable in both, so the spec's own `backgroundColor` and
 * `borderColor` are deliberately unread.
 *
 * Eight entries, which is more series than a readable chart carries. A ninth series
 * wraps to the first, and the legend is what tells the two apart.
 */
export const SERIES_COLORS = [
  'var(--primary)',
  'var(--warning)',
  'var(--success)',
  'var(--danger)',
  'oklch(from var(--primary) l c calc(h + 140))',
  'oklch(from var(--warning) l c calc(h + 140))',
  'oklch(from var(--success) l c calc(h + 140))',
  'oklch(from var(--danger) l c calc(h + 140))',
] as const

export const chartBody = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  marginTop: 'var(--space-2)',
})

export const chartHeading = style({
  fontWeight: 600,
})

export const chartDescription = style({
  color: 'var(--muted-foreground)',
  fontSize: '0.85em',
})

/**
 * The drawing itself.
 *
 * The SVG carries a `viewBox` and no intrinsic size, so its height follows its width
 * at a fixed ratio. The transcript MEASURES every row and caches that height, and a
 * chart that sized itself from its own content after mount would commit a height the
 * offset map then has to correct.
 */
export const chartCanvas = style({
  width: '100%',
  height: 'auto',
  maxHeight: '320px',
  overflow: 'visible',
})

export const chartAxisLine = style({
  stroke: 'var(--border)',
  strokeWidth: 1,
})

export const chartAxisLabel = style({
  fill: 'var(--muted-foreground)',
  fontSize: '9px',
})

export const chartSeriesLine = style({
  fill: 'none',
  strokeWidth: 2,
  strokeLinejoin: 'round',
  strokeLinecap: 'round',
})

export const chartLegend = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: 'var(--space-1) var(--space-3)',
  fontSize: '0.85em',
  color: 'var(--muted-foreground)',
})

export const chartLegendEntry = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
})

export const chartLegendSwatch = style({
  width: '10px',
  height: '10px',
  borderRadius: '2px',
  flexShrink: 0,
})

export const chartTable = style({
  fontSize: '0.85em',
  borderCollapse: 'collapse',
})

// The cells are DESCENDANTS of the table, so they cannot ride a `selectors` key: a
// key there must target the `&` itself, and vanilla-extract rejects `& td` by failing
// the whole module rather than the one rule.
globalStyle(`${chartTable} th, ${chartTable} td`, {
  padding: '2px var(--space-2)',
  textAlign: 'right',
  borderBottom: '1px solid var(--border)',
})

globalStyle(`${chartTable} th`, {
  color: 'var(--muted-foreground)',
  fontWeight: 500,
})

// The first column holds the category, so it reads from the left while every number
// beside it lines up on the right.
globalStyle(`${chartTable} th:first-child, ${chartTable} td:first-child`, {
  textAlign: 'left',
})

export const chartNotice = styleVariants({
  plain: [{ color: 'var(--muted-foreground)', fontSize: '0.85em' }],
  error: [{ color: 'var(--danger)', fontSize: '0.85em' }],
})
