import { style } from '@vanilla-extract/css'
import { ACTION_SIZE } from './AgentInputQueue.css'

/**
 * The paused-queue notice, directly above the queue and the composer.
 *
 * No background, no border and no horizontal padding: it is a line of text in
 * the composer's own column, not a card sitting on top of it. Vertical padding
 * is zero too, because `inputArea` owns every gap in that column -- see the
 * `inputArea` comment in `./ChatView.css.ts`.
 */
export const banner = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  padding: 0,
  fontSize: '0.72rem',
  color: 'var(--muted-foreground)',
})

export const icon = style({
  flexShrink: 0,
  color: 'var(--warning)',
})

/** Takes the free width, so Resume stays at the end of the row. */
export const text = style({
  flex: 1,
  minWidth: 0,
})

export const resume = style({
  height: ACTION_SIZE,
  padding: '0 var(--space-2)',
  fontSize: '0.72rem',
  flexShrink: 0,
})
