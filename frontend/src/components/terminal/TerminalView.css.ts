import { globalStyle, style } from '@vanilla-extract/css'

export const terminalInner = style({
  flex: 1,
  overflow: 'hidden',
  display: 'flex',
  flexDirection: 'column',
})

export const container = style({
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  overflow: 'hidden',
})

export const terminalWrapper = style({
  flex: 1,
  overflow: 'hidden',
  backgroundColor: 'var(--background)',
  fontVariantLigatures: 'none',
  position: 'relative',
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
  flex: 1,
  overflow: 'hidden',
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

// Override xterm.css default background (#000) so the themed wrapper background shows through.
globalStyle(`${terminalWrapper} .xterm .xterm-viewport`, {
  backgroundColor: 'transparent',
})
