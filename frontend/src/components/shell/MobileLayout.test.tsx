import type { MobileOverlay } from './MobileLayout'
import type { SwipeDirection } from '~/lib/horizontalSwipe'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { SWIPE_MIN_PX } from '~/lib/horizontalSwipe'
import { pointerEvent } from '~/test-support/pointer'
import * as styles from './AppShell.css'
import { createMobileOverlayState, MobileLayout, nextOverlayForSwipe } from './MobileLayout'

interface RenderOpts {
  overlay?: MobileOverlay
  tabBarHidden?: boolean
  onSwipe?: (direction: SwipeDirection) => void
}

function renderMobile(opts: RenderOpts = {}, onCloseSheet: () => void = () => {}) {
  return render(() => (
    <MobileLayout
      overlay={opts.overlay ?? 'none'}
      tabBarHidden={opts.tabBarHidden ?? false}
      onCloseSheet={onCloseSheet}
      onSwipe={opts.onSwipe ?? (() => {})}
      leftSidebarElement={<div data-testid="sidebar-left">left</div>}
      rightSidebarElement={<div data-testid="sidebar-right">right</div>}
      tabBarElement={<div data-testid="tab-bar">tab-bar</div>}
      tileContent={<div data-testid="tile-content">tiles</div>}
      editorPanel={<div data-testid="editor-panel">editor</div>}
    />
  ))
}

/** Drag a finger across `target` by `dx`, in four samples, and lift. */
function swipeAcross(target: HTMLElement, dx: number) {
  const y = 300
  target.dispatchEvent(pointerEvent('pointerdown', { x: 200, y, pointerType: 'touch' }))
  for (let step = 1; step <= 4; step++)
    target.dispatchEvent(pointerEvent('pointermove', { x: 200 + (dx * step) / 4, y, pointerType: 'touch' }))
  target.dispatchEvent(pointerEvent('pointerup', { x: 200 + dx, y, pointerType: 'touch' }))
}

/** The wrapper the bar element sits in, which is what carries the hide class. */
function tabBarWrapper(): HTMLElement {
  const wrapper = screen.getByTestId('tab-bar').parentElement
  if (!wrapper)
    throw new Error('the tab bar element has no wrapper')
  return wrapper
}

/** The drawer panel wrapper, which is what carries the open class. */
function findSidebarPanel(side: 'left' | 'right'): HTMLElement {
  return screen.getByTestId(`mobile-drawer-${side}`)
}

describe('mobileLayout', () => {
  it('renders both sidebars closed by default', () => {
    renderMobile({})

    const leftPanel = findSidebarPanel('left')
    const rightPanel = findSidebarPanel('right')
    expect(leftPanel.classList.contains(styles.mobileSidebarOpen)).toBe(false)
    expect(rightPanel.classList.contains(styles.mobileSidebarOpen)).toBe(false)
  })

  it('applies the open class to the left sidebar when overlay is left', () => {
    renderMobile({ overlay: 'left' })

    const leftPanel = findSidebarPanel('left')
    const rightPanel = findSidebarPanel('right')
    expect(leftPanel.classList.contains(styles.mobileSidebarOpen)).toBe(true)
    expect(rightPanel.classList.contains(styles.mobileSidebarOpen)).toBe(false)
    expect(screen.getByTestId('tab-sheet-overlay')).not.toHaveClass(styles.sheetOverlayOpen)
  })

  it('applies the open class to the right sidebar when overlay is right', () => {
    renderMobile({ overlay: 'right' })

    const leftPanel = findSidebarPanel('left')
    const rightPanel = findSidebarPanel('right')
    expect(leftPanel.classList.contains(styles.mobileSidebarOpen)).toBe(false)
    expect(rightPanel.classList.contains(styles.mobileSidebarOpen)).toBe(true)
    expect(screen.getByTestId('tab-sheet-overlay')).not.toHaveClass(styles.sheetOverlayOpen)
  })

  it('renders the sheet scrim inert while closed and active when the sheet is open', () => {
    const closed = renderMobile({})
    expect(screen.getByTestId('tab-sheet-overlay')).not.toHaveClass(styles.sheetOverlayOpen)
    closed.unmount()

    renderMobile({ overlay: 'sheet' })
    expect(screen.getByTestId('tab-sheet-overlay')).toHaveClass(styles.sheetOverlayOpen)
    expect(findSidebarPanel('left').classList.contains(styles.mobileSidebarOpen)).toBe(false)
    expect(findSidebarPanel('right').classList.contains(styles.mobileSidebarOpen)).toBe(false)
  })

  // With the soft keyboard up the bar carries nothing reachable, and the body
  // is pinned to the visible region, so a bar left in place re-seats itself at
  // the top of the screen on every focus and blur.
  it('drops the tab bar from the layout when tabBarHidden is set', () => {
    const shown = renderMobile({})
    expect(tabBarWrapper()).not.toHaveClass(styles.mobileTabBarHidden)
    shown.unmount()

    renderMobile({ tabBarHidden: true })
    expect(tabBarWrapper()).toHaveClass(styles.mobileTabBarHidden)
    // Still rendered, so the bar keeps its state and returns intact on blur.
    expect(screen.getByTestId('tab-bar')).toBeInTheDocument()
  })

  it('a tap on the sheet scrim asks the overlay owner to close the sheet', () => {
    const onCloseSheet = vi.fn()
    renderMobile({ overlay: 'sheet' }, onCloseSheet)

    fireEvent.click(screen.getByTestId('tab-sheet-overlay'))

    expect(onCloseSheet).toHaveBeenCalledOnce()
  })

  // The gesture itself is covered in ~/lib/horizontalSwipe.test.ts. What is
  // this layout's own is WHERE it is armed: the content region, so one gesture
  // reaches both the tiles and the full-bleed drawers over them.
  it('reports a swipe across the tile content', () => {
    const onSwipe = vi.fn()
    renderMobile({ onSwipe })

    swipeAcross(screen.getByTestId('tile-content'), SWIPE_MIN_PX + 40)
    expect(onSwipe).toHaveBeenCalledExactlyOnceWith('right')
  })

  it('reports a swipe across an open drawer', () => {
    const onSwipe = vi.fn()
    renderMobile({ overlay: 'left', onSwipe })

    swipeAcross(screen.getByTestId('sidebar-left'), -SWIPE_MIN_PX - 40)
    expect(onSwipe).toHaveBeenCalledExactlyOnceWith('left')
  })

  // The bar carries the drawer toggles, the tab chip and the tab strip. A
  // finger there is working the bar, not reaching past it for a drawer.
  it('ignores a swipe across the tab bar', () => {
    const onSwipe = vi.fn()
    renderMobile({ onSwipe })

    swipeAcross(screen.getByTestId('tab-bar'), SWIPE_MIN_PX + 40)
    expect(onSwipe).not.toHaveBeenCalled()
  })

  // The gesture holds a NON-PASSIVE touch listener, which the desktop layout
  // must not inherit. A layout that leaked one would keep blocking this
  // region's touches on the main thread after the viewport widened.
  it('drops the gesture when the layout unmounts', () => {
    const onSwipe = vi.fn()
    const rendered = renderMobile({ onSwipe })
    const tiles = screen.getByTestId('tile-content')

    rendered.unmount()
    swipeAcross(tiles, SWIPE_MIN_PX + 40)
    expect(onSwipe).not.toHaveBeenCalled()
  })
})

