import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import * as styles from './AppShell.css'
import { createMobileOverlayState, MobileLayout } from './MobileLayout'

interface RenderOpts {
  leftSidebarOpen?: boolean
  rightSidebarOpen?: boolean
  sheetOpen?: boolean
  tabBarHidden?: boolean
}

function renderMobile(opts: RenderOpts = {}, onCloseSheet: () => void = () => {}) {
  return render(() => (
    <MobileLayout
      leftSidebarOpen={opts.leftSidebarOpen ?? false}
      rightSidebarOpen={opts.rightSidebarOpen ?? false}
      sheetOpen={opts.sheetOpen ?? false}
      tabBarHidden={opts.tabBarHidden ?? false}
      onCloseSheet={onCloseSheet}
      leftSidebarElement={<div data-testid="sidebar-left">left</div>}
      rightSidebarElement={<div data-testid="sidebar-right">right</div>}
      tabBarElement={<div data-testid="tab-bar">tab-bar</div>}
      tileContent={<div data-testid="tile-content">tiles</div>}
      editorPanel={<div data-testid="editor-panel">editor</div>}
    />
  ))
}

/** The wrapper the bar element sits in, which is what carries the hide class. */
function tabBarWrapper(): HTMLElement {
  const wrapper = screen.getByTestId('tab-bar').parentElement
  if (!wrapper)
    throw new Error('the tab bar element has no wrapper')
  return wrapper
}

/** Find the closest ancestor that carries the mobileSidebar class (the panel wrapper). */
function findSidebarPanel(testId: string): HTMLElement {
  const inner = screen.getByTestId(testId)
  let el: HTMLElement | null = inner
  while (el && !el.classList.contains(styles.mobileSidebar)) {
    el = el.parentElement
  }
  if (!el)
    throw new Error(`No mobileSidebar wrapper found around ${testId}`)
  return el
}

describe('mobileLayout', () => {
  it('renders both sidebars closed by default', () => {
    renderMobile({})

    const leftPanel = findSidebarPanel('sidebar-left')
    const rightPanel = findSidebarPanel('sidebar-right')
    expect(leftPanel.classList.contains(styles.mobileSidebarOpen)).toBe(false)
    expect(rightPanel.classList.contains(styles.mobileSidebarOpen)).toBe(false)
  })

  it('applies the open class to the left sidebar when leftSidebarOpen is true', () => {
    renderMobile({ leftSidebarOpen: true })

    const leftPanel = findSidebarPanel('sidebar-left')
    const rightPanel = findSidebarPanel('sidebar-right')
    expect(leftPanel.classList.contains(styles.mobileSidebarOpen)).toBe(true)
    expect(rightPanel.classList.contains(styles.mobileSidebarOpen)).toBe(false)
  })

  it('applies the open class to the right sidebar when rightSidebarOpen is true', () => {
    renderMobile({ rightSidebarOpen: true })

    const leftPanel = findSidebarPanel('sidebar-left')
    const rightPanel = findSidebarPanel('sidebar-right')
    expect(leftPanel.classList.contains(styles.mobileSidebarOpen)).toBe(false)
    expect(rightPanel.classList.contains(styles.mobileSidebarOpen)).toBe(true)
  })

  it('renders the sheet scrim inert while closed and active when the sheet is open', () => {
    const closed = renderMobile({})
    expect(screen.getByTestId('tab-sheet-overlay')).not.toHaveClass(styles.sheetOverlayOpen)
    closed.unmount()

    renderMobile({ sheetOpen: true })
    expect(screen.getByTestId('tab-sheet-overlay')).toHaveClass(styles.sheetOverlayOpen)
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
    renderMobile({ sheetOpen: true }, onCloseSheet)

    fireEvent.click(screen.getByTestId('tab-sheet-overlay'))

    expect(onCloseSheet).toHaveBeenCalledOnce()
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
