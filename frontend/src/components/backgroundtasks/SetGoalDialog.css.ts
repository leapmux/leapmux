import { style } from '@vanilla-extract/css'

/**
 * The field column, and the dialog's width.
 *
 * `Dialog`'s own stylesheet gives `> .body > section` the scroller and the
 * edge-to-edge bleed, so the hint, the editor and the byte notice sit in one
 * and stack here. There is no wrapper element above it -- see `SetGoalDialog`.
 *
 * The width lives here for that reason. Wide enough to write a paragraph in,
 * and never wider than the viewport: `Dialog` drops to `width: 100%` below the
 * `sm` breakpoint, so a bare 420px would push the panel off a phone screen. The
 * subtraction matches Oat's own dialog inset, `min(100% - 2rem, 32rem)`.
 */
export const field = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  minWidth: 'min(420px, calc(100vw - var(--space-8)))',
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
