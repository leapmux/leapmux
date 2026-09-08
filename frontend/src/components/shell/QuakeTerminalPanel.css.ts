import { style } from '@vanilla-extract/css'
import { motion } from '~/styles/tokens'

/**
 * The clip the panel slides out of.
 *
 * Anchored to one edge of the positioned ancestor -- `center` in
 * `./AppShell.css.ts` on desktop, `mobileCenter` on mobile -- and sized on ONE
 * axis from `--quake-size`. `overflow: hidden` is what hides the panel while it
 * sits outside; without it a closed panel would paint over the sidebars.
 *
 * `pointerEvents: none` so a closed clip never takes a click meant for the tile
 * underneath. The panel turns them back on for itself.
 *
 * Same shape as the mobile tab sheet's clip in `./TabBar.css.ts`, which is the
 * established slide-out in this codebase.
 */
export const quakeClip = style({
  position: 'absolute',
  overflow: 'hidden',
  pointerEvents: 'none',
  zIndex: 10,
  selectors: {
    '&[data-quake-orientation="top"]': {
      top: 0,
      left: 0,
      right: 0,
      height: 'var(--quake-size)',
    },
    '&[data-quake-orientation="bottom"]': {
      bottom: 0,
      left: 0,
      right: 0,
      height: 'var(--quake-size)',
    },
    '&[data-quake-orientation="left"]': {
      left: 0,
      top: 0,
      bottom: 0,
      width: 'var(--quake-size)',
    },
    '&[data-quake-orientation="right"]': {
      right: 0,
      top: 0,
      bottom: 0,
      width: 'var(--quake-size)',
    },
  },
})

/**
 * The sliding surface.
 *
 * The OPEN state settles on `transform: none`, not `translateY(0)`. An identity
 * transform is still a transform, and it makes the element a containing block
 * for every `position: fixed` descendant -- the hazard `./Dialog.css.ts` in
 * `~/components/common` records, where a popover inside a transformed ancestor
 * jumps to the wrong corner on WKWebView. `none` interpolates as the identity
 * matrix, so the slide is unaffected.
 *
 * The four closed selectors are attribute PAIRS of equal specificity and are
 * mutually exclusive, so the open base rule needs no `:not()` and the order of
 * the block does not matter.
 *
 * The background is translucent, and the TEXT is not: the alpha lives on this
 * surface's own colour rather than on an `opacity` that would fade the terminal
 * with it. For that to show, the xterm underneath must paint nothing -- see
 * `transparentBackground` in `~/lib/terminal`, which is what makes both the DOM
 * and the WebGL renderer leave this background alone.
 */
export const quakePanel = style({
  'height': '100%',
  'width': '100%',
  'display': 'flex',
  'flexDirection': 'column',
  'overflow': 'hidden',
  'pointerEvents': 'auto',
  'outline': 'none',
  'backgroundColor': 'color-mix(in srgb, var(--card) var(--quake-opacity), transparent)',
  'boxShadow': '0 2px 8px rgba(0, 0, 0, 0.3)',
  'transform': 'none',
  'transition': `transform var(--quake-duration, ${motion.medium}ms) ease`,
  'selectors': {
    '&[data-quake-orientation="top"]': { borderBottom: '1px solid var(--border)' },
    '&[data-quake-orientation="bottom"]': { borderTop: '1px solid var(--border)' },
    '&[data-quake-orientation="left"]': { borderRight: '1px solid var(--border)' },
    '&[data-quake-orientation="right"]': { borderLeft: '1px solid var(--border)' },
    '&[data-quake-open="false"][data-quake-orientation="top"]': { transform: 'translateY(-100%)' },
    '&[data-quake-open="false"][data-quake-orientation="bottom"]': { transform: 'translateY(100%)' },
    '&[data-quake-open="false"][data-quake-orientation="left"]': { transform: 'translateX(-100%)' },
    '&[data-quake-open="false"][data-quake-orientation="right"]': { transform: 'translateX(100%)' },
  },
  '@media': {
    '(prefers-reduced-motion: reduce)': { transition: 'none' },
  },
})

/** The terminal fills whatever the panel's chrome leaves. */
export const quakeBody = style({
  position: 'relative',
  flex: 1,
  minHeight: 0,
  minWidth: 0,
  display: 'flex',
  flexDirection: 'column',
})
