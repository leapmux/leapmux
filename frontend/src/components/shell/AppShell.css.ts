import { style } from '@vanilla-extract/css'
import { resizeHandleSelectors } from '~/styles/resizeHandle'
import { motion } from '~/styles/tokens'

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
  // One column: the bar in normal flow, the content region below it. The
  // region's top edge IS the bar's bottom edge — flush by construction, with
  // no measured height or safe-area arithmetic to keep in step with the bar.
  display: 'flex',
  flexDirection: 'column',
})

/**
 * Everything below the tab bar: the tile pane and editor in flow, with the
 * drawers and the sheet scrim layered absolutely on top. Owns a stacking
 * context (`z-index: 1`) so its overlays stay BELOW the bar (`z-index: 100`):
 * the bar paints above an open drawer on purpose, keeping its toggles
 * tappable, and the sheet panel that drops from the bar covers the drawer.
 */
export const mobileCenter = style({
  display: 'flex',
  flexDirection: 'column',
  // Fill the shell column below the bar (the shell itself fills the body's
  // *content* area — body consumes safe-area insets via padding +
  // border-box, so the visible region sits inside the system bars). The body
  // still holds the `--vvh` contract for keyboard-up shrinkage.
  flex: 1,
  minHeight: 0,
  overflow: 'hidden',
  position: 'relative',
  zIndex: 1,
})

/**
 * A full-bleed drawer. Absolute in the content region, so it starts exactly
 * at the bar's bottom edge and spans the workspace entirely — it closes
 * through the same tab-bar toggle that opened it, and there is deliberately
 * no dimmed strip left over for a tap-outside-to-close gesture.
 */
export const mobileSidebar = style({
  'position': 'absolute',
  'top': 0,
  'bottom': 0,
  'left': 0,
  'right': 0,
  'width': '100%',
  'zIndex': 100,
  'backgroundColor': 'var(--card)',
  // The drawer's top edge sits directly under the bar — give it a boundary.
  'borderTop': '1px solid var(--border)',
  'transform': 'translateX(-100%)',
  'transition': `transform ${motion.medium}ms ease`,
  'overflow': 'hidden',
  'display': 'flex',
  'flexDirection': 'column',
  '@media': {
    // The drawer slide is the largest motion on the mobile screen; the same
    // reduce-motion override the tab sheet's scrim and panel carry.
    '(prefers-reduced-motion: reduce)': {
      transition: 'none',
    },
  },
})

export const mobileSidebarRight = style({
  left: 'auto',
  right: 0,
  transform: 'translateX(100%)',
})

export const mobileSidebarOpen = style({
  transform: 'translateX(0)',
})

export const mobileTabBar = style({
  position: 'relative',
  zIndex: 100,
})

// The tab sheet's scrim. Rendered unconditionally by MobileLayout; opacity +
// pointer-events flip via `sheetOverlayOpen` so the dim fades in AND out
// alongside the sheet's own slide. Anchored to the content region — the same
// band the drawers start at — so the bar stays bright and tappable while the
// sheet is open, and the bar's chip is the toggle that closes the sheet again.
export const sheetOverlay = style({
  'position': 'absolute',
  'top': 0,
  'left': 0,
  'right': 0,
  'bottom': 0,
  'backgroundColor': 'rgba(0, 0, 0, 0.4)',
  // Above the drawers (z-index 100) inside the region's stacking context,
  // which itself sits below the bar — so the sheet experience dims an open
  // drawer but never the bar.
  'zIndex': 101,
  'opacity': 0,
  'pointerEvents': 'none',
  'transition': `opacity ${motion.medium}ms ease`,
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transition: 'none',
    },
  },
})

export const sheetOverlayOpen = style({
  opacity: 1,
  pointerEvents: 'auto',
})

// Positioning + flex slot for the absolutely-positioned tilePane fragment
// returned by `renderTileContent`. Without this wrapper the tilePanes fall
// out of flow (`position: absolute; inset: 0`) and only the tab bar +
// composer end up in mobileCenter's flex flow — collapsing the composer
// up against the tab bar at the top of the viewport. Mirrors the desktop
// `tileContent` style at `./Tile.css.ts`.
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
  // No `text-overflow` here: this box is a flex container, and the property
  // acts on a block container's own inline content, never on a flex item. The
  // label inside carries `clippedText` and ellipsizes itself.
})
