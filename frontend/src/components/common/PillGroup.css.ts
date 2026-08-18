import { style } from '@vanilla-extract/css'

export const pillGroup = style({
  display: 'flex',
  flexDirection: 'row',
  gap: 'var(--space-2)',
})

/** Shape and metrics shared by both pill states; only the colors differ. */
const pillBase = style({
  padding: 'var(--space-2) var(--space-4)',
  borderRadius: 'var(--radius-medium)',
  fontWeight: 'var(--font-normal)',
  cursor: 'pointer',
})

export const pillOption = style([pillBase, {
  'backgroundColor': 'var(--card)',
  'color': 'var(--muted-foreground)',
  'border': '1px solid var(--border)',
  // Hover changes the border only. The background stays `--card`, which is
  // what the unhovered pill already uses.
  ':hover': {
    borderColor: 'var(--muted-foreground)',
  },
}])

export const pillOptionActive = style([pillBase, {
  backgroundColor: 'var(--primary)',
  color: '#ffffff',
  border: '1px solid var(--primary)',
}])
