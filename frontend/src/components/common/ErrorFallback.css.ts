import { style } from '@vanilla-extract/css'

/**
 * Fills its CONTAINER, not the viewport.
 *
 * Two boundaries render this component and the whole point of the pair is that
 * they have different blast radii: the app-level one replaces the shell, the
 * route-level one replaces only the route's slot inside `DesktopRouteChrome`.
 * A `position: fixed; inset: 0` overlay -- which this was -- erases that
 * distinction, and on the desktop build paints straight over `CustomTitlebar`,
 * whose drag region and window buttons are then unreachable: the window cannot
 * be moved, minimised or closed. Sizing to the container instead lets each
 * boundary's placement decide the footprint, which is the thing the scoping was
 * added to buy.
 *
 * Works in both slots because each is a filling box already: the route slot is
 * `minimalTitlebarContent` (`flex: 1`), and the app-level one replaces a
 * `height: 100%` root.
 */
export const container = style({
  position: 'relative',
  boxSizing: 'border-box',
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  justifyContent: 'center',
  gap: 'var(--space-4)',
  flex: 1,
  width: '100%',
  height: '100%',
  minHeight: 0,
  overflow: 'auto',
  padding: 'var(--space-4)',
  // `role="alert"` is the correct semantics for this screen -- it is what makes
  // a screen reader announce the failure -- but @knadh/oat skins that selector
  // as a small inline notice box: border, radius, background, and
  // `font-size: var(--text-7)`. `Alert.tsx` wants exactly that; stretched over
  // a full-height container it draws a ring around the whole area and shrinks
  // the stack trace. vanilla-extract emits UNLAYERED rules and oat's live in
  // `@layer components`, so simply restating these wins the cascade.
  border: 'none',
  borderRadius: 0,
  backgroundColor: 'transparent',
  fontSize: 'inherit',
})

/**
 * A column sized by the stack trace rather than by the viewport, so the action
 * lands on the trace's right edge instead of floating centred under a block
 * whose width depends on whatever the error happened to say.
 */
export const traceColumn = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'flex-end',
  gap: 'var(--space-2)',
  maxWidth: '80vw',
  minHeight: 0,
})

export const trace = style({
  maxWidth: '100%',
  maxHeight: '50vh',
  overflow: 'auto',
  cursor: 'pointer',
})
