import { style } from '@vanilla-extract/css'

/**
 * The in-progress IME composition, painted over the cell the cursor sits on.
 *
 * Only layout lives here. The font and the colors are written as inline styles
 * when the preview is shown, because they must follow the live xterm options
 * (`terminal.options.fontFamily`, `fontSize`, and `theme`), which the user can
 * change at any time from the settings panel.
 *
 * xterm ships its own `.composition-view` for this, but it is unusable here on
 * two counts: LeapMux intercepts the composition events before xterm sees them,
 * so xterm never activates it, and it hardcodes `background: #000; color: #FFF`,
 * which is unreadable against a light terminal theme.
 */
export const compositionPreview = style({
  position: 'absolute',
  display: 'none',
  whiteSpace: 'nowrap',
  // Above the renderer's canvases, matching the z-index xterm gives its own
  // composition view so the preview is never painted over by a cell.
  zIndex: 1,
  // The composed text is almost always wider than the single cell the textarea
  // is sized to, so it must be free to overflow that box to the right.
  pointerEvents: 'none',
  // Underline it the way a native input marks composing text, so it reads as
  // pending rather than as terminal output.
  textDecoration: 'underline',
})

/** Applied while a composition is active. */
export const compositionPreviewActive = style({
  display: 'block',
})
