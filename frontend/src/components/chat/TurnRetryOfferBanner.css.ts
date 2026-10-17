import { style } from '@vanilla-extract/css'
import { compactActionLabelled, compactTextSize } from '~/styles/tokens'

/**
 * The turn-retry offer, directly above the queue and the composer.
 *
 * No background, no border and no horizontal padding: it is a line of text in
 * the composer's own column, not a card sitting on top of it. Vertical padding
 * is zero too, because `inputArea` owns every gap in that column -- see the
 * `inputArea` comment in `~/components/chat/ChatView.css.ts`.
 */
export const banner = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  padding: 0,
  fontSize: compactTextSize,
  color: 'var(--muted-foreground)',
})

/** Takes the free width, so the actions stay at the end of the row. */
export const text = style({
  flex: 1,
  minWidth: 0,
})

/**
 * The two buttons and the "Learn more" link sit in the same column as the row
 * actions of the queue, so all three take the shared compact-action geometry
 * from `~/styles/tokens.ts`. One source, so the heights cannot drift.
 */
export const action = style({ ...compactActionLabelled })
