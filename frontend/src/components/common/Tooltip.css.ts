import { style } from '@vanilla-extract/css'
import { floatingCardSurface } from '~/styles/popover.css'

// The fill, border, radius, inset, shadow and line height come from `floatingCardSurface` in
// `~/styles/popover.css.ts` -- the same class the chat rail's preview card carries, so the app's
// two floating surfaces cannot drift apart again. Only what is genuinely a TOOLTIP's own is left
// here: where it sits, how wide it grows, how its text wraps, and that it never takes a pointer.
export const tooltip = style([floatingCardSurface, {
  position: 'fixed',
  margin: 0,
  inset: 'unset',
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
  whiteSpace: 'normal',
  overflowWrap: 'anywhere',
  pointerEvents: 'none',
}])
