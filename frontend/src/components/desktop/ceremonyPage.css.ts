import { style } from '@vanilla-extract/css'

/**
 * The page chrome the desktop browser-ceremony views share: a centered card,
 * its heading and description, the error line, the text-link button, and the
 * two action rows. A page keeps only its own controls.
 */
export const container = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  justifyContent: 'center',
  minHeight: '100vh',
  gap: 'var(--space-4)',
  padding: 'var(--space-6)',
  textAlign: 'center',
})

export const title = style({
  fontSize: '1.5rem',
  fontWeight: 600,
  margin: 0,
})

export const description = style({
  color: 'var(--color-text-secondary)',
  margin: 0,
})

export const error = style({
  color: 'var(--color-danger)',
  margin: 0,
})

/** A `<button>` that reads as a text link. */
export const unstyledLink = style({
  background: 'none',
  border: 'none',
  color: 'var(--color-text-secondary)',
  cursor: 'pointer',
  textDecoration: 'underline',
})

/** The action row of the waiting state, with its tight gap. */
export const waitingActions = style({
  display: 'flex',
  gap: 'var(--space-2)',
})

/** The action row of the hand-off page, with its loose gap. */
export const actions = style({
  display: 'flex',
  gap: 'var(--space-3)',
})
