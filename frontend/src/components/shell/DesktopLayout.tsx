import type { Accessor, Component, JSX } from 'solid-js'
import type { Sidebar } from '~/generated/leapmux/v1/section_pb'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createSectionStore } from '~/stores/section.store'
import Plus from 'lucide-solid/icons/plus'
import { createEffect, createSignal, onCleanup, onMount, Show } from 'solid-js'
import { ChatDropZone } from '~/components/chat/ChatDropZone'
import { Icon } from '~/components/common/Icon'
import { useShortcutContext } from '~/hooks/useShortcutContext'
import { shortcutHint } from '~/lib/shortcuts/display'
import * as styles from './AppShell.css'
import { SectionDragProvider } from './SectionDragContext'
import { TabDragProvider } from './TabDragContext'
import { TilingLayout } from './TilingLayout'

const DEFAULT_SIDEBAR_PX = 250
const MIN_SIDEBAR_PX = 250
const COLLAPSED_SIZE_PX = 45

interface SidebarFactoryOpts {
  isCollapsed: Accessor<boolean>
  onExpand: () => void
  initialOpenSections?: Record<string, boolean>
  initialSectionSizes?: Record<string, number>
  onStateChange?: (open: Record<string, boolean>, sizes: Record<string, number>) => void
}

interface SidebarState {
  leftSize?: number
  rightSize?: number
  leftCollapsed?: boolean
  rightCollapsed?: boolean
  autoCollapsedLeft?: boolean
  autoCollapsedRight?: boolean
  leftOpenSections?: Record<string, boolean>
  leftSectionSizes?: Record<string, number>
  rightOpenSections?: Record<string, boolean>
  rightSectionSizes?: Record<string, number>
}

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
  onCrossWorkspaceMove?: (targetWorkspaceId: string, tabKey: string, sourceWorkspaceId?: string, targetTileId?: string) => void
  lookupTileIdForTab: (key: string) => string | undefined
  renderDragOverlay: (key: string) => JSX.Element
  renderTile: (tileId: string) => JSX.Element
  onRatioChange: (splitId: string, ratios: number[]) => void
  // Sidebar factories
  createLeftSidebar: (opts: SidebarFactoryOpts) => JSX.Element
  createRightSidebar: (opts: SidebarFactoryOpts) => JSX.Element
  editorPanel: JSX.Element | false
  floatingWindowLayer?: JSX.Element
  onFileDrop?: (dataTransfer: DataTransfer, shiftKey: boolean) => void
  fileDropDisabled?: boolean
  setToggleLeftSidebar?: (fn: () => void) => void
  setToggleRightSidebar?: (fn: () => void) => void
  setLeftSidebarVisible?: (visible: boolean) => void
  setRightSidebarVisible?: (visible: boolean) => void
}

function useSidebarDrag(opts: {
  getWidth: () => number
  setWidth: (px: number) => void
  minWidth: number
  direction: 'left' | 'right'
}) {
  const onPointerDown = (e: PointerEvent) => {
    e.preventDefault()
    const handle = e.currentTarget as HTMLElement
    handle.dataset.dragging = ''
    const startX = e.clientX
    const startWidth = opts.getWidth()
    const sign = opts.direction === 'left' ? 1 : -1

    const onPointerMove = (ev: PointerEvent) => {
      const delta = (ev.clientX - startX) * sign
      opts.setWidth(Math.max(opts.minWidth, startWidth + delta))
    }
    const onPointerUp = () => {
      delete handle.dataset.dragging
      document.removeEventListener('pointermove', onPointerMove)
      document.removeEventListener('pointerup', onPointerUp)
      document.removeEventListener('pointercancel', onPointerUp)
      document.body.style.removeProperty('cursor')
      document.body.style.removeProperty('user-select')
    }
    document.addEventListener('pointermove', onPointerMove)
    document.addEventListener('pointerup', onPointerUp)
    document.addEventListener('pointercancel', onPointerUp)
    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
  }
  return onPointerDown
}

