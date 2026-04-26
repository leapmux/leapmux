import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import * as styles from './AppShell.css'
import { createMobileSidebarToggles, MobileLayout } from './MobileLayout'

/**
 * Build a minimal sectionStore-shaped object — MobileLayout only needs
 * `state.sections` from it (passed through to SectionDragProvider, which
 * shouldn't fire any drag interactions in these tests).
 */
function makeStubSectionStore() {
  return {
    state: { sections: [] },
  } as unknown as Parameters<typeof MobileLayout>[0]['sectionStore']
}

interface RenderOpts {
  leftSidebarOpen?: boolean
  rightSidebarOpen?: boolean
  closeAllSidebars?: () => void
}

function renderMobile(opts: RenderOpts = {}) {
  return render(() => (
    <MobileLayout
      sectionStore={makeStubSectionStore()}
      onMoveSection={() => {}}
      onMoveSectionServer={() => {}}
      leftSidebarOpen={opts.leftSidebarOpen ?? false}
      rightSidebarOpen={opts.rightSidebarOpen ?? false}
      closeAllSidebars={opts.closeAllSidebars ?? (() => {})}
      leftSidebarElement={<div data-testid="sidebar-left">left</div>}
      rightSidebarElement={<div data-testid="sidebar-right">right</div>}
      tabBarElement={<div data-testid="tab-bar">tab-bar</div>}
      tileContent={<div data-testid="tile-content">tiles</div>}
      editorPanel={<div data-testid="editor-panel">editor</div>}
    />
  ))
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

  it('does not render the overlay when both sidebars are closed', () => {
    const { container } = renderMobile({})
    expect(container.querySelector(`.${styles.mobileOverlay}`)).toBeNull()
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

  it('renders the overlay when either sidebar is open and removes it when both close', () => {
    const [leftOpen, setLeftOpen] = createSignal(true)
    const [rightOpen, setRightOpen] = createSignal(false)
    const { container } = render(() => (
      <MobileLayout
        sectionStore={makeStubSectionStore()}
        onMoveSection={() => {}}
        onMoveSectionServer={() => {}}
        leftSidebarOpen={leftOpen()}
        rightSidebarOpen={rightOpen()}
        closeAllSidebars={() => {}}
        leftSidebarElement={<div data-testid="sidebar-left">left</div>}
        rightSidebarElement={<div data-testid="sidebar-right">right</div>}
        tabBarElement={<div data-testid="tab-bar">tab-bar</div>}
        tileContent={<div data-testid="tile-content">tiles</div>}
        editorPanel={<div data-testid="editor-panel">editor</div>}
      />
    ))

    expect(container.querySelector(`.${styles.mobileOverlay}`)).not.toBeNull()

    setLeftOpen(false)
    setRightOpen(true)
    expect(container.querySelector(`.${styles.mobileOverlay}`)).not.toBeNull()

    setLeftOpen(false)
    setRightOpen(false)
    expect(container.querySelector(`.${styles.mobileOverlay}`)).toBeNull()
  })

  it('invokes closeAllSidebars when the overlay is clicked', () => {
    const closeAllSidebars = vi.fn()
    const { container } = renderMobile({ leftSidebarOpen: true, closeAllSidebars })

    const overlay = container.querySelector(`.${styles.mobileOverlay}`)
    expect(overlay).not.toBeNull()
    fireEvent.click(overlay!)

    expect(closeAllSidebars).toHaveBeenCalledTimes(1)
  })
})

describe('createMobileSidebarToggles', () => {
  it('starts with both sidebars closed', () => {
    createRoot((dispose) => {
      const t = createMobileSidebarToggles()
      expect(t.leftSidebarOpen()).toBe(false)
      expect(t.rightSidebarOpen()).toBe(false)
      dispose()
    })
  })

  it('toggleLeftSidebar opens the left sidebar', () => {
    createRoot((dispose) => {
      const t = createMobileSidebarToggles()
      t.toggleLeftSidebar()
      expect(t.leftSidebarOpen()).toBe(true)
      expect(t.rightSidebarOpen()).toBe(false)
      dispose()
    })
  })

  it('toggleLeftSidebar closes the left sidebar when already open', () => {
    createRoot((dispose) => {
      const t = createMobileSidebarToggles()
      t.toggleLeftSidebar()
      t.toggleLeftSidebar()
      expect(t.leftSidebarOpen()).toBe(false)
      dispose()
    })
  })

  it('toggleRightSidebar opens the right sidebar and closes the left', () => {
    createRoot((dispose) => {
      const t = createMobileSidebarToggles()
      t.toggleLeftSidebar()
      expect(t.leftSidebarOpen()).toBe(true)

      t.toggleRightSidebar()
      expect(t.rightSidebarOpen()).toBe(true)
      expect(t.leftSidebarOpen()).toBe(false)
      dispose()
    })
  })

  it('toggleLeftSidebar closes the right sidebar when opening the left', () => {
    createRoot((dispose) => {
      const t = createMobileSidebarToggles()
      t.toggleRightSidebar()
      expect(t.rightSidebarOpen()).toBe(true)

      t.toggleLeftSidebar()
      expect(t.leftSidebarOpen()).toBe(true)
      expect(t.rightSidebarOpen()).toBe(false)
      dispose()
    })
  })

  it('closeAllSidebars closes both sidebars', () => {
    createRoot((dispose) => {
      const t = createMobileSidebarToggles()
      t.toggleLeftSidebar()
      t.closeAllSidebars()
      expect(t.leftSidebarOpen()).toBe(false)
      expect(t.rightSidebarOpen()).toBe(false)

      t.toggleRightSidebar()
      t.closeAllSidebars()
      expect(t.leftSidebarOpen()).toBe(false)
      expect(t.rightSidebarOpen()).toBe(false)
      dispose()
    })
  })
})
