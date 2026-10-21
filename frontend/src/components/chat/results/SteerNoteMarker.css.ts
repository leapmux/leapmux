import { style } from '@vanilla-extract/css'

/** An inline chip: the note is not the model's own prose, so it stands apart. */
export const steerNoteMarker = style({
  display: 'inline-flex',
  alignItems: 'baseline',
  gap: 'var(--space-1)',
  maxWidth: '100%',
  padding: '0 var(--space-1)',
  borderRadius: 4,
  border: '1px solid var(--border)',
  color: 'var(--foreground-muted)',
  fontFamily: 'var(--font-sans)',
  fontSize: 'var(--text-7)',
  verticalAlign: 'baseline',
})

/** A fixed glyph marks the steer, so the note reads as metadata, not prose. */
export const steerNoteIcon = style({
  color: 'var(--foreground-muted)',
  userSelect: 'none',
})
