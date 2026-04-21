import { style } from '@vanilla-extract/css'

/** Two-row layout for the startup-failure body (heading on top, details below). */
export const startupErrorBody = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'stretch',
  gap: '10px',
  minWidth: 0,
  maxWidth: '100%',
})

export const startupErrorTitle = style({
  margin: 0,
  textAlign: 'start',
})

/**
 * Soft-wrapped, monospace code block for the server's formatted error
 * details. `white-space: pre-wrap` preserves the server's line breaks
 * while still wrapping long lines; `overflow-wrap: anywhere` breaks
 * words that would otherwise overflow horizontally (e.g. long paths or
 * stderr snippets with no whitespace).
 */
export const startupErrorDetails = style({
  margin: 0,
  padding: '10px 12px',
  whiteSpace: 'pre-wrap',
  overflowWrap: 'anywhere',
  textAlign: 'start',
  backgroundColor: 'var(--lm-danger-subtle)',
  borderRadius: '6px',
  maxWidth: '100%',
  minWidth: 0,
})
