import { globalStyle, style } from '@vanilla-extract/css'
import { hideNativeScrollbar } from '~/styles/scrollbar'
import { scrollbarThumb, scrollbarThumbHover } from '~/styles/tokens'

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
 * `transparentBackground` in `~/lib/terminal`), and xterm.css hardcodes its one
 * opaque viewport background only for terminals that did not ask for
 * transparency.
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
 * "Starting terminal…" overlay layered on top of xterm. The overlay stays
 * visible until the shell paints its first non-whitespace character.
 *
 * The overlay inherits the wrapper background. A normal wrapper supplies the
 * opaque app background. A Quake wrapper supplies transparency, which exposes
 * the panel's configured terminal background and opacity.
 */
export const startupOverlay = style({
  position: 'absolute',
  inset: 0,
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  backgroundColor: 'inherit',
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

// The viewport must stay transparent: the terminal theme's background is
// already painted underneath it. xterm writes that colour INLINE on the
// `.xterm` element itself (Viewport.ts, `element.style.backgroundColor` from
// the theme) and again on `.xterm-scrollable-element`. A background declared
// here covers it. xterm.css declares one -- an opaque `#000` -- and the rule
// below neutralizes it.
//
// xterm.css hardcodes it as `.xterm:not(.allow-transparency) .xterm-viewport
// { background-color: #000 }`, at three classes of specificity, so the macOS
// scrollbar is opaque. The override mirrors that selector under the wrapper --
// four classes -- because a tie falls to whichever stylesheet the bundler
// emits last, and that order is not the project's to choose. It flipped once
// already and brought the black viewport back. Keep the override strictly
// more specific than xterm.css's rule; never let the two tie.
//
// The `:not(.allow-transparency)` scoping is deliberate: it covers exactly the
// elements xterm.css paints black. A terminal that asked for transparency
// (the quake panel) matches neither rule and keeps the default transparent
// viewport, which is what lets its panel show through.
globalStyle(`${terminalWrapper} .xterm:not(.allow-transparency) .xterm-viewport`, {
  backgroundColor: 'transparent',
})

// The SCROLLBAR is redundant, and was a second bar beside xterm's own. xterm 6
// draws its scrollbar itself -- the `.slider` of the vendored VS Code
// scrollable element, styled below -- while xterm.css still declares
// `overflow-y: scroll` on this viewport, which asks the browser for a native
// bar on the same edge. Both were painted, a few px apart, in every terminal.
// Hiding it takes two properties because the engines split: Chromium honours
// `scrollbar-width` and then ignores the pseudo-element, Safari has only the
// pseudo-element. Neither stops the viewport SCROLLING -- the wheel and the
// keyboard reach the scrollback exactly as before.
//
// This half matches EVERY viewport, transparent or not: xterm.css declares
// `overflow-y: scroll` on all of them, so the quake panel needs the native bar
// hidden too.
globalStyle(`${terminalWrapper} .xterm .xterm-viewport`, {
  scrollbarWidth: 'none',
})

hideNativeScrollbar(`${terminalWrapper} .xterm .xterm-viewport`)

/**
 * xterm's own slider, shaped like every other scrollbar in the app.
 *
 * Its WIDTH is not set here -- xterm writes that inline from
 * `scrollbar.width`, which `~/lib/terminal` sets from `scrollbarWidthPx` in
 * `~/styles/tokens`, the same token the `::-webkit-scrollbar` rules in
 * `~/styles/global.css.ts` size their box with. The rest of the shape is the
 * shared `scrollbarThumb` declaration: a transparent border with
 * `background-clip: content-box` leaves the thumb floating inside that lane,
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
 * resolves. The shared declaration keeps one definition of "thumb" for the
 * whole app.
 */
globalStyle(`${terminalWrapper} .xterm .xterm-scrollable-element > .scrollbar > .slider`, {
  ...scrollbarThumb,
})

globalStyle(
  `${terminalWrapper} .xterm .xterm-scrollable-element > .scrollbar > .slider:hover,
   ${terminalWrapper} .xterm .xterm-scrollable-element > .scrollbar > .slider.active`,
  { ...scrollbarThumbHover },
)