export const DesktopLayout: Component<DesktopLayoutProps> = (props) => {
  // Read saved sidebar state (read-once at mount time).
  // eslint-disable-next-line solid/reactivity -- read-once at mount time, matching original IIFE behavior
  const wsId = props.activeWorkspaceId
  const savedSidebar: SidebarState | null = (() => {
    if (!wsId)
      return null
    try {
      return JSON.parse(sessionStorage.getItem(`leapmux:sidebar:${wsId}`) ?? '')
    }
    catch { return null }
  })()

  // Sidebar widths stored as pixels. Clamp to minimum.
  const initLeftPx = Math.max(savedSidebar?.leftSize ?? DEFAULT_SIDEBAR_PX, MIN_SIDEBAR_PX)
  const initRightPx = Math.max(savedSidebar?.rightSize ?? DEFAULT_SIDEBAR_PX, MIN_SIDEBAR_PX)

  let leftOpenSections: Record<string, boolean> = savedSidebar?.leftOpenSections ?? {}
  let leftSectionSizes: Record<string, number> = savedSidebar?.leftSectionSizes ?? {}
  let rightOpenSections: Record<string, boolean> = savedSidebar?.rightOpenSections ?? {}
  let rightSectionSizes: Record<string, number> = savedSidebar?.rightSectionSizes ?? {}

  // Reactive sidebar state.
  const [leftWidth, setLeftWidth] = createSignal(initLeftPx)
  const [rightWidth, setRightWidth] = createSignal(initRightPx)
  const [leftCollapsed, setLeftCollapsed] = createSignal(savedSidebar?.leftCollapsed ?? false)
  const [rightCollapsed, setRightCollapsed] = createSignal(savedSidebar?.rightCollapsed ?? false)
  const [autoCollapsedLeft, setAutoCollapsedLeft] = createSignal(savedSidebar?.autoCollapsedLeft ?? false)
  const [autoCollapsedRight, setAutoCollapsedRight] = createSignal(savedSidebar?.autoCollapsedRight ?? false)

  useShortcutContext('sidebarVisible', () => !leftCollapsed())

  let leftWidthBeforeCollapse = initLeftPx
  let rightWidthBeforeCollapse = initRightPx

  // --- Persistence ---
  const doSaveSidebarState = () => {
    const id = props.activeWorkspaceId
    if (!id)
      return
    const state: SidebarState = {
      leftSize: leftCollapsed() ? leftWidthBeforeCollapse : leftWidth(),
      rightSize: rightCollapsed() ? rightWidthBeforeCollapse : rightWidth(),
      leftCollapsed: leftCollapsed(),
      rightCollapsed: rightCollapsed(),
      autoCollapsedLeft: autoCollapsedLeft(),
      autoCollapsedRight: autoCollapsedRight(),
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

  // --- Collapse / Expand ---
  const collapseLeft = () => {
    leftWidthBeforeCollapse = leftWidth()
    setAutoCollapsedLeft(false)
    setLeftCollapsed(true)
    saveSidebarState()
  }
  const expandLeft = () => {
    setAutoCollapsedLeft(false)
    setLeftCollapsed(false)
    setLeftWidth(leftWidthBeforeCollapse)
    saveSidebarState()
  }

  const toggleLeft = () => {
    if (leftCollapsed())
      expandLeft()
    else
      collapseLeft()
  }
  onMount(() => {
    props.setToggleLeftSidebar?.(toggleLeft)
  })
  createEffect(() => props.setLeftSidebarVisible?.(!leftCollapsed()))

  const collapseRight = () => {
    rightWidthBeforeCollapse = rightWidth()
    setAutoCollapsedRight(false)
    setRightCollapsed(true)
    saveSidebarState()
  }
  const expandRight = () => {
    setAutoCollapsedRight(false)
    setRightCollapsed(false)
    setRightWidth(rightWidthBeforeCollapse)
    saveSidebarState()
  }
  const toggleRight = () => {
    if (rightCollapsed())
      expandRight()
    else
      collapseRight()
  }
  onMount(() => {
    props.setToggleRightSidebar?.(toggleRight)
  })
  createEffect(() => props.setRightSidebarVisible?.(!rightCollapsed()))

  // --- Drag handles ---
  const leftDrag = useSidebarDrag({
    getWidth: leftWidth,
    setWidth: (px) => {
      setLeftWidth(px)
      saveSidebarState()
    },
    minWidth: MIN_SIDEBAR_PX,
    direction: 'left',
  })
  const rightDrag = useSidebarDrag({
    getWidth: rightWidth,
    setWidth: (px) => {
      setRightWidth(px)
      saveSidebarState()
    },
    minWidth: MIN_SIDEBAR_PX,
    direction: 'right',
  })

  // --- Auto-collapse / expand on viewport resize ---
  const applyViewportResize = () => {
    const newWidth = window.innerWidth
    const halfViewport = newWidth / 2

    const leftPx = leftCollapsed() ? 0 : leftWidth()
    const rightPx = rightCollapsed() ? 0 : rightWidth()
    const visibleTotal = leftPx + rightPx

    if (visibleTotal > halfViewport && visibleTotal > 0) {
      if (!leftCollapsed()) {
        leftWidthBeforeCollapse = leftWidth()
        setAutoCollapsedLeft(true)
        setLeftCollapsed(true)
      }
      if (!rightCollapsed()) {
        rightWidthBeforeCollapse = rightWidth()
        setAutoCollapsedRight(true)
        setRightCollapsed(true)
      }
      saveSidebarState()
    }
    else {
      const wantExpandLeft = autoCollapsedLeft() && leftCollapsed()
      const wantExpandRight = autoCollapsedRight() && rightCollapsed()
      if (wantExpandLeft || wantExpandRight) {
        let wouldUse = 0
        if (wantExpandLeft)
          wouldUse += leftWidthBeforeCollapse
        else if (!leftCollapsed())
          wouldUse += leftWidth()
        if (wantExpandRight)
          wouldUse += rightWidthBeforeCollapse
        else if (!rightCollapsed())
          wouldUse += rightWidth()

        if (wouldUse <= halfViewport) {
          if (wantExpandLeft) {
            setAutoCollapsedLeft(false)
            setLeftCollapsed(false)
            setLeftWidth(leftWidthBeforeCollapse)
          }
          if (wantExpandRight) {
            setAutoCollapsedRight(false)
            setRightCollapsed(false)
            setRightWidth(rightWidthBeforeCollapse)
          }
          saveSidebarState()
        }
      }
    }
  }

  let resizeRafId: number | null = null
  const handleViewportResize = () => {
    if (resizeRafId !== null)
      return
    resizeRafId = requestAnimationFrame(() => {
      resizeRafId = null
      applyViewportResize()
    })
  }

  window.addEventListener('resize', handleViewportResize)
  onCleanup(() => {
    window.removeEventListener('resize', handleViewportResize)
    if (resizeRafId !== null)
      cancelAnimationFrame(resizeRafId)
    if (sidebarSaveTimer) {
      clearTimeout(sidebarSaveTimer)
      doSaveSidebarState()
    }
  })

  // Computed widths for CSS.
  const leftPxStyle = () => `${leftCollapsed() ? COLLAPSED_SIZE_PX : leftWidth()}px`
  const rightPxStyle = () => `${rightCollapsed() ? COLLAPSED_SIZE_PX : rightWidth()}px`

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
        <div class={styles.shell} style={{ display: 'flex' }}>
          {/* Left sidebar */}
          <div
            class={styles.sidebar}
            style={{ flex: `0 0 ${leftPxStyle()}` }}
          >
            {props.createLeftSidebar({
              isCollapsed: leftCollapsed,
              onExpand: expandLeft,
              initialOpenSections: savedSidebar?.leftOpenSections,
              initialSectionSizes: savedSidebar?.leftSectionSizes,
              onStateChange: (open, sizes) => {
                leftOpenSections = open
                leftSectionSizes = sizes
                doSaveSidebarState()
              },
            })}
          </div>

          {/* Left resize handle */}
          <div
            class={styles.resizeHandle}
            data-testid="resize-handle"
            onPointerDown={leftDrag}
          />

          {/* Center panel */}
          <div
            class={styles.center}
            style={{ 'flex': '1 1 0px', 'min-width': '0px' }}
            ref={(el) => {
              const observer = new ResizeObserver((entries) => {
                for (const entry of entries)
                  props.setCenterPanelHeight(entry.contentRect.height)
              })
              observer.observe(el)
              onCleanup(() => observer.disconnect())
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
                      title={shortcutHint('Create a new workspace...', 'app.newWorkspaceDialog')}
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
              <ChatDropZone onDrop={props.onFileDrop} disabled={props.fileDropDisabled}>
                <TilingLayout
                  root={props.layoutStore.state.root}
                  renderTile={props.renderTile}
                  onRatioChange={props.onRatioChange}
                />
                {props.editorPanel}
              </ChatDropZone>
            </Show>
          </div>

          {/* Right resize handle */}
          <div
            class={styles.resizeHandle}
            data-testid="resize-handle"
            onPointerDown={rightDrag}
          />

          {/* Right sidebar */}
          <div
            class={styles.rightPanel}
            style={{ flex: `0 0 ${rightPxStyle()}` }}
          >
            {props.createRightSidebar({
              isCollapsed: rightCollapsed,
              onExpand: expandRight,
              initialOpenSections: savedSidebar?.rightOpenSections,
              initialSectionSizes: savedSidebar?.rightSectionSizes,
              onStateChange: (open, sizes) => {
                rightOpenSections = open
                rightSectionSizes = sizes
                doSaveSidebarState()
              },
            })}
          </div>
        </div>
        {props.floatingWindowLayer}
      </TabDragProvider>
    </SectionDragProvider>
  )
}
