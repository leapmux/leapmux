import { style } from '@vanilla-extract/css'

export const warningList = style({
  margin: 0,
  paddingInlineStart: 'var(--space-5)',
})

export const selectorRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
})

export const selectorInput = style({
  flex: 1,
  minWidth: 0,
  marginBlockStart: 0,
  fontFamily: 'var(--font-mono)',
})

export const removeButton = style({
  paddingInline: 'var(--space-3)',
})

export const examples = style({
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-8)',
  lineHeight: 1.6,
})

export const exampleList = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: 'var(--space-1) var(--space-3)',
  margin: 0,
  padding: 0,
  listStyle: 'none',
})

export const addRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
})

export const note = style({
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-8)',
})
