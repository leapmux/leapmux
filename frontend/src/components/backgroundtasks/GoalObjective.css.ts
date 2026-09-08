import { globalStyle, style } from '@vanilla-extract/css'
import { markdownContent } from '~/components/chat/markdownEditor/markdownContent.css'
import { fadeMaskBottom } from '~/styles/fadeMask'

/**
 * How many lines of the objective the card shows before it clamps.
 *
 * Four, because that is what a one-sentence goal and a short list both fit in,
 * and the sidebar still has room for the status and the counters below.
 */
const CLAMPED_LINES = 4

/** The line box the clamp counts in. Also what the fade's height is measured in. */
const LINE_HEIGHT = 1.5

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  minWidth: 0,
})

export const body = style({
  fontSize: 'var(--text-7)',
  color: 'var(--foreground)',
  lineHeight: LINE_HEIGHT,
  // The objective is model-written or user-written prose, so it may hold a URL
  // or an identifier longer than the sidebar is wide.
  overflowWrap: 'anywhere',
})

/**
 * The collapsed box: at most four lines, with the rest clipped.
 *
 * A visual clamp, never a slice of the markdown SOURCE. A slice cuts a block in
 * half -- a fenced code block loses its closing fence, a list loses its last
 * item -- which is the rule `~/components/chat/results/CollapsibleContent.tsx`
 * records for every markdown body in the app.
 */
export const bodyClamped = style({
  maxHeight: `calc(${LINE_HEIGHT}em * ${CLAMPED_LINES})`,
  overflow: 'hidden',
})

/**
 * The fade over the last line of a clamped box.
 *
 * Apart from the clamp, and applied only once the box actually hides something.
 * A goal that is exactly four lines long carries the clamp and hides nothing,
 * and a fade there would dim a line that is completely visible.
 */
export const bodyFaded = style(fadeMaskBottom(LINE_HEIGHT))

/** The disclosure sits at the right end, under the fade it removes. */
export const toggleRow = style({
  display: 'flex',
  justifyContent: 'flex-end',
})

/**
 * The objective inside the hover tooltip.
 *
 * A height cap and nothing else. The tooltip sets `pointer-events: none`, so it
 * cannot scroll, and a very long objective therefore stops at the cap -- the
 * `Show more` disclosure in the card is the complete read, and this is the
 * peek. No fade here: the tooltip is as tall as its content until it reaches
 * the cap, so a fade would dim the last line of every objective that fits.
 */
export const tooltipBody = style({
  maxHeight: '50vh',
  overflow: 'hidden',
})

/**
 * Strip the markdown renderer's outer vertical margins, so the objective sits
 * flush against the card's padding.
 *
 * Anchored on `MarkdownText`'s own container class, NOT on a count of `> *`
 * hops. The two boxes nest differently -- `body` holds the measured wrapper and
 * `tooltipBody` does not -- so a depth-counted selector needed one spelling
 * each, and both stopped matching, silently, the moment either box gained or
 * lost a wrapper. The class is where the margins actually come from.
 */
function stripOuterMargins(scope: string): void {
  globalStyle(`${scope} .${markdownContent} > :first-child`, { marginTop: 0 })
  globalStyle(`${scope} .${markdownContent} > :last-child`, { marginBottom: 0 })
}

stripOuterMargins(body)
stripOuterMargins(tooltipBody)

// A heading in a 300px sidebar column renders at the transcript's size and
// swamps the card. The weight still marks it as a heading; only the size is
// capped. `toolResultCollapsed` in `~/components/chat/toolStyles.css.ts` caps
// the same way for the same reason.
const HEADINGS = ['h1', 'h2', 'h3', 'h4', 'h5', 'h6']
globalStyle(
  HEADINGS.flatMap(h => [`${body} ${h}`, `${tooltipBody} ${h}`]).join(', '),
  { fontSize: 'inherit' },
)
