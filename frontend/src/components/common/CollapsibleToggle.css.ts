import { style } from '@vanilla-extract/css'

/**
 * The disclosure toggle that opens and closes a clipped block: "Show more" and
 * "Show less".
 *
 * A dotted underline rather than a button box, because it sits at the end of the
 * content it opens and a solid control there reads as a second action on the
 * row.
 *
 * Beside its component rather than in `~/styles/shared.css.ts`. It was shared
 * while three surfaces each hand-wrote their own button around it -- a control
 * request's reason text, a control request's option list, and the session goal's
 * objective. `./CollapsibleToggle.tsx` now owns all three, so the paint and the
 * behavior have one home and one owner.
 */
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
