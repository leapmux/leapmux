import type { Accessor, Component, JSX } from 'solid-js'
import type { Sidebar } from '~/generated/leapmux/v1/section_pb'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createSectionStore } from '~/stores/section.store'
import Resizable from '@corvu/resizable'
import Plus from 'lucide-solid/icons/plus'
import { createSignal, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import * as styles from './AppShell.css'
import { CrossTileDragProvider } from './CrossTileDragContext'
import { SectionDragProvider } from './SectionDragContext'
import { TilingLayout } from './TilingLayout'

interface DesktopLayoutProps {
  sectionStore: ReturnType<typeof createSectionStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  onMoveSection: (sectionId: string, sidebar: Sidebar, position: string) => void
  onMoveSectionServer: (sectionId: string, sidebar: Sidebar, position: string) => void
  activeWorkspaceId: string | null | undefined
  activeWorkspace: () => { id: string } | null
  workspaceLoading: boolean
  getInProgressSectionId: () => string | null
  onNewWorkspace: () => void
  setCenterPanelHeight: (v: number) => void
  // Tiling
  onIntraTileReorder: (tileId: string, fromKey: string, toKey: string) => void
  onCrossTileMove: (fromTileId: string, toTileId: string, draggedTabKey: string, nearTabKey: string | null) => void
  lookupTileIdForTab: (key: string) => string | undefined
  renderDragOverlay: (key: string) => JSX.Element
  renderTile: (tileId: string) => JSX.Element
  onRatioChange: (splitId: string, ratios: number[]) => void
  // Sidebar factories
  createLeftSidebar: (opts: {
    isCollapsed: Accessor<boolean>
    onExpand: () => void
    onCollapse: () => void
    initialOpenSections?: Record<string, boolean>
    initialSectionSizes?: Record<string, number>
    onStateChange?: (open: Record<string, boolean>, sizes: Record<string, number>) => void
  }) => JSX.Element
  createRightSidebar: (opts: {
    isCollapsed: Accessor<boolean>
    onExpand: () => void
    onCollapse: () => void
    initialOpenSections?: Record<string, boolean>
    initialSectionSizes?: Record<string, number>
    onStateChange?: (open: Record<string, boolean>, sizes: Record<string, number>) => void
  }) => JSX.Element
  editorPanel: JSX.Element | false
}

export const DesktopLayout: Component<DesktopLayoutProps> = (props) => {
  // Read saved sidebar state before Resizable mounts (initialSize is read-once).
  // eslint-disable-next-line solid/reactivity -- read-once at mount time, matching original IIFE behavior
  const wsId = props.activeWorkspaceId
  interface SidebarState {
    leftSize?: number
    rightSize?: number
    leftCollapsed?: boolean
    rightCollapsed?: boolean
    leftOpenSections?: Record<string, boolean>
    leftSectionSizes?: Record<string, number>
    rightOpenSections?: Record<string, boolean>
    rightSectionSizes?: Record<string, number>
  }
  const savedSidebar: SidebarState | null = (() => {
    if (!wsId)
      return null
    try {
      return JSON.parse(sessionStorage.getItem(`leapmux:sidebar:${wsId}`) ?? '')
    }
    catch { return null }
  })()
  const initLeft = savedSidebar?.leftSize ?? 0.18
  const initRight = savedSidebar?.rightSize ?? 0.20
  const initCenter = 1 - initLeft - initRight

  let leftOpenSections: Record<string, boolean> = savedSidebar?.leftOpenSections ?? {}
  let leftSectionSizes: Record<string, number> = savedSidebar?.leftSectionSizes ?? {}
  let rightOpenSections: Record<string, boolean> = savedSidebar?.rightOpenSections ?? {}
  let rightSectionSizes: Record<string, number> = savedSidebar?.rightSectionSizes ?? {}

  let saveSidebarRef: (() => void) | undefined

  return (
    <SectionDragProvider
      sections={() => props.sectionStore.state.sections}
      onMoveSection={props.onMoveSection}
      onMoveSectionServer={props.onMoveSectionServer}
    >
      <Resizable orientation="horizontal" class={styles.shell} onSizesChange={() => saveSidebarRef?.()}>
        {() => {
          const ctx = Resizable.useContext()
          const [leftCollapsed, setLeftCollapsed] = createSignal(false)
          const [rightCollapsed, setRightCollapsed] = createSignal(false)
          let leftSizeBeforeCollapse = initLeft
          let rightSizeBeforeCollapse = initRight

          const doSaveSidebarState = () => {
            const id = props.activeWorkspaceId
            if (!id)
              return
            const sizes = ctx.sizes()
            const state: SidebarState = {
              leftSize: leftCollapsed() ? leftSizeBeforeCollapse : sizes[0],
              rightSize: rightCollapsed() ? rightSizeBeforeCollapse : sizes[2],
              leftCollapsed: leftCollapsed(),
              rightCollapsed: rightCollapsed(),
              leftOpenSections,
              leftSectionSizes,
              rightOpenSections,
              rightSectionSizes,
            }
            sessionStorage.setItem(`leapmux:sidebar:${id}`, JSON.stringify(state))
          }
          let sidebarSaveTimer: ReturnType<typeof setTimeout> | null = null
          const saveSidebarState = () => {
            if (sidebarSaveTimer)
              clearTimeout(sidebarSaveTimer)
            sidebarSaveTimer = setTimeout(doSaveSidebarState, 300)
          }
          saveSidebarRef = saveSidebarState

          const collapseLeft = () => {
            leftSizeBeforeCollapse = ctx.sizes()[0] ?? initLeft
            ctx.collapse(0)
          }
          const expandLeft = () => {
            ctx.expand(0)
            ctx.resize(0, leftSizeBeforeCollapse)
          }
          const collapseRight = () => {
            rightSizeBeforeCollapse = ctx.sizes()[2] ?? initRight
            ctx.collapse(2)
          }
          const expandRight = () => {
            ctx.expand(2)
            ctx.resize(2, rightSizeBeforeCollapse)
          }

          if (savedSidebar?.leftCollapsed)
            queueMicrotask(() => collapseLeft())
          if (savedSidebar?.rightCollapsed)
            queueMicrotask(() => collapseRight())

          return (
            <>
              <Resizable.Panel
                initialSize={initLeft}
                minSize={0.10}
                collapsible
                collapsedSize="45px"
                collapseThreshold={0.05}
                class={styles.sidebar}
                onCollapse={() => {
                  setLeftCollapsed(true)
                  saveSidebarState()
                }}
                onExpand={() => {
                  setLeftCollapsed(false)
                  saveSidebarState()
                }}
              >
                {props.createLeftSidebar({
                  isCollapsed: leftCollapsed,
                  onExpand: expandLeft,
                  onCollapse: collapseLeft,
                  initialOpenSections: savedSidebar?.leftOpenSections,
                  initialSectionSizes: savedSidebar?.leftSectionSizes,
                  onStateChange: (open, sizes) => {
                    leftOpenSections = open
                    leftSectionSizes = sizes
                    doSaveSidebarState()
                  },
                })}
              </Resizable.Panel>

              <Resizable.Handle class={styles.resizeHandle} data-testid="resize-handle" />

              <Resizable.Panel
                initialSize={initCenter}
                class={styles.center}
                ref={(el: HTMLElement) => {
                  const observer = new ResizeObserver((entries) => {
                    for (const entry of entries)
                      props.setCenterPanelHeight(entry.contentRect.height)
                  })
                  observer.observe(el)
                }}
              >
                <Show
                  when={props.activeWorkspace() && !props.workspaceLoading}
                  fallback={(
                    <Show when={!props.activeWorkspace() && !props.activeWorkspaceId}>
                      <div class={styles.emptyTileActions} data-testid="no-workspace-empty-state">
                        <button
                          class="outline"
                          data-testid="create-workspace-button"
                          onClick={props.onNewWorkspace}
                        >
                          <Icon icon={Plus} size="sm" />
                          {' '}
                          Create a new workspace...
                        </button>
                      </div>
                    </Show>
                  )}
                >
                  <CrossTileDragProvider
                    onIntraTileReorder={props.onIntraTileReorder}
                    onCrossTileMove={props.onCrossTileMove}
                    lookupTileIdForTab={props.lookupTileIdForTab}
                    renderDragOverlay={props.renderDragOverlay}
                  >
                    <TilingLayout
                      root={props.layoutStore.state.root}
                      renderTile={props.renderTile}
                      onRatioChange={props.onRatioChange}
                    />
                  </CrossTileDragProvider>
                  {props.editorPanel}
                </Show>
              </Resizable.Panel>

              <Resizable.Handle class={styles.resizeHandle} data-testid="resize-handle" />
              <Resizable.Panel
                initialSize={initRight}
                minSize={0.10}
                collapsible
                collapsedSize="45px"
                collapseThreshold={0.05}
                class={styles.rightPanel}
                onCollapse={() => {
                  setRightCollapsed(true)
                  saveSidebarState()
                }}
                onExpand={() => {
                  setRightCollapsed(false)
                  saveSidebarState()
                }}
              >
                {props.createRightSidebar({
                  isCollapsed: rightCollapsed,
                  onExpand: expandRight,
                  onCollapse: collapseRight,
                  initialOpenSections: savedSidebar?.rightOpenSections,
                  initialSectionSizes: savedSidebar?.rightSectionSizes,
                  onStateChange: (open, sizes) => {
                    rightOpenSections = open
                    rightSectionSizes = sizes
                    doSaveSidebarState()
                  },
                })}
              </Resizable.Panel>
            </>
          )
        }}
      </Resizable>
    </SectionDragProvider>
  )
}
