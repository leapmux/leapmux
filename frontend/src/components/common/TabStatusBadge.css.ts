import { createVar, globalStyle, style } from '@vanilla-extract/css'
import { clippedText } from '~/styles/shared.css'

/**
 * The tone's color, from the design tokens. Neutral text reads as muted, like
 * the rest of a tab's secondary text; its ring takes the primary color, as
 * the terminal's own progress bar does.
 */
const toneColor = createVar()
const ringColor = createVar()

export const status = style({
  vars: {
    [toneColor]: 'var(--muted-foreground)',
    [ringColor]: 'var(--primary)',
  },
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  minWidth: 0,
  // A status shares the row with the title, and the title wins: the status
  // gives up its width first and caps it, so a long status cannot hide the
  // name of the tab.
  flex: '0 2 auto',
  maxWidth: '96px',
  fontSize: 'var(--text-8)',
  color: toneColor,
  selectors: {
    '&[data-tone="success"]': {
      vars: { [toneColor]: 'var(--success)', [ringColor]: 'var(--success)' },
    },
    '&[data-tone="warning"]': {
      vars: { [toneColor]: 'var(--warning)', [ringColor]: 'var(--warning)' },
    },
    '&[data-tone="error"]': {
      vars: { [toneColor]: 'var(--danger)', [ringColor]: 'var(--danger)' },
    },
  },
})

/** The text, cut with an ellipsis; the tooltip holds the whole of it. */
export const text = style([clippedText, {}])

/**
 * The progress, as a ring that fills clockwise: a conic gradient up to the
 * percentage, masked to a ring. The sizes are a glyph's, not spacing, so the
 * `--space-N` scale does not apply to them.
 */
export const ring = style({
  flexShrink: 0,
  width: '10px',
  height: '10px',
  borderRadius: '50%',
  background: `conic-gradient(${ringColor} var(--tab-status-progress, 0%), var(--border) 0)`,
  mask: 'radial-gradient(farthest-side, transparent calc(100% - 3px), #000 calc(100% - 3px))',
})

/**
 * The dot that stands for a status without a progress, so a tile too small to
 * show the text still shows that the tab has a status, in its tone.
 */
export const dot = style({
  flexShrink: 0,
  width: '6px',
  height: '6px',
  borderRadius: '50%',
  backgroundColor: ringColor,
})

// A compact tile hides the tab's title, and the status text with it: the
// ring or the dot still shows that the tab has a status, in its tone.
globalStyle(`[data-tile-size="compact"] ${text}`, {
  display: 'none',
})
