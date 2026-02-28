import { globalStyle, style } from '@vanilla-extract/css'
import { codeBlockCode, codeBlockPre } from '~/styles/codeBlock'
import { iconSize, spacing } from '~/styles/tokens'

export const markdownContent = style({
  wordBreak: 'break-word',
})

// Code blocks: move scroll to <code> so the copy button stays fixed.
globalStyle(`${markdownContent} pre`, codeBlockPre('hidden'))
globalStyle(`${markdownContent} pre code`, codeBlockCode)

// Shiki dual-theme support via CSS variables
globalStyle(`${markdownContent} pre.shiki`, {
  color: 'var(--shiki-light)',
})

globalStyle(`${markdownContent} pre.shiki span`, {
  color: 'var(--shiki-light)',
})

globalStyle(`html[data-theme="dark"] ${markdownContent} pre.shiki`, {
  color: 'var(--shiki-dark)',
})

globalStyle(`html[data-theme="dark"] ${markdownContent} pre.shiki span`, {
  color: 'var(--shiki-dark)',
})

// Task list checkboxes
globalStyle(`${markdownContent} li > input[type="checkbox"]`, {
  marginRight: spacing.xs,
  verticalAlign: 'middle',
  pointerEvents: 'none',
})

// Copy button for code blocks (injected via DOM)
globalStyle(`${markdownContent} pre .copy-code-button`, {
  all: 'unset',
  boxSizing: 'border-box',
  position: 'absolute',
  top: spacing.xs,
  right: spacing.xs,
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: iconSize.container.md,
  height: iconSize.container.md,
  borderRadius: 'var(--radius-small)',
  border: '1px solid var(--border)',
  backgroundColor: 'var(--card)',
  color: 'var(--muted-foreground)',
  cursor: 'pointer',
  opacity: '0',
  transition: 'opacity 0.15s',
})

globalStyle(`${markdownContent} pre:hover .copy-code-button`, {
  opacity: '1',
})

globalStyle(`${markdownContent} pre .copy-code-button:hover`, {
  backgroundColor: 'var(--card)',
  color: 'var(--foreground)',
})
