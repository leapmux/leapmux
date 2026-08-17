import { breakpoints } from '~/styles/tokens'
import { useViewportBelow } from './useViewportBelow'

/** Whether the viewport sits in the mobile-layout band (below `md`). */
export function useIsMobileLayout() {
  return useViewportBelow(breakpoints.md)
}
