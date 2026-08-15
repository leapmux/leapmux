import { style } from '@vanilla-extract/css'
import { resizeHandleSelectors } from '~/styles/resizeHandle'
import { headerHeightPx, motion } from '~/styles/tokens'

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
  // Start BELOW the tab bar, not under it. The bar paints above the drawers
  // (later in DOM at equal z-index) and is opaque, so a drawer that starts
  // at the viewport top has its first section header — the exact 34px band
  // the bar occupies — permanently covered; its header actions (sort,
  // refresh) were unreachable on mobile. `--mobile-tabbar-h` is the MEASURED
  // rendered height of the bar (set on the shell root by MobileLayout via a
  // ResizeObserver), because the bar is content-driven and has no fixed
  // height of its own; the fallback is the shared header height.
  top: `calc(env(safe-area-inset-top) + var(--mobile-tabbar-h, ${headerHeightPx}px))`,
  bottom: 0,
  // Full bleed: the drawer covers the workspace entirely, and closes through
  // the same tab-bar toggle that opened it — there is deliberately no dimmed
  // strip left over for a tap-outside-to-close gesture.
  width: '100%',
  zIndex: 100,
  backgroundColor: 'var(--card)',
  // The drawer's own top edge is exposed now that it no longer tucks under
  // the tab bar — give it a boundary.
  borderTop: '1px solid var(--border)',
  transform: 'translateX(-100%)',
  transition: `transform ${motion.medium}ms ease`,
  overflow: 'hidden',
  display: 'flex',
  flexDirection: 'column',
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