describe('nextOverlayForSwipe', () => {
  it.each<[MobileOverlay, SwipeDirection, MobileOverlay]>([
    // A clear screen: the swipe pulls in the drawer it moves away from.
    ['none', 'right', 'left'],
    ['none', 'left', 'right'],
    // An open drawer: the swipe that pushes it back out closes it...
    ['left', 'left', 'none'],
    ['right', 'right', 'none'],
    // ...and the swipe that would open it again changes nothing. This is also
    // what stops one flick from closing one drawer and opening its opposite.
    ['left', 'right', 'left'],
    ['right', 'left', 'right'],
    // The sheet covers the swipe region and closes through its own chip.
    ['sheet', 'left', 'sheet'],
    ['sheet', 'right', 'sheet'],
  ])('%s + a swipe %s leaves %s up', (current, direction, expected) => {
    expect(nextOverlayForSwipe(current, direction)).toBe(expected)
  })
})

describe('createMobileOverlayState', () => {
  it('starts with nothing open', () => {
    createRoot((dispose) => {
      const s = createMobileOverlayState()
      expect(s.overlay()).toBe('none')
      dispose()
    })
  })

  it('toggleDrawer opens and closes its own drawer', () => {
    createRoot((dispose) => {
      const s = createMobileOverlayState()
      s.toggleDrawer('left')
      expect(s.overlay()).toBe('left')
      s.toggleDrawer('left')
      expect(s.overlay()).toBe('none')
      dispose()
    })
  })

  it('opening one drawer replaces the other, and the sheet', () => {
    createRoot((dispose) => {
      const s = createMobileOverlayState()
      s.toggleDrawer('left')
      s.toggleDrawer('right')
      expect(s.overlay()).toBe('right')

      s.toggleSheet()
      expect(s.overlay()).toBe('sheet')

      s.toggleDrawer('left')
      expect(s.overlay()).toBe('left')
      dispose()
    })
  })

  it('toggleSheet toggles, and closeSheet closes only the sheet', () => {
    createRoot((dispose) => {
      const s = createMobileOverlayState()
      s.toggleSheet()
      s.toggleSheet()
      expect(s.overlay()).toBe('none')

      // closeSheet must not disturb an open drawer.
      s.toggleDrawer('right')
      s.closeSheet()
      expect(s.overlay()).toBe('right')
      dispose()
    })
  })

  it('applySwipe drives the drawers through the same one owner', () => {
    createRoot((dispose) => {
      const s = createMobileOverlayState()
      s.applySwipe('right')
      expect(s.overlay()).toBe('left')
      s.applySwipe('left')
      expect(s.overlay()).toBe('none')

      s.applySwipe('left')
      expect(s.overlay()).toBe('right')
      // A swipe with nothing to do leaves the state alone rather than clearing it.
      s.applySwipe('left')
      expect(s.overlay()).toBe('right')

      // A swipe under the sheet never disturbs it.
      s.toggleSheet()
      s.applySwipe('right')
      expect(s.overlay()).toBe('sheet')
      dispose()
    })
  })

  it('closeAll closes whatever is up', () => {
    createRoot((dispose) => {
      const s = createMobileOverlayState()
      s.toggleSheet()
      s.closeAll()
      expect(s.overlay()).toBe('none')

      s.toggleDrawer('left')
      s.closeAll()
      expect(s.overlay()).toBe('none')
      dispose()
    })
  })
})
