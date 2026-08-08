import { style } from '@vanilla-extract/css'

// wrapper hosts the provider icon + the optional subagent corner overlay. The
// overlay is absolutely positioned bottom-right with a background halo so it
// reads against any provider icon color.
export const wrapper = style({
  position: 'relative',
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
})

export const subagentOverlay = style({
  position: 'absolute',
  bottom: '-2px',
  right: '-2px',
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  color: 'var(--foreground)',
  backgroundColor: 'var(--background)',
  borderRadius: '50%',
  padding: '1px',
  lineHeight: 0,
})
