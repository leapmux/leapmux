import { style } from '@vanilla-extract/css'
import { compactAction, compactActionLabelled, compactTextSize } from '~/styles/tokens'

export const chip = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  padding: 'var(--space-2)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-small)',
  fontSize: compactTextSize,
})

export const copy = style({
  flex: '1 1 auto',
  minWidth: 0,
  maxHeight: '8rem',
  overflowY: 'auto',
})

export const label = style({
  fontWeight: 600,
  color: 'var(--muted-foreground)',
})

export const text = style({
  whiteSpace: 'pre-wrap',
  overflowWrap: 'anywhere',
})

export const action = style(compactActionLabelled)
export const dismiss = style({
  ...compactAction,
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  padding: 'var(--space-1)',
})
