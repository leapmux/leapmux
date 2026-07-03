import { style } from '@vanilla-extract/css'

// The skeleton container: fills the row's EXACT reserved height (set inline),
// clipping the fill block so the placeholder can never change the row's
// geometry.
export const rowSkeleton = style({
  overflow: 'hidden',
})

// The single masked shimmer block filling the container. Oat's `.line` preset
// pins height to 1rem, so the full-height override here is required — and it
// only receives Oat's skeleton styles at all because the element carries
// role="status": the component selector is `[role=status].skeleton` (see
// ChatRowSkeleton.tsx). Un-layered, so these overrides beat Oat's @layer
// rules regardless of order.
export const rowSkeletonFill = style({
  height: '100%',
  marginBlockEnd: 0,
})
