import { style } from '@vanilla-extract/css'
import { resizeHandleSelectors } from '~/styles/resizeHandle'
import { breakpoints, motion } from '~/styles/tokens'

export const shell = style({
  height: '100%',
  width: '100%',
  overflow: 'hidden',
})

export const sidebar = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  backgroundColor: 'var(--card)',
  borderRight: '1px solid var(--border)',
  overflow: 'hidden',
})

export const resizeHandle = style({
  all: 'unset',
  boxSizing: 'border-box',
  width: '4px',
  background: 'transparent',
  borderRadius: 0,
  position: 'relative',
  flexShrink: 0,
  margin: '0 -2px',
  zIndex: 5,
  cursor: 'col-resize',
  selectors: resizeHandleSelectors('horizontal'),
})

export const center = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  overflow: 'hidden',
})

export const rightPanel = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  backgroundColor: 'var(--card)',
  borderLeft: '1px solid var(--border)',
  overflow: 'hidden',
})

/**
 * Tile content pane — agent, terminal, or file viewer. Sits in the
 * `position: relative` slot established by `tileContent` (in Tile.css)
 * and absolutely fills it. Multiple panes can share the same slot; only
 * the active one is visible. Keeping inactive panes laid out (instead
 * of `display: none`) preserves their dimensions across tab switches —
 * critical for xterm, whose renderer reads container size and can land
 * in a degenerate state when the parent collapses to zero.
 */
export const tilePane = style({
  position: 'absolute',
  inset: 0,
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
})

export const tilePaneHidden = style({
  visibility: 'hidden',
  pointerEvents: 'none',
})

export const fullWindow = style({
  'height': '100%',
  'width': '100%',
  'overflow': 'auto',
  // Mobile only: when the AppShell falls back to rendering non-workspace
  // routes (dashboard etc.) inside this wrapper, we must NOT let it
  // become page-scrollable underneath any nested mobile UI. iOS Safari
  // happily scrolls the outer container while a focused contenteditable
  // is in the inner column, pushing the composer above the visible
  // viewport. Locking the scroll here keeps the chosen layout in charge.
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      overflow: 'hidden',
    },
  },
})

export const placeholder = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  flex: 1,
  color: 'var(--faint-foreground)',
  textAlign: 'center',
  padding: '0 var(--space-6)',
})

export const emptyTileActions = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  justifyContent: 'center',
  gap: 'var(--space-3)',
  flex: 1,
})

export const emptyTileActionContent = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  minWidth: 0,
})

export const emptyTileActionShortcut = style({
  flexShrink: 0,
  color: 'var(--muted-foreground)',
  opacity: 0.75,
  fontSize: 'var(--text-8)',
  whiteSpace: 'nowrap',
})

export const emptyTileHint = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  flex: 1,
  color: 'var(--faint-foreground)',
  textAlign: 'center',
  padding: '0 var(--space-6)',
  cursor: 'default',
})

// --- Mobile layout styles ---

export const mobileShell = style({
  height: '100%',
  width: '100%',
  overflow: 'hidden',
  position: 'relative',
})

export const mobileCenter = style({
  display: 'flex',
  flexDirection: 'column',
  // Fill the body's *content* area (body now consumes safe-area insets
  // via padding + border-box, so `100%` here = visible region inside
  // the system bars). Previously this was `var(--vvh, 100dvh)` which
  // double-counted the height and let the layout overshoot the bottom
  // safe-area in standalone PWA mode. The body still holds the `--vvh`
  // contract for keyboard-up shrinkage.
  height: '100%',
  width: '100%',
  minHeight: 0,
  overflow: 'hidden',
})

