import { style } from '@vanilla-extract/css'

export const startupSpinner = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: '0.5em',
})

/**
 * Vertical column shared by `StartupBody` and `StartupErrorBody`. Used
 * for both the neutral fallback body (informational) and the
 * danger-flavored failure body. The wrapping caller sets the text color.
 */
export const startupBody = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'stretch',
  gap: '10px',
  minWidth: 0,
  maxWidth: '100%',
})

export const startupTitle = style({
  margin: 0,
  textAlign: 'start',
})

/** Centered, wrap-friendly row for action buttons rendered under a body. */
export const startupActions = style({
  display: 'flex',
  flexDirection: 'row',
  flexWrap: 'wrap',
  alignItems: 'center',
  justifyContent: 'center',
  gap: 'var(--space-2)',
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
