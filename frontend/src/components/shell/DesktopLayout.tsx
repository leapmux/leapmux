import type { Accessor, Component, JSX } from 'solid-js'
import type { Sidebar } from '~/generated/leapmux/v1/section_pb'
import type { createLayoutStore, GridAxis } from '~/stores/layout.store'
import type { createSectionStore } from '~/stores/section.store'
import Plus from 'lucide-solid/icons/plus'
import { createEffect, createSignal, onCleanup, onMount, Show } from 'solid-js'
import { ChatDropZone } from '~/components/chat/ChatDropZone'
import { Icon } from '~/components/common/Icon'
import { useShortcutContext } from '~/hooks/useShortcutContext'
import { PREFIX_SIDEBAR, sessionStorageGet, sessionStorageSet } from '~/lib/browserStorage'
import { trailingDebounce } from '~/lib/debounce'
import { createRafResizeObserver } from '~/lib/resizeObserver'
import { getShortcutHintsText } from '~/lib/shortcuts/display'
import * as styles from './AppShell.css'
import { SectionDragProvider } from './SectionDragContext'
import { TabDragProvider } from './TabDragContext'
import { TilingLayout } from './TilingLayout'
import { useWindowPointerDrag } from './windowPointerDrag'

const DEFAULT_SIDEBAR_PX = 250
const MIN_SIDEBAR_PX = 250
const COLLAPSED_SIZE_PX = 45

/** Snapshot of sidebar state needed to compute an auto-collapse / auto-expand decision. */
export interface AutoCollapseInput {
  viewportWidth: number
  leftCollapsed: boolean
  rightCollapsed: boolean
  leftWidth: number
  rightWidth: number
  autoCollapsedLeft: boolean
  autoCollapsedRight: boolean
  leftWidthBeforeCollapse: number
  rightWidthBeforeCollapse: number
}

/**
 * Decision produced by {@link decideAutoCollapse}. Each field is undefined
 * unless that side should be transitioned. `collapseLeft: true` means the
 * caller should collapse the left sidebar (recording the current width as
 * "before collapse" first); `expandLeft.newWidth` means the caller should
 * restore the left sidebar to that width.
 */
export interface AutoCollapseDecision {
  collapseLeft?: true
  collapseRight?: true
  expandLeft?: { newWidth: number }
  expandRight?: { newWidth: number }
}

/**
 * Decide whether to auto-collapse or auto-expand sidebars based on the
 * current viewport. Pure function — does not read the DOM, so it can be
 * unit-tested exhaustively without rendering.
 *
 * Rules (mirroring the original DesktopLayout behavior):
 *
 * - If the visible sidebar pixels exceed half the viewport, auto-collapse
 *   any uncollapsed side (recording its width to be restored later).
 * - Otherwise, if a side was previously *auto*-collapsed and restoring it
 *   would still fit within half the viewport, auto-expand it back to its
 *   pre-collapse width. User-initiated collapses are not auto-expanded.
 */
