import { style } from '@vanilla-extract/css'
import { spacing } from '~/styles/tokens'

export { backLink, errorText, successText } from '~/styles/shared.css'

export const section = style({
  marginBottom: spacing.xl,
  borderBottom: '1px solid var(--border)',
  paddingBottom: spacing.xl,
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
  gap: spacing.xs,
  fontSize: 'var(--text-7)',
  fontWeight: 400,
  color: 'var(--muted-foreground)',
})

export const toggleRow = style({
  display: 'flex',
  flexDirection: 'row',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: spacing.md,
  padding: `${spacing.sm} 0`,
})

export const toggleLabel = style({
  fontWeight: 400,
  color: 'var(--foreground)',
})

export const searchRow = style({
  display: 'flex',
  flexDirection: 'row',
  gap: spacing.md,
  marginBottom: spacing.lg,
})

export const passwordSetIndicator = style({
  fontSize: 'var(--text-7)',
  color: 'var(--success)',
  fontWeight: 400,
})

export const subsection = style({
  marginTop: spacing.lg,
  padding: spacing.lg,
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
})

export const inlineResetRow = style({
  display: 'flex',
  flexDirection: 'row',
  alignItems: 'center',
  gap: spacing.sm,
})

export const pageContainer = style({
  padding: spacing.xl,
})

export const mutedHint = style({
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-8)',
  padding: `${spacing.sm} 0`,
})

export const autoTable = style({
  tableLayout: 'auto',
})

export const flexWrap = style({
  flexWrap: 'wrap',
})

export const inlineInput = style({
  'padding': `2px ${spacing.sm}`,
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
