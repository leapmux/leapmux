import { style } from '@vanilla-extract/css'
import { spacing } from '~/styles/tokens'

export { backLink, errorText, successText, warningText } from '~/styles/shared.css'

export const container = style({
  maxWidth: '600px',
  margin: '0 auto',
  padding: spacing.xxl,
})

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

export const pillGroup = style({
  display: 'flex',
  flexDirection: 'row',
  gap: spacing.sm,
})

export const pillOption = style({
  'padding': `${spacing.sm} ${spacing.lg}`,
  'backgroundColor': 'var(--card)',
  'color': 'var(--muted-foreground)',
  'border': '1px solid var(--border)',
  'borderRadius': 'var(--radius-medium)',
  'fontWeight': 400,
  'cursor': 'pointer',
  ':hover': {
    backgroundColor: 'var(--card)',
    borderColor: 'var(--muted-foreground)',
  },
})

export const pillOptionActive = style({
  padding: `${spacing.sm} ${spacing.lg}`,
  backgroundColor: 'var(--primary)',
  color: '#ffffff',
  border: '1px solid var(--primary)',
  borderRadius: 'var(--radius-medium)',
  fontWeight: 400,
  cursor: 'pointer',
})

export const toggleLabel = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  fontWeight: 400,
  color: 'var(--foreground)',
  cursor: 'pointer',
})

export const fontListHeader = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  marginTop: spacing.md,
})

export const fontList = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '2px',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  overflow: 'hidden',
})

export const fontListItem = style({
  'display': 'flex',
  'alignItems': 'center',
  'gap': spacing.sm,
  'padding': `${spacing.sm} ${spacing.md}`,
  'backgroundColor': 'var(--card)',
  'cursor': 'grab',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
})

export const fontDragHandle = style({
  color: 'var(--faint-foreground)',
  cursor: 'grab',
  userSelect: 'none',
})

export const fontName = style({
  flex: 1,
  fontSize: 'var(--text-7)',
  color: 'var(--foreground)',
})

export const fontRemoveButton = style({
  'display': 'flex',
  'alignItems': 'center',
  'justifyContent': 'center',
  'width': '20px',
  'height': '20px',
  'borderRadius': '3px',
  'color': 'var(--faint-foreground)',
  'cursor': 'pointer',
  'border': 'none',
  'background': 'none',
  'padding': 0,
  ':hover': {
    color: 'var(--danger)',
    backgroundColor: 'var(--card)',
  },
})

export const fontEditWrapper = style({
  flex: 1,
  display: 'flex',
  flexDirection: 'column',
  gap: '2px',
})

export const fontEditInput = style({
  'width': '100%',
  'padding': '2px 4px',
  'fontSize': 'var(--text-7)',
  'fontFamily': 'inherit',
  'color': 'var(--foreground)',
  'backgroundColor': 'var(--background)',
  'border': '1px solid var(--ring)',
  'borderRadius': 'var(--radius-small)',
  'outline': 'none',
  'boxSizing': 'border-box',
  ':focus': {
    boxShadow: '0 0 0 2px var(--ring)',
  },
})

export const fontEditError = style({
  fontSize: 'var(--text-8)',
  color: 'var(--danger)',
})

export const fontListEmpty = style({
  fontSize: 'var(--text-7)',
  color: 'var(--faint-foreground)',
  padding: `${spacing.sm} 0`,
  fontStyle: 'italic',
})

export const fontAddRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: spacing.sm,
})

export const sliderGroup = style({
  display: 'flex',
  flexDirection: 'column',
  gap: spacing.sm,
  marginTop: spacing.md,
})

export const sliderRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: spacing.md,
})

export const sliderValue = style({
  minWidth: '40px',
  textAlign: 'right',
  fontSize: 'var(--text-7)',
  color: 'var(--muted-foreground)',
})

export const volumeOverrideRow = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: spacing.md,
})
