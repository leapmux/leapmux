import { style } from '@vanilla-extract/css'

export const form = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  minWidth: '320px',
})

// The field column. Dialog's own stylesheet gives `> .body > form > section`
// its scroller, so the label and the textarea sit in one and stack here.
export const field = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
})

export const label = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})

export const input = style({
  width: '100%',
  fontFamily: 'inherit',
  fontSize: 'var(--text-7)',
  resize: 'vertical',
})

// No `actions` and no `button` here on purpose. The footer row is the shared
// `actionsFooter` from `~/components/common/actionsFooter.css`, which 30 other
// dialogs use, and the buttons are Oat's plain `<button>` and
// `<button class="outline">`, the pair ConfirmDialog uses. Hand-rolling either
// one approximated the shared rule and stopped following it when it changed.
