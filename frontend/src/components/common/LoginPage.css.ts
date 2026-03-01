import { style } from '@vanilla-extract/css'

export { authCard, errorText } from '~/styles/shared.css'

export const container = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  height: '100%',
  backgroundColor: 'var(--background)',
})

export const authFooter = style({
  marginTop: 'var(--space-4)',
  fontSize: 'var(--text-8)',
})

export const verificationMessage = style({
  padding: 'var(--space-4) 0',
})

export const inlineLink = style({
  marginTop: 'var(--space-2)',
  display: 'inline-block',
})
