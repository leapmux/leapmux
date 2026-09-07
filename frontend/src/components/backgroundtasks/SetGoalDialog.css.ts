import { style } from '@vanilla-extract/css'

export const form = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  // Wide enough to write a paragraph in, and never wider than the viewport.
  // `Dialog` drops to `width: 100%` below the `sm` breakpoint, so a bare 420px
  // would push the panel off a phone screen. The subtraction matches Oat's own
  // dialog inset, `min(100% - 2rem, 32rem)`.
  minWidth: 'min(420px, calc(100vw - var(--space-8)))',
})

// The field column. Dialog's own stylesheet gives `> .body > form > section`
// its scroller, so the hint, the editor and the byte notice sit in one and
// stack here.
export const field = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
})

export const label = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})

// No `input`, no `actions` and no `button` here on purpose. The field is
// `MarkdownEditor`, which owns its own box; the footer row is the shared
// `actionsFooter` from `~/components/common/actionsFooter.css`, which 30 other
// dialogs use; and the buttons are Oat's plain `<button>` and
// `<button class="outline">`, the pair ConfirmDialog uses. Hand-rolling any of
// them approximated the shared rule and stopped following it when it changed.
