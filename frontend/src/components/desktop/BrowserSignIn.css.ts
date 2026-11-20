import { style } from '@vanilla-extract/css'
import { unstyledLink } from './ceremonyPage.css'

export const primaryButton = style({
  padding: 'var(--space-3) var(--space-6)',
  fontSize: '1rem',
})

export const switchMode = style([
  unstyledLink,
  { marginTop: 'var(--space-4)' },
])
