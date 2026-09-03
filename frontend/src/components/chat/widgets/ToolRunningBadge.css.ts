import { style } from '@vanilla-extract/css'

// root sits in a tool card's header row, right after the title.
//
// Deliberately NO `marginLeft: auto`: the header actions already carry one (see
// the globalStyle on toolHeaderActions in ../toolStyles.css.ts), and the title
// does not grow, so a second auto margin would split the free space between the
// two and leave the badge floating in the middle of the row. Sitting beside the
// title reads as an annotation on the tool, and the actions stay right-aligned.
//
// flexShrink 0 + nowrap keep the BADGE itself on one line, so its own text never
// wraps. They do NOT keep the header at one line. A title that wraps -- the
// `toolInputPath` and `toolInputSummary` forms, which set no nowrap -- gives up
// horizontal space to the badge and can take a second line, which makes the
// header taller. A clipped title absorbs the space instead and does not.
//
// That is why the premeasure pass must render this badge, and does: ChatView
// builds one host for both the hidden measuring copy and the real row, so the
// measured height already includes the badge. Do not suppress the badge in
// premeasure mode -- every wrapping-title row would then measure one line short.
//
// The header puts this smaller text on the title's baseline (`align-items:
// baseline` on toolUseHeader). Set no `line-height` here: this text is 12px
// beside a 14px title, so a taller line box hangs below the shared baseline and
// makes every tool header 1px taller. The 1.6 that the badge inherits gives a
// shorter box than the title's, which is what the baseline alignment expects.
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

// retry marks a tool whose subagent retries an API call. Warning-coloured
// because it reports a failure that the agent works around, not normal progress.
export const retry = style({
  color: 'var(--warning)',
})
