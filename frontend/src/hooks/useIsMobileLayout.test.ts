import { cleanup, render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { breakpoints } from '~/styles/tokens'
import { stubMatchMedia } from '~/test-support/matchMediaStub'
import { useIsMobileLayout } from './useIsMobileLayout'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

/**
 * Mount the hook and hand back its accessor.
 *
 * `useViewportBelow` registers its media query in `onMount`, so the hook needs
 * an owner with a mount phase; a bare `createRoot` never registers anything.
 */
function mount(): () => boolean {
  let below!: () => boolean
  render(() => {
    below = useIsMobileLayout()
    return null
  })
  return below
}

/**
 * The threshold is the ONE thing this hook decides — it delegates everything
 * else to `useViewportBelow`, which is covered beside it.
 *
 * `md` is the mobile-layout flavor cutoff: below it `AppShell` renders the
 * drawer-based `MobileShellLayer` instead of the tiling desktop one. `sm` is
 * the phone form factor, a band inside this one, and switching the whole
 * shell at `sm` would leave a small tablet tiling panes it has no room for.
 */
describe('useIsMobileLayout', () => {
  it('watches the md breakpoint, and no other one', () => {
    const mm = stubMatchMedia()
    try {
      mount()
      expect(mm.handlersFor(`(max-width: ${breakpoints.md - 1}px)`)).toHaveLength(1)
      expect(mm.handlersFor(`(max-width: ${breakpoints.sm - 1}px)`)).toHaveLength(0)
    }
    finally {
      mm.restore()
    }
  })

  // The boundary itself: `md` is a MIN-width threshold, so the last mobile
  // pixel is `md - 1` and `md` already belongs to the desktop layout.
  it('reports the band as below md, and md itself as outside it', () => {
    vi.stubGlobal('innerWidth', breakpoints.md - 1)
    expect(mount()()).toBe(true)
    cleanup()

    vi.stubGlobal('innerWidth', breakpoints.md)
    expect(mount()()).toBe(false)
  })

  it('follows its query after mount', () => {
    const mm = stubMatchMedia()
    try {
      const below = mount()
      expect(below()).toBe(false)
      for (const handler of mm.handlersFor(`(max-width: ${breakpoints.md - 1}px)`))
        handler({ matches: true })
      expect(below()).toBe(true)
    }
    finally {
      mm.restore()
    }
  })
})