export function decideAutoCollapse(input: AutoCollapseInput): AutoCollapseDecision {
  const halfViewport = input.viewportWidth / 2
  const leftPx = input.leftCollapsed ? 0 : input.leftWidth
  const rightPx = input.rightCollapsed ? 0 : input.rightWidth
  const visibleTotal = leftPx + rightPx

  if (visibleTotal > halfViewport && visibleTotal > 0) {
    const decision: AutoCollapseDecision = {}
    if (!input.leftCollapsed)
      decision.collapseLeft = true
    if (!input.rightCollapsed)
      decision.collapseRight = true
    return decision
  }

  const wantExpandLeft = input.autoCollapsedLeft && input.leftCollapsed
  const wantExpandRight = input.autoCollapsedRight && input.rightCollapsed
  if (!wantExpandLeft && !wantExpandRight)
    return {}

  let wouldUse = 0
  if (wantExpandLeft)
    wouldUse += input.leftWidthBeforeCollapse
  else if (!input.leftCollapsed)
    wouldUse += input.leftWidth
  if (wantExpandRight)
    wouldUse += input.rightWidthBeforeCollapse
  else if (!input.rightCollapsed)
    wouldUse += input.rightWidth

  if (wouldUse > halfViewport)
    return {}

  const decision: AutoCollapseDecision = {}
  if (wantExpandLeft)
    decision.expandLeft = { newWidth: input.leftWidthBeforeCollapse }
  if (wantExpandRight)
    decision.expandRight = { newWidth: input.rightWidthBeforeCollapse }
  return decision
}

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
  onGridRatiosChange?: (gridId: string, axis: GridAxis, ratios: number[]) => void
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
  const drag = useWindowPointerDrag()
  return (e: PointerEvent) => {
    e.preventDefault()
    const handle = e.currentTarget as HTMLElement
    const startX = e.clientX
    const startWidth = opts.getWidth()
    const sign = opts.direction === 'left' ? 1 : -1
    let claimed = false
    drag.start({
      onMove: (ev) => {
        // Claim the handle/body cursor lazily on first move so a bare click
        // (no movement → no `onUp`) leaves no dataset/cursor/userSelect
        // residue. The helper's `onUp` only fires when at least one move
        // dispatched, so the cleanup symmetry holds.
        if (!claimed) {
          claimed = true
          handle.dataset.dragging = ''
          document.body.style.cursor = 'col-resize'
          document.body.style.userSelect = 'none'
        }
        const delta = (ev.clientX - startX) * sign
        opts.setWidth(Math.max(opts.minWidth, startWidth + delta))
      },
      onUp: () => {
        delete handle.dataset.dragging
        document.body.style.removeProperty('cursor')
        document.body.style.removeProperty('user-select')
      },
    })
  }
}

/**
 * Per-side sidebar state + handlers. Both sidebars need width,
 * collapsed/auto-collapsed flags, collapse/expand/toggle handlers,
 * the pointer-drag binding, and a way to apply an auto-collapse
 * decision from {@link decideAutoCollapse}. Encapsulating one
 * side's state here lets DesktopLayout call this hook twice
 * instead of mirroring every signal/handler per side.
 *
 * `onSave` is invoked whenever side state changes; the caller owns
 * the cross-side serialization (sessionStorage payload includes both
 * sides' fields together) so the hook does not directly touch
 * storage.
 */
function useSidebarSide(opts: {
  side: 'left' | 'right'
  initWidth: number
  initCollapsed: boolean
  initAutoCollapsed: boolean
  minWidth: number
  collapsedSizePx: number
  onSave: () => void
}) {
  const [width, setWidth] = createSignal(opts.initWidth)
  const [collapsed, setCollapsed] = createSignal(opts.initCollapsed)
  const [autoCollapsed, setAutoCollapsed] = createSignal(opts.initAutoCollapsed)
  let widthBeforeCollapse = opts.initWidth

  const collapse = () => {
    widthBeforeCollapse = width()
    setAutoCollapsed(false)
    setCollapsed(true)
    opts.onSave()
  }
  const expand = () => {
    setAutoCollapsed(false)
    setCollapsed(false)
    setWidth(widthBeforeCollapse)
    opts.onSave()
  }
  const toggle = () => (collapsed() ? expand() : collapse())

  const drag = useSidebarDrag({
    getWidth: width,
    setWidth: (px) => {
      setWidth(px)
      opts.onSave()
    },
    minWidth: opts.minWidth,
    direction: opts.side,
  })

  const pxStyle = () => `${collapsed() ? opts.collapsedSizePx : width()}px`

  const applyAutoCollapseDecision = (decision: AutoCollapseDecision) => {
    const wantCollapse = opts.side === 'left' ? decision.collapseLeft : decision.collapseRight
    const wantExpand = opts.side === 'left' ? decision.expandLeft : decision.expandRight
    if (wantCollapse) {
      widthBeforeCollapse = width()
      setAutoCollapsed(true)
      setCollapsed(true)
    }
    if (wantExpand) {
      setAutoCollapsed(false)
      setCollapsed(false)
      setWidth(wantExpand.newWidth)
    }
  }

  return {
    width,
    collapsed,
    autoCollapsed,
    collapse,
    expand,
    toggle,
    drag,
    pxStyle,
    applyAutoCollapseDecision,
    getWidthBeforeCollapse: () => widthBeforeCollapse,
  }
}

