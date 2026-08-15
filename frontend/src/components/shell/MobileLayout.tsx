import type { Component, JSX } from 'solid-js'
import type { Sidebar } from '~/generated/leapmux/v1/section_pb'
import type { createSectionStore } from '~/stores/section.store'
import { createSignal, onCleanup, onMount } from 'solid-js'
import * as styles from './AppShell.css'
import { SectionDragProvider } from './SectionDragContext'
import { TabDragProvider } from './TabDragContext'

/**
 * Mobile sidebar open/close state with mutual exclusion: opening one sidebar
 * always closes the other; toggling a sidebar closes itself or opens it while
 * closing the other.
 */
export function createMobileSidebarToggles() {
  const [leftSidebarOpen, setLeftSidebarOpen] = createSignal(false)
  const [rightSidebarOpen, setRightSidebarOpen] = createSignal(false)
  const toggleLeftSidebar = () => {
    setLeftSidebarOpen(prev => !prev)
    setRightSidebarOpen(false)
  }
  const toggleRightSidebar = () => {
    setRightSidebarOpen(prev => !prev)
    setLeftSidebarOpen(false)
  }
  const closeAllSidebars = () => {
    setLeftSidebarOpen(false)
    setRightSidebarOpen(false)
  }
  return {
    leftSidebarOpen,
    rightSidebarOpen,
    toggleLeftSidebar,
    toggleRightSidebar,
    closeAllSidebars,
  }
}

interface MobileLayoutProps {
  sectionStore: ReturnType<typeof createSectionStore>
  onMoveSection: (sectionId: string, sidebar: Sidebar, position: string) => void
  onMoveSectionServer: (sectionId: string, sidebar: Sidebar, position: string) => void
  leftSidebarOpen: boolean
  rightSidebarOpen: boolean
  leftSidebarElement: JSX.Element
  rightSidebarElement: JSX.Element
  tabBarElement: JSX.Element
  tileContent: JSX.Element
  editorPanel: JSX.Element | false
  /** Tab drag routing — the same bundle `DesktopLayout` feeds its TabDragProvider. */
  onIntraTileReorder: (tileId: string, fromKey: string, toKey: string) => void
  onCrossTileMove: (fromTileId: string, toTileId: string, tabKey: string, nearTabKey: string | null) => void
  onCrossWorkspaceMove?: (targetWorkspaceId: string, tabKey: string, sourceWorkspaceId?: string, targetTileId?: string) => void
  lookupTileIdForTab: (tabKey: string) => string | undefined
  renderDragOverlay: (tabKey: string) => JSX.Element
}

export const MobileLayout: Component<MobileLayoutProps> = (props) => {
  let shellRef: HTMLDivElement | undefined
  let tabBarRef: HTMLDivElement | undefined

  /**
   * Publish the rendered tab bar height as `--mobile-tabbar-h` on the shell
   * root. The drawers start below the bar (`top: calc(env(safe-area-inset-top)
   * + var(--mobile-tabbar-h))`) so their first section header is not covered
   * by the opaque bar — which paints above them (later in DOM at equal
   * z-index) on purpose, to keep its drawer toggles tappable while a drawer
   * is open. Measured rather than hardcoded: the bar is content-driven
   * (~40px with its size-xl buttons today) and has no fixed height of its own.
   */
  const applyTabBarHeight = () => {
    if (!shellRef || !tabBarRef)
      return
    shellRef.style.setProperty('--mobile-tabbar-h', `${Math.round(tabBarRef.getBoundingClientRect().height)}px`)
  }

  onMount(() => {
    applyTabBarHeight()
    if (typeof ResizeObserver === 'undefined')
      return
    const observer = new ResizeObserver(applyTabBarHeight)
    if (tabBarRef)
      observer.observe(tabBarRef)
    onCleanup(() => observer.disconnect())
  })

  return (
    <SectionDragProvider
      sections={() => props.sectionStore.state.sections}
      onMoveSection={props.onMoveSection}
      onMoveSectionServer={props.onMoveSectionServer}
    >
      <TabDragProvider
        onIntraTileReorder={props.onIntraTileReorder}
        onCrossTileMove={props.onCrossTileMove}
        onCrossWorkspaceMove={props.onCrossWorkspaceMove}
        lookupTileIdForTab={props.lookupTileIdForTab}
        renderDragOverlay={props.renderDragOverlay}
      >
        <div ref={(el) => { shellRef = el }} class={styles.mobileShell}>
          <div
            class={styles.mobileSidebar}
            classList={{ [styles.mobileSidebarOpen]: props.leftSidebarOpen }}
          >
            {props.leftSidebarElement}
          </div>

          <div
            class={`${styles.mobileSidebar} ${styles.mobileSidebarRight}`}
            classList={{ [styles.mobileSidebarOpen]: props.rightSidebarOpen }}
          >
            {props.rightSidebarElement}
          </div>

          <div class={styles.mobileCenter}>
            <div ref={(el) => { tabBarRef = el }} class={styles.mobileTabBar}>
              {props.tabBarElement}
            </div>
            <div class={styles.mobileTilePaneSlot}>
              {props.tileContent}
            </div>
            {props.editorPanel}
          </div>
        </div>
      </TabDragProvider>
    </SectionDragProvider>
  )
}
