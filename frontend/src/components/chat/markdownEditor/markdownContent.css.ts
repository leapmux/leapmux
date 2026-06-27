import { globalStyle, style } from '@vanilla-extract/css'
import { codeBlockCode, codeBlockPre } from '~/styles/codeBlock'
import { iconSize } from '~/styles/tokens'

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
  marginRight: 'var(--space-1)',
  verticalAlign: 'middle',
  pointerEvents: 'none',
})

// Copy button for code blocks (injected via DOM by MessageBubble.injectCopyButtons).
//
// Keyed to the `code-copy-host` marker class the injector adds to every <pre> it
// augments -- NOT to `.markdownContent`. The button is injected into code blocks in any
// context (markdown bodies AND non-markdown <pre> such as a result-divider error
// detail), but the positioning used to be scoped to `${markdownContent} pre ...`, so a
// <pre> outside the markdown wrapper got an UNpositioned button that fell inline at the
// end of the text. Anchoring on the marker class instead positions it top-right
// everywhere, and the marker carries `position: relative` so the absolute button anchors
// to its own <pre> regardless of the surrounding layout.
export const codeCopyHostClass = 'code-copy-host'

globalStyle(`.${codeCopyHostClass}`, {
  position: 'relative',
})

globalStyle(`.${codeCopyHostClass} .copy-code-button`, {
  all: 'unset',
  boxSizing: 'border-box',
  position: 'absolute',
  top: 'var(--space-1)',
  right: 'var(--space-1)',
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
  transition: 'opacity var(--transition)',
})

globalStyle(`.${codeCopyHostClass}:hover .copy-code-button`, {
  opacity: '1',
})

globalStyle(`.${codeCopyHostClass} .copy-code-button:hover`, {
  backgroundColor: 'var(--card)',
  color: 'var(--foreground)',
})
