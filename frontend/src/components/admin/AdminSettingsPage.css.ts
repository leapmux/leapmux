import { style } from '@vanilla-extract/css'

export { backLink, errorText, successText } from '~/styles/shared.css'

export const section = style({
  marginBottom: 'var(--space-6)',
  borderBottom: '1px solid var(--border)',
  paddingBottom: 'var(--space-6)',
  selectors: {
    '&:last-child': {
      borderBottom: 'none',
      marginBottom: 0,
    },
  },
})

export const fieldLabel = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  fontSize: 'var(--text-7)',
  fontWeight: 400,
  color: 'var(--muted-foreground)',
})

export const toggleRow = style({
  display: 'flex',
  flexDirection: 'row',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 'var(--space-3)',
  padding: 'var(--space-2) 0',
})

export const toggleLabel = style({
  fontWeight: 400,
  color: 'var(--foreground)',
})

export const searchRow = style({
  display: 'flex',
  flexDirection: 'row',
  gap: 'var(--space-3)',
  marginBottom: 'var(--space-4)',
})

export const passwordSetIndicator = style({
  fontSize: 'var(--text-7)',
  color: 'var(--success)',
  fontWeight: 400,
})

export const subsection = style({
  marginTop: 'var(--space-4)',
  padding: 'var(--space-4)',
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
})

export const inlineResetRow = style({
  display: 'flex',
  flexDirection: 'row',
  alignItems: 'center',
  gap: 'var(--space-2)',
})

export const pageContainer = style({
  padding: 'var(--space-6)',
})

export const mutedHint = style({
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-8)',
  padding: 'var(--space-2) 0',
})

export const autoTable = style({
  tableLayout: 'auto',
})

export const flexWrap = style({
  flexWrap: 'wrap',
})

export const inlineInput = style({
  'padding': '2px var(--space-2)',
  'backgroundColor': 'var(--background)',
  'border': '1px solid var(--border)',
  'borderRadius': 'var(--radius-small)',
  'color': 'var(--foreground)',
  'fontSize': 'var(--text-7)',
  'outline': 'none',
  'width': '140px',
  ':focus': {
    borderColor: 'var(--ring)',
    boxShadow: '0 0 0 3px var(--ring)',
  },
})
