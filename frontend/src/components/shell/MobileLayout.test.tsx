import { render, screen } from '@solidjs/testing-library'
import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import * as styles from './AppShell.css'
import { createMobileSidebarToggles, MobileLayout } from './MobileLayout'
import * as SectionDragModule from './SectionDragContext'
import { useTabDrag } from './TabDragContext'

/**
 * Capture the handlers TabDragProvider registers with the section drag
 * context, so a test can drive one drag through the pipeline MobileLayout
 * mounts and land it in the prop MobileLayout was given. Everything else in
 * the module stays real.
 */
vi.mock('~/components/shell/SectionDragContext', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/components/shell/SectionDragContext')>()
  type Handler = (payload: any) => void
  const externalTabDragHandlers = {
    dragStart: [] as Handler[],
    dragOver: [] as Handler[],
    dragEnd: [] as Handler[],
    overlay: [] as Handler[],
  }
  const fakeSectionDrag = {
    addExternalDragStartHandler: (h: Handler) => {
      externalTabDragHandlers.dragStart.push(h)
      return () => {}
    },
    addExternalDragOverHandler: (h: Handler) => {
      externalTabDragHandlers.dragOver.push(h)
      return () => {}
    },
    addExternalDragHandler: (h: Handler) => {
      externalTabDragHandlers.dragEnd.push(h)
      return () => {}
    },
    addExternalOverlayRenderer: (h: Handler) => {
      externalTabDragHandlers.overlay.push(h)
      return () => {}
    },
  }
  return {
    ...actual,
    useOptionalSectionDrag: () => fakeSectionDrag,
    externalTabDragHandlers,
  }
})

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

const stubDragProps = {
  onIntraTileReorder: () => {},
  onCrossTileMove: () => {},
  onCrossWorkspaceMove: () => {},
  lookupTileIdForTab: (_tabKey: string): string | undefined => undefined,
  renderDragOverlay: () => <div />,
}

/** Reports whether a TabDragProvider is an ancestor, via the hook that throws without one. */
function TabDragProbe() {
  let reachable = false
  try {
    useTabDrag()
    reachable = true
  }
  catch { reachable = false }
  return <div data-testid="tab-drag-probe" data-reachable={reachable ? 'true' : 'false'} />
}

interface RenderOpts {
  leftSidebarOpen?: boolean
  rightSidebarOpen?: boolean
  /** Overrides for the drag props MobileLayout forwards to TabDragProvider. */
  drag?: Partial<typeof stubDragProps>
}

function renderMobile(opts: RenderOpts = {}) {
  return render(() => (
    <MobileLayout
      sectionStore={makeStubSectionStore()}
      onMoveSection={() => {}}
      onMoveSectionServer={() => {}}
      leftSidebarOpen={opts.leftSidebarOpen ?? false}
      rightSidebarOpen={opts.rightSidebarOpen ?? false}
      leftSidebarElement={<div data-testid="sidebar-left">left</div>}
      rightSidebarElement={<div data-testid="sidebar-right">right</div>}
      tabBarElement={<div data-testid="tab-bar">tab-bar</div>}
      tileContent={<TabDragProbe />}
      editorPanel={<div data-testid="editor-panel">editor</div>}
      {...stubDragProps}
      {...opts.drag}
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

/** The shell root — the element carrying the mobileShell class. */
function findShellRoot(container: HTMLElement): HTMLElement {
  const el = container.querySelector(`.${styles.mobileShell}`)
  if (!el)
    throw new Error('No mobileShell root found')
  return el as HTMLElement
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

  it('publishes the tab bar height as --mobile-tabbar-h on the shell root', () => {
    // jsdom reports zero geometry, so the value is "0px" — what matters is
    // that the variable is written at all (and driven by the bar's rect).
    const { container } = renderMobile({})
    expect(findShellRoot(container).style.getPropertyValue('--mobile-tabbar-h')).toBe('0px')
  })

  it('mounts a TabDragProvider so tab drags route on mobile too', () => {
    renderMobile({})
    expect(screen.getByTestId('tab-drag-probe')).toHaveAttribute('data-reachable', 'true')
  })

  it('hands its drag handler props to the tab drag pipeline it mounts', () => {
    const onIntraTileReorder = vi.fn()
    const lookupTileIdForTab = vi.fn((): string | undefined => 'tile-1')
    renderMobile({ drag: { onIntraTileReorder, lookupTileIdForTab } })

    // Drive one drag through the handlers the mounted provider registered:
    // same tile for source and target tab keys makes it an intra-tile reorder.
    const registered = (SectionDragModule as unknown as {
      externalTabDragHandlers: {
        dragStart: Array<(payload: any) => void>
        dragEnd: Array<(payload: any) => void>
      }
    }).externalTabDragHandlers
    registered.dragStart.at(-1)!({ draggable: { id: 'agent:a1' } })
    registered.dragEnd.at(-1)!({ draggable: { id: 'agent:a1' }, droppable: { id: 'agent:b1' } })

    expect(lookupTileIdForTab).toHaveBeenCalledWith('agent:a1')
    expect(onIntraTileReorder).toHaveBeenCalledWith('tile-1', 'agent:a1', 'agent:b1')
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
