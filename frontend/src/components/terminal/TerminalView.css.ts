import { globalStyle, style } from '@vanilla-extract/css'

export const terminalInner = style({
  flex: 1,
  overflow: 'hidden',
  // Positioning context for the absolutely-positioned terminal wrappers
  // below. All terminals share this single stacking slot; only the active
  // one is visible. Keeping inactive wrappers in layout (rather than
  // display:none) preserves their xterm.js dimensions so switching tabs
  // doesn't trigger a rewrap/refit/SIGWINCH cycle.
  position: 'relative',
})

export const container = style({
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  overflow: 'hidden',
})

export const terminalWrapper = style({
  position: 'absolute',
  inset: 0,
  overflow: 'hidden',
  backgroundColor: 'var(--background)',
  fontVariantLigatures: 'none',
})

/**
 * For a terminal whose SURFACE owns the background -- the quake panel, which
 * paints the user's opacity on itself.
 *
 * The wrapper is the last opaque layer between that surface and the user: xterm
 * itself paints nothing once its theme background is transparent (see
 * `transparentBackground` in `~/lib/terminal`), and the rule below already
 * neutralizes the one background xterm.css hardcodes.
 */
export const terminalWrapperTransparent = style({
  backgroundColor: 'transparent',
})

/**
 * Applied to inactive terminal wrappers. `visibility: hidden` keeps the
 * element in layout (so its dimensions stay valid for xterm.js / FitAddon)
 * while hiding it visually and suppressing pointer events.
 */
export const terminalWrapperHidden = style({
  visibility: 'hidden',
  pointerEvents: 'none',
})

/** Container that xterm.open() attaches to. Fills the wrapper. */
export const xtermHost = style({
  width: '100%',
  height: '100%',
})

/**
 * "Starting terminal…" overlay layered on top of xterm. Kept visible
 * from tab creation through the first non-whitespace character the
 * shell paints — the backend's READY signal isn't enough because it
 * fires when the PTY is spawned, well before the shell has rendered
 * its prompt.
 */
export const startupOverlay = style({
  position: 'absolute',
  inset: 0,
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  backgroundColor: 'var(--background)',
  color: 'var(--faint-foreground)',
  pointerEvents: 'none',
  zIndex: 1,
})

/**
 * Full-pane centered layout shown in place of xterm when a PTY never
 * spawned (STARTUP_FAILED). Sibling to other TerminalContainer wrappers
 * in the `<For>` loop, so the caller toggles `display` based on the
 * active terminal id.
 */
export const startupErrorPane = style({
  position: 'absolute',
  inset: 0,
  overflow: 'hidden',
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  justifyContent: 'center',
  padding: '24px',
  whiteSpace: 'pre-wrap',
  textAlign: 'center',
  color: 'var(--danger)',
  backgroundColor: 'var(--background)',
})

// Apply padding to the xterm element rather than the wrapper so that
// FitAddon correctly accounts for it when calculating rows/cols.
globalStyle(`${terminalWrapper} .xterm`, {
  padding: 'var(--space-1)',
})

// Override xterm.css default background (#000) so the themed wrapper background
// shows through, and suppress the viewport's own scrollbar.
//
// The background is NOT the theme colour: xterm writes that inline onto
// `.xterm-scrollable-element` (Viewport.ts), which no rule here can or should
// reach. This rule exists only for the opaque `#000` xterm.css declares on the
// viewport itself, which would otherwise sit above the wrapper's palette
// background -- and, for a transparent terminal, above the panel's.
//
// The SCROLLBAR is redundant, and was a second bar beside xterm's own. xterm 6
// draws its scrollbar itself -- the `.slider` of the vendored VS Code
// scrollable element, styled below -- while xterm.css still declares
// `overflow-y: scroll` on this viewport, which asks the browser for a native
// bar on the same edge. Both were painted, a few px apart, in every terminal.
// Hiding it takes two properties because the engines split: Chromium honours
// `scrollbar-width` and then ignores the pseudo-element, Safari has only the
// pseudo-element. Neither stops the viewport SCROLLING -- the wheel and the
// keyboard reach the scrollback exactly as before.
globalStyle(`${terminalWrapper} .xterm .xterm-viewport`, {
  backgroundColor: 'transparent',
  scrollbarWidth: 'none',
})

globalStyle(`${terminalWrapper} .xterm .xterm-viewport::-webkit-scrollbar`, {
  display: 'none',
})

/**
 * xterm's own slider, shaped like every other scrollbar in the app.
 *
 * Its WIDTH is not set here -- xterm writes that inline from
 * `overviewRuler.width`, which `~/lib/terminal` sets to the same 8px box the
 * `::-webkit-scrollbar` rules in `~/styles/global.css.ts` ask for. These rules
 * supply the rest of that shape: a 2px transparent border with
 * `background-clip: content-box` leaves a 4px thumb floating in the 8px lane,
 * which is exactly what the chat's scrollbar beside it looks like.
 *
 * The selectors carry `.xterm` on purpose. xterm injects a stylesheet of its
 * own INTO the terminal element for these three states, at four classes of
 * specificity; matching that exactly would tie, and a tie goes to whichever
 * rule comes later in the document -- always xterm's, because its style element
 * sits further down the tree than this file's.
 *
 * The colours come from the palette rather than from xterm's theme, which has
 * `scrollbarSlider*` entries for exactly this: those take a colour xterm must
 * PARSE, and these tokens are relative-colour functions
 * (`rgb(from var(--muted-foreground) r g b / 0.35)`) that only a browser
 * resolves. Naming the token keeps one definition of "thumb" for the whole app.
 */
globalStyle(`${terminalWrapper} .xterm .xterm-scrollable-element > .scrollbar > .slider`, {
  backgroundColor: 'var(--scrollbar-thumb)',
  backgroundClip: 'content-box',
  border: '2px solid transparent',
  borderRadius: '4px',
})

globalStyle(
  `${terminalWrapper} .xterm .xterm-scrollable-element > .scrollbar > .slider:hover,
   ${terminalWrapper} .xterm .xterm-scrollable-element > .scrollbar > .slider.active`,
  {
    backgroundColor: 'var(--scrollbar-thumb-hover)',
  },
)
