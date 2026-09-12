import { globalStyle, style } from '@vanilla-extract/css'
import { markdownContent } from '../markdownEditor/markdownContent.css'

export const questionOption = style({
  minWidth: 0,
})

export const questionPreview = style({
  maxHeight: '18rem',
  minWidth: 0,
  overflow: 'auto',
  marginBlock: 'var(--space-1)',
  marginInlineStart: 'var(--space-6)',
  padding: 'var(--space-2)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-small)',
  backgroundColor: 'var(--card)',
  fontFamily: 'var(--font-mono)',
  fontSize: 'var(--text-8)',
  fontVariantLigatures: 'none',
  whiteSpace: 'pre',
})

// Preview columns must stay aligned, including fenced ASCII diagrams.
globalStyle(`${questionPreview} ${markdownContent}`, {
  whiteSpace: 'pre',
  overflowWrap: 'normal',
  wordBreak: 'normal',
})
globalStyle(`${questionPreview} ${markdownContent} pre code`, {
  whiteSpace: 'pre',
  overflowWrap: 'normal',
  wordBreak: 'normal',
})
