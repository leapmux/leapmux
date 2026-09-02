import { style } from '@vanilla-extract/css'

// root sits in a tool card's header row, right after the title.
//
// Deliberately NO `marginLeft: auto`: the header actions already carry one (see
// the globalStyle on toolHeaderActions in ../toolStyles.css.ts), and the title
// does not grow, so a second auto margin would split the free space between the
// two and leave the badge floating in the middle of the row. Sitting beside the
// title reads as an annotation on the tool, and the actions stay right-aligned.
//
// It must never change the header's height: the premeasure pass measures a row
// once, and a badge that appeared later and wrapped the title onto a second line
// would leave every measured height stale. flexShrink 0 + nowrap keep the badge
// on one line and let the (already clipped) title give up the space instead.
export const root = style({
  flexShrink: 0,
  whiteSpace: 'nowrap',
  paddingLeft: 'var(--space-1)',
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  // Tabular digits so the elapsed time does not shift the text beside it when
  // it steps from 9s-wide to 10s-wide glyphs.
  fontVariantNumeric: 'tabular-nums',
  userSelect: 'none',
})

// retry marks a tool whose subagent is retrying an API call. Warning-coloured
// because it reports a failure the agent is working around, not normal progress.
export const retry = style({
  color: 'var(--warning)',
})
