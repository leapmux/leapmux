import { style } from '@vanilla-extract/css'
import { spacing } from '~/styles/tokens'

export { authCard, errorText } from '~/styles/shared.css'

export const container = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  height: '100%',
  backgroundColor: 'var(--background)',
})

export const authFooter = style({
  marginTop: spacing.lg,
  fontSize: 'var(--text-8)',
})

export const verificationMessage = style({
  padding: `${spacing.lg} 0`,
})

export const inlineLink = style({
  marginTop: spacing.sm,
  display: 'inline-block',
})
