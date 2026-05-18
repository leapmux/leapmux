import { style } from '@vanilla-extract/css'

export const tooltip = style({
  position: 'fixed',
  margin: 0,
  inset: 'unset',
  padding: 'var(--space-2) var(--space-3)',
  fontSize: 'var(--text-8)',
  // Short button labels stay one line; long content (e.g. Claude
  // TaskCreate descriptions) soft-wraps at `maxWidth`. We need to
  // override the popover UA default `width: fit-content` because
  // `wordBreak: 'break-word'` makes every character a valid break
  // point, which collapses fit-content to min-content (≈1ch wide).
  // `width: max-content` keeps the natural single-line width as the
  // preferred size and lets `max-width` clamp it.
  width: 'max-content',
  maxWidth: 'min(28rem, calc(100vw - var(--space-4)))',
  lineHeight: 1.4,
  whiteSpace: 'normal',
  overflowWrap: 'anywhere',
  background: 'var(--card)',
  color: 'var(--foreground)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  boxShadow: '0 2px 8px rgba(0, 0, 0, 0.15)',
  pointerEvents: 'none',
})
