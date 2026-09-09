import { style } from '@vanilla-extract/css'
import { motion } from '~/styles/tokens'

/**
 * The shadow the open panel drops on the tile it covers, and how far past its
 * own edge that shadow reaches.
 *
 * The reach is DERIVED, because the closed panel has to travel its own size
 * plus that distance: stopping at `-100%` parks the box out of sight but leaves
 * its shadow spilling back over the centre area, as a band of shade with no
 * terminal above it. Writing the closed offset as its own literal would let a
 * wider blur leave it behind, silently, in the one state where the panel is not
 * on screen to explain it.
 */
const SHADOW_OFFSET_PX = 2
const SHADOW_BLUR_PX = 8
const SHADOW_REACH_PX = SHADOW_OFFSET_PX + SHADOW_BLUR_PX
const SHADOW_COLOR = 'rgba(0, 0, 0, 0.3)'

/**
 * The clip the panel slides through.
 *
 * Covers the WHOLE centre area -- `center` in `./AppShell.css.ts` on desktop,
 * `mobileCenter` on mobile -- and the panel takes `--quake-size` on its own
 * axis, resolved against this box. `overflow: hidden` is what hides the panel
 * while it sits outside; without it a closed panel would paint over the
 * sidebars.
 *
 * The clip is deliberately BIGGER than the panel, and that is what lets the
 * panel's shadow exist. `overflow: hidden` clips a descendant's shadow, so a
 * clip cut down to the panel's own size erases it for the whole open state and
 * leaves only the spill of a closed panel -- shade on the centre area with
 * nothing casting it. Sized to the centre instead, the shadow falls INSIDE the
 * clip and travels with the slide, which is the one place it can both show and
 * move. The alternative -- hanging it on this element, whose own shadow escapes
 * its own `overflow` -- cannot move at all: the clip never slides, so its
 * shadow appears at the panel's final edge the instant it turns on.
 *
 * `pointerEvents: none` so the clip never takes a click meant for the tile
 * underneath. It now covers every tile rather than the panel's strip, so this
 * is load-bearing rather than tidy. The panel turns them back on for itself.
 *
 * ONE `zIndex` serves both mounts. On desktop it clears the tile resize handles
 * at 5, which are the clip's siblings. On mobile it stays under the drawers at
 * 100 and the sheet scrim at 101 -- those are siblings too, because
 * `mobileCenter` in `./AppShell.css.ts` is itself a stacking context, so a
 * drawer covers the panel exactly as it covers the workspace.
 */
export const quakeClip = style({
  position: 'absolute',
  inset: 0,
  overflow: 'hidden',
  pointerEvents: 'none',
  zIndex: 10,
})

/**
 * The sliding surface.
 *
 * Anchored to one edge of the clip and sized from `--quake-size` on ONE axis:
 * the orientation rule picks whether that lands on height or width, so there is
 * no second value to keep in step. The percentage resolves against the clip,
 * which is the centre area, so it means what the setting says.
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
 * the block does not matter. Each carries the shadow's reach on top of its own
 * `100%`; see `SHADOW_REACH_PX`.
 *
 * The background is translucent, and the TEXT is not: the alpha lives on this
 * surface's own colour rather than on an `opacity` that would fade the terminal
 * with it. For that to show, the xterm underneath must paint nothing -- see
 * `transparentBackground` in `~/lib/terminal`, which is what makes both the DOM
 * and the WebGL renderer leave this background alone.
 *
 * That colour is `--background` and NOT the `--card` a floating surface would
 * normally take, because this panel is a terminal sliding over a terminal: a
 * tile paints `--background` on `terminalWrapper` in
 * `~/components/terminal/TerminalView.css.ts` and again in xterm's own theme,
 * so `--card` made the same shell read as a lighter slab -- and it stayed
 * lighter at full opacity, since the token difference is independent of the
 * alpha. The slide, the border and the shadow are what mark the panel as
 * floating; the colour does not have to.
 */
export const quakePanel = style({
  'position': 'absolute',
  'display': 'flex',
  'flexDirection': 'column',
  'overflow': 'hidden',
  'pointerEvents': 'auto',
  'outline': 'none',
  'backgroundColor': 'color-mix(in srgb, var(--background) var(--quake-opacity), transparent)',
  'transform': 'none',
  'transition': `transform var(--quake-duration, ${motion.medium}ms) ease`,
  'selectors': {
    '&[data-quake-orientation="top"]': {
      top: 0,
      left: 0,
      right: 0,
      height: 'var(--quake-size)',
      borderBottom: '1px solid var(--border)',
      boxShadow: `0 ${SHADOW_OFFSET_PX}px ${SHADOW_BLUR_PX}px ${SHADOW_COLOR}`,
    },
    '&[data-quake-orientation="bottom"]': {
      bottom: 0,
      left: 0,
      right: 0,
      height: 'var(--quake-size)',
      borderTop: '1px solid var(--border)',
      boxShadow: `0 -${SHADOW_OFFSET_PX}px ${SHADOW_BLUR_PX}px ${SHADOW_COLOR}`,
    },
    '&[data-quake-orientation="left"]': {
      left: 0,
      top: 0,
      bottom: 0,
      width: 'var(--quake-size)',
      borderRight: '1px solid var(--border)',
      boxShadow: `${SHADOW_OFFSET_PX}px 0 ${SHADOW_BLUR_PX}px ${SHADOW_COLOR}`,
    },
    '&[data-quake-orientation="right"]': {
      right: 0,
      top: 0,
      bottom: 0,
      width: 'var(--quake-size)',
      borderLeft: '1px solid var(--border)',
      boxShadow: `-${SHADOW_OFFSET_PX}px 0 ${SHADOW_BLUR_PX}px ${SHADOW_COLOR}`,
    },
    '&[data-quake-open="false"][data-quake-orientation="top"]': {
      transform: `translateY(calc(-100% - ${SHADOW_REACH_PX}px))`,
    },
    '&[data-quake-open="false"][data-quake-orientation="bottom"]': {
      transform: `translateY(calc(100% + ${SHADOW_REACH_PX}px))`,
    },
    '&[data-quake-open="false"][data-quake-orientation="left"]': {
      transform: `translateX(calc(-100% - ${SHADOW_REACH_PX}px))`,
    },
    '&[data-quake-open="false"][data-quake-orientation="right"]': {
      transform: `translateX(calc(100% + ${SHADOW_REACH_PX}px))`,
    },
  },
  '@media': {
    '(prefers-reduced-motion: reduce)': { transition: 'none' },
  },
})

/**
 * The hide control, floated over the terminal's top-right corner.
 *
 * Absolute rather than a header row, so the panel keeps giving the shell every
 * row it has: a quake terminal is short by design, and a chrome bar would cost
 * one of them permanently. The offsets are positioning, not spacing, so they
 * are plain pixels and not `--space-N`.
 */
export const quakeClose = style({
  position: 'absolute',
  top: 4,
  right: 4,
  zIndex: 1,
  opacity: 0.55,
  selectors: {
    '&:hover, &:focus-visible': { opacity: 1 },
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
