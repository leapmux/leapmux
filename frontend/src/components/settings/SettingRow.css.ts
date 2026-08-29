import { style } from '@vanilla-extract/css'

export const row = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  padding: 'var(--space-4) 0',
  borderBottom: '1px solid var(--border)',
  minWidth: 0,
})

export const headerRow = style({
  display: 'flex',
  alignItems: 'baseline',
  justifyContent: 'space-between',
  gap: 'var(--space-3)',
  minWidth: 0,
})

// Margin alone. The label is an <h3>, so Oat's heading rule owns the type:
// size, weight, colour and break-word come from there, and restating them
// here would only drift. The one thing a settings row must not take from a
// page heading is its spacing, so the margins reset to zero.
export const label = style({
  margin: 0,
})

export const helpText = style({
  fontSize: 'var(--text-7)',
  color: 'var(--muted-foreground)',
  overflowWrap: 'break-word',
})

export const statusRow = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: 'var(--space-2)',
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  minWidth: 0,
  overflowWrap: 'anywhere',
})

export const sliderRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-3)',
})

export const sliderValue = style({
  minWidth: '40px',
  textAlign: 'right',
  fontSize: 'var(--text-7)',
  color: 'var(--muted-foreground)',
})

export const numberRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  minWidth: 0,
  maxWidth: '100%',
})

/** Native number inputs have a large UA min-width; cap them to the row. */
export const numberInput = style({
  minWidth: 0,
  width: '100%',
  maxWidth: '16rem',
})

export const unitLabel = style({
  fontSize: 'var(--text-7)',
  color: 'var(--muted-foreground)',
  flexShrink: 0,
})

export const secretRow = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  flexWrap: 'wrap',
})

export const resetButton = style({
  'fontSize': 'var(--text-8)',
  'background': 'none',
  'border': 'none',
  'padding': 0,
  'color': 'var(--muted-foreground)',
  'cursor': 'pointer',
  'textDecoration': 'underline',
  ':hover': {
    color: 'var(--danger)',
  },
})

export const effectiveNote = style({
  color: 'var(--muted-foreground)',
})

/** The scope chip trigger: "Account default" / "This device" / "Account". */
export const scopeChip = style({
  'display': 'inline-flex',
  'alignItems': 'center',
  'gap': 'var(--space-1)',
  'padding': 'var(--space-1) var(--space-3)',
  'fontSize': 'var(--text-8)',
  'color': 'var(--muted-foreground)',
  'backgroundColor': 'var(--card)',
  'border': '1px solid var(--border)',
  'borderRadius': 'var(--radius-full)',
  'cursor': 'pointer',
  ':hover': {
    borderColor: 'var(--muted-foreground)',
  },
})

/** The non-interactive scope note rendered on single-tier rows. */
export const scopePlain = style({
  fontSize: 'var(--text-8)',
  color: 'var(--faint-foreground)',
})