export const DesktopLayout: Component<DesktopLayoutProps> = (props) => {
  // Read saved sidebar state (read-once at mount time).
  // eslint-disable-next-line solid/reactivity -- read-once at mount time
  const wsId = props.activeWorkspaceId
  const savedSidebar: SidebarState | null = wsId
    ? sessionStorageGet<SidebarState>(`${PREFIX_SIDEBAR}${wsId}`) ?? null
    : null

  // Sidebar widths stored as pixels. Clamp to minimum.
  const initLeftPx = Math.max(savedSidebar?.leftSize ?? DEFAULT_SIDEBAR_PX, MIN_SIDEBAR_PX)
  const initRightPx = Math.max(savedSidebar?.rightSize ?? DEFAULT_SIDEBAR_PX, MIN_SIDEBAR_PX)

  let leftOpenSections: Record<string, boolean> = savedSidebar?.leftOpenSections ?? {}
  let leftSectionSizes: Record<string, number> = savedSidebar?.leftSectionSizes ?? {}
  let rightOpenSections: Record<string, boolean> = savedSidebar?.rightOpenSections ?? {}
  let rightSectionSizes: Record<string, number> = savedSidebar?.rightSectionSizes ?? {}

  // --- Persistence ---
  // Forward-declare so the side hooks can call into it before their
  // own state is constructed; the closures read at fire time.
  let leftSide!: ReturnType<typeof useSidebarSide>
  let rightSide!: ReturnType<typeof useSidebarSide>
  const doSaveSidebarState = () => {
    const id = props.activeWorkspaceId
    if (!id)
      return
    const state: SidebarState = {
      leftSize: leftSide.collapsed() ? leftSide.getWidthBeforeCollapse() : leftSide.width(),
      rightSize: rightSide.collapsed() ? rightSide.getWidthBeforeCollapse() : rightSide.width(),
      leftCollapsed: leftSide.collapsed(),
      rightCollapsed: rightSide.collapsed(),
      autoCollapsedLeft: leftSide.autoCollapsed(),
      autoCollapsedRight: rightSide.autoCollapsed(),
      leftOpenSections,
      leftSectionSizes,
      rightOpenSections,
      rightSectionSizes,
    }
    sessionStorageSet(`${PREFIX_SIDEBAR}${id}`, state)
  }
  // trailingDebounce reads signals at fire time; the lint can't see through it.
  // eslint-disable-next-line solid/reactivity
  const saveSidebarState = trailingDebounce(doSaveSidebarState, 300)

  leftSide = useSidebarSide({
    side: 'left',
    initWidth: initLeftPx,
    initCollapsed: savedSidebar?.leftCollapsed ?? false,
    initAutoCollapsed: savedSidebar?.autoCollapsedLeft ?? false,
    minWidth: MIN_SIDEBAR_PX,
    collapsedSizePx: COLLAPSED_SIZE_PX,
    onSave: saveSidebarState,
  })
  rightSide = useSidebarSide({
    side: 'right',
    initWidth: initRightPx,
    initCollapsed: savedSidebar?.rightCollapsed ?? false,
    initAutoCollapsed: savedSidebar?.autoCollapsedRight ?? false,
    minWidth: MIN_SIDEBAR_PX,
    collapsedSizePx: COLLAPSED_SIZE_PX,
    onSave: saveSidebarState,
  })

  useShortcutContext('sidebarVisible', () => !leftSide.collapsed())

  onMount(() => {
    props.setToggleLeftSidebar?.(leftSide.toggle)
    props.setToggleRightSidebar?.(rightSide.toggle)
  })
  createEffect(() => props.setLeftSidebarVisible?.(!leftSide.collapsed()))
  createEffect(() => props.setRightSidebarVisible?.(!rightSide.collapsed()))

  // --- Auto-collapse / expand on viewport resize ---
  const applyViewportResize = () => {
    const decision = decideAutoCollapse({
      viewportWidth: window.innerWidth,
      leftCollapsed: leftSide.collapsed(),
      rightCollapsed: rightSide.collapsed(),
      leftWidth: leftSide.width(),
      rightWidth: rightSide.width(),
      autoCollapsedLeft: leftSide.autoCollapsed(),
      autoCollapsedRight: rightSide.autoCollapsed(),
      leftWidthBeforeCollapse: leftSide.getWidthBeforeCollapse(),
      rightWidthBeforeCollapse: rightSide.getWidthBeforeCollapse(),
    })
    if (!decision.collapseLeft && !decision.collapseRight && !decision.expandLeft && !decision.expandRight)
      return
    leftSide.applyAutoCollapseDecision(decision)
    rightSide.applyAutoCollapseDecision(decision)
    saveSidebarState()
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
    saveSidebarState.flush()
  })

  // Computed widths for CSS.
  const leftPxStyle = leftSide.pxStyle
  const rightPxStyle = rightSide.pxStyle

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
              isCollapsed: leftSide.collapsed,
              onExpand: leftSide.expand,
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
            onPointerDown={leftSide.drag}
          />

          {/* Center panel */}
          <div
            class={styles.center}
            style={{ 'flex': '1 1 0px', 'min-width': '0px' }}
            ref={(el) => {
              const observer = createRafResizeObserver((entries) => {
                for (const entry of entries)
                  props.setCenterPanelHeight(entry.contentRect.height)
              })
              observer?.observe(el)
              onCleanup(() => observer?.disconnect())
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
                      <span class={styles.emptyTileActionContent}>
                        <span>Create a new workspace...</span>
                        <Show when={getShortcutHintsText('app.newWorkspaceDialog')}>
                          {shortcut => <span class={styles.emptyTileActionShortcut}>{shortcut()}</span>}
                        </Show>
                      </span>
                    </button>
                  </div>
                </Show>
              )}
            >
              <ChatDropZone onDrop={props.onFileDrop} disabled={props.fileDropDisabled}>
                {/*
                  Key TilingLayout on the active workspace id so the
                  entire tile tree (and all its TabBar instances)
                  re-mounts on every workspace switch. Without this,
                  Solid's <Match>/<For> reuses the prior workspace's
                  tile component and its prop bindings — and although
                  state.root and tabStore are updated by the reconciler,
                  the cached prop accessors (especially the
                  `tabsByTile` memo result handed to TabBar's
                  `<For each={props.tabs}>`) sometimes don't re-evaluate
                  in time. A fresh subtree avoids the race entirely.
                */}
                <Show when={props.activeWorkspaceId} keyed>
                  <TilingLayout
                    root={props.layoutStore.state.root}
                    renderTile={props.renderTile}
                    onRatioChange={props.onRatioChange}
                    onGridRatiosChange={props.onGridRatiosChange}
                  />
                </Show>
                {props.editorPanel}
              </ChatDropZone>
            </Show>
          </div>

          {/* Right resize handle */}
          <div
            class={styles.resizeHandle}
            data-testid="resize-handle"
            onPointerDown={rightSide.drag}
          />

          {/* Right sidebar */}
          <div
            class={styles.rightPanel}
            style={{ flex: `0 0 ${rightPxStyle()}` }}
          >
            {props.createRightSidebar({
              isCollapsed: rightSide.collapsed,
              onExpand: rightSide.expand,
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