export const mobileSidebar = style({
  position: 'fixed',
  // Match body's `padding-top: env(safe-area-inset-top)` so the drawer
  // starts below the system status bar / Dynamic Island. Body's
  // `transform` makes body the containing block for fixed descendants
  // (resolved against body's *padding-box*, which extends up under the
  // status bar), so a literal `top: 0` would land the drawer's content
  // over the system area on a notched iPhone in standalone PWA mode.
  top: 'env(safe-area-inset-top)',
  bottom: 0,
  width: '80%',
  maxWidth: '320px',
  zIndex: 100,
  backgroundColor: 'var(--card)',
  transform: 'translateX(-100%)',
  transition: `transform ${motion.medium}ms ease, box-shadow ${motion.medium}ms ease`,
  // Box-shadow only applied while open — when the drawer is translated
  // off-screen the residual 2px+8px shadow projects into the viewport
  // edge and reads as a gray "gradient" along the left/right side of
  // the screen. Gating it on the open state eliminates that artifact.
  overflow: 'hidden',
  display: 'flex',
  flexDirection: 'column',
  // Clip the drawer's outgoing box-shadow so it only escapes on the
  // *side* facing the page, not above/below. Without this the shadow's
  // 8px blur radius bleeds upward into the safe-area-top region (now
  // visible as white html background above the drawer since the
  // drawer's own `top: env(safe-area-inset-top)` leaves room there)
  // and reads as an awkward gradient on the white. `inset(0 -16px 0 0)`
  // = clip to the drawer's box vertically, plus 16px past the right
  // edge for the side shadow. The right-side drawer overrides this
  // below to mirror the inset on the opposite axis.
  clipPath: 'inset(0 -16px 0 0)',
})

export const mobileSidebarRight = style({
  left: 'auto',
  right: 0,
  transform: 'translateX(100%)',
  clipPath: 'inset(0 0 0 -16px)',
})

export const mobileSidebarOpen = style({
  transform: 'translateX(0)',
  // Box-shadow on the drawer's exposed edge while open. The side is
  // discriminated by the sibling `mobileSidebarRight` class so the two
  // shadow rules live together instead of being split across a base
  // style + a globalStyle override.
  selectors: {
    [`&:not(.${mobileSidebarRight})`]: {
      boxShadow: '2px 0 8px rgba(0, 0, 0, 0.3)',
    },
    [`&.${mobileSidebarRight}`]: {
      boxShadow: '-2px 0 8px rgba(0, 0, 0, 0.3)',
    },
  },
})

// Rendered unconditionally; opacity + pointer-events flip via
// `mobileOverlayOpen` so the dim fades in *and* out alongside the
// drawer's own 200ms transform slide. Mounting on demand via `<Show>`
// would skip the fade entirely.
export const mobileOverlay = style({
  'position': 'fixed',
  // Keep the dim out of the system status bar / Dynamic Island area —
  // dimming over the status bar reads as a glass tint on the iOS chrome
  // and feels wrong. Matches the drawer's own safe-area-inset-top.
  'top': 'env(safe-area-inset-top)',
  'left': 0,
  'right': 0,
  'bottom': 0,
  'backgroundColor': 'rgba(0, 0, 0, 0.4)',
  'zIndex': 99,
  'opacity': 0,
  'pointerEvents': 'none',
  'transition': `opacity ${motion.medium}ms ease`,
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transition: 'none',
    },
  },
})

export const mobileOverlayOpen = style({
  opacity: 1,
  pointerEvents: 'auto',
})

export const mobileTabBar = style({
  position: 'relative',
  zIndex: 100,
})

// Positioning + flex slot for the absolutely-positioned tilePane fragment
// returned by `renderTileContent`. Without this wrapper the tilePanes fall
// out of flow (`position: absolute; inset: 0`) and only the tab bar +
// composer end up in mobileCenter's flex flow — collapsing the composer
// up against the tab bar at the top of the viewport. Mirrors the desktop
// `tileContent` style at `Tile.css.ts`.
export const mobileTilePaneSlot = style({
  flex: 1,
  minHeight: 0,
  position: 'relative',
  overflow: 'hidden',
})

// --- End mobile layout styles ---

export const dragPreviewTooltip = style({
  display: 'flex',
  alignItems: 'center',
  gap: '6px',
  padding: '4px 6px',
  fontSize: '13px',
  background: 'var(--card)',
  border: '1px solid var(--border)',
  borderRadius: '4px 4px 0 0',
  boxShadow: '0 2px 8px rgba(0,0,0,0.15)',
  whiteSpace: 'nowrap',
  maxWidth: '180px',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
})
