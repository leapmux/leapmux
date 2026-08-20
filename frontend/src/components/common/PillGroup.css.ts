import { style } from '@vanilla-extract/css'

export const pillGroup = style({
  display: 'flex',
  flexDirection: 'row',
  gap: 'var(--space-2)',
})

/** Shape and metrics shared by both pill states; only the colors differ. */
const pillBase = style({
  'padding': 'var(--space-2) var(--space-4)',
  'borderRadius': 'var(--radius-medium)',
  'fontWeight': 'var(--font-normal)',
  'cursor': 'pointer',
  // A governed group keeps its colors and dims as a whole, so the selection it
  // shows stays readable while the pointer says it is not the thing to click.
  ':disabled': {
    cursor: 'default',
    opacity: 0.55,
  },
})

export const pillOption = style([pillBase, {
  backgroundColor: 'var(--card)',
  color: 'var(--muted-foreground)',
  border: '1px solid var(--border)',
  selectors: {
    // Hover changes the border only. The background stays `--card`, which is
    // what the unhovered pill already uses. Excluded while disabled: a pill
    // that lights up under the pointer promises a click it refuses.
    '&:hover:not(:disabled)': {
      borderColor: 'var(--muted-foreground)',
    },
  },
}])

export const pillOptionActive = style([pillBase, {
  backgroundColor: 'var(--primary)',
  // The label reads from the palette, never from a literal. Every variant
  // publishes `--primary-foreground` against its own `--primary`, and 21 of
  // the 30 set it to black -- a literal white is unreadable on those, down to
  // 1.49:1 on Ayu Mirage. `themes.test.ts` floors this exact pair at 3:1.
  color: 'var(--primary-foreground)',
  border: '1px solid var(--primary)',
}])
