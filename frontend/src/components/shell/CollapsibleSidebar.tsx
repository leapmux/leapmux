import type { LucideIcon } from 'lucide-solid'
import type { Component, JSX } from 'solid-js'
import { createDraggable, createDroppable, useDragDropContext } from '@thisbeyond/solid-dnd'
import GripVertical from 'lucide-solid/icons/grip-vertical'
import PanelLeftClose from 'lucide-solid/icons/panel-left-close'
import PanelLeftOpen from 'lucide-solid/icons/panel-left-open'
import PanelRightClose from 'lucide-solid/icons/panel-right-close'
import PanelRightOpen from 'lucide-solid/icons/panel-right-open'
import { createEffect, createMemo, createSignal, For, on, Show } from 'solid-js'
import { IconButton } from '~/components/common/IconButton'
import { iconSize } from '~/styles/tokens'
import * as styles from './CollapsibleSidebar.css'
import { useOptionalSectionDrag } from './SectionDragContext'
import { SECTION_DRAG_PREFIX, SIDEBAR_ZONE_PREFIX } from './sectionDragUtils'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface SidebarSectionDef {
  id: string
  title: string
  /** Icon component for the collapsed rail button. */
  railIcon: LucideIcon
  /** Tooltip for the rail icon button. */
  railTitle?: string
  /** Optional badge below the rail icon (e.g., todo count). */
  railBadge?: () => JSX.Element | undefined
  /**
   * Section body content factory.  Called once when the section first mounts;
   * the returned JSX persists for the lifetime of the section.  Reactive props
   * inside the JSX still update fine-grainedly via SolidJS getters.
   */
  content: () => JSX.Element
  /** Whether section can be collapsed/expanded. Default: true. */
  collapsible?: boolean
  /** Whether the section is currently visible. Default: true. */
  visible?: boolean
  /** Default open state when no persisted state exists. Default: true. */
  defaultOpen?: boolean
  /** Actions rendered in the section header's right side. */
  headerActions?: JSX.Element
  /**
   * Rail-only section: no expandable content panel; only shows in the rail.
   * In the expanded sidebar it renders `content` at the bottom without a collapsible header.
   */
  railOnly?: boolean
  /** Full custom element for the rail (overrides default icon button). */
  railElement?: JSX.Element
  /** Rail position: 'top' (default) or 'bottom'. */
  railPosition?: 'top' | 'bottom'
  /** data-testid for the section details element. */
  testId?: string
  /** Whether the section can be dragged/reordered. Default: false. */
  draggable?: boolean
}

export interface CollapsibleSidebarProps {
  /** Section definitions. */
  sections: SidebarSectionDef[]
  /** Which side the sidebar is on (determines collapse icon direction). */
  side: 'left' | 'right'
  /** Whether the outer Resizable panel is collapsed (shows rail). */
  isCollapsed: boolean
  /** Expand the outer Resizable panel. */
  onExpand: () => void
  /** Collapse the outer Resizable panel. */
  onCollapse?: () => void
  /** Initial open/closed state per section. Read once on mount. */
  initialOpenSections?: Record<string, boolean>
  /** Initial per-section sizes (fractions). Read once on mount. */
  initialSectionSizes?: Record<string, number>
  /** Called whenever open sections or section sizes change. */
  onStateChange?: (openSections: Record<string, boolean>, sectionSizes: Record<string, number>) => void
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export const CollapsibleSidebar: Component<CollapsibleSidebarProps> = (props) => {
  // Build initial open state: start from provided initial state (preserves
  // state for async-loaded sections), then overlay defaults for sections that
  // are already present but have no saved preference.
  const initialOpen: Record<string, boolean> = { ...(props.initialOpenSections ?? {}) }
  // eslint-disable-next-line solid/reactivity -- read once for initialization
  for (const s of props.sections) {
    if (s.railOnly)
      continue
    if (!(s.id in initialOpen))
      initialOpen[s.id] = s.defaultOpen ?? true
  }

  const [openSections, setOpenSections] = createSignal<Record<string, boolean>>(initialOpen)
  const [sectionSizes, setSectionSizes] = createSignal<Record<string, number>>(
    props.initialSectionSizes ?? {},
  )
  const [draggingHandleIndex, setDraggingHandleIndex] = createSignal<number | null>(null)
  let containerRef: HTMLDivElement | undefined

  const notifyStateChange = () => {
    props.onStateChange?.(openSections(), sectionSizes())
  }

  /** Quick lookup for the latest section definition by ID. */
  const sectionById = createMemo(() => {
    const map = new Map<string, SidebarSectionDef>()
    for (const s of props.sections) map.set(s.id, s)
    return map
  })

  // Fall back to the section's defaultOpen preference (instead of always true)
  // so that sections like Archived render correctly before the effect fires.
  const isOpen = (id: string) => {
    const state = openSections()[id]
    if (state !== undefined)
      return state
    const section = sectionById().get(id)
    return section?.defaultOpen ?? true
  }

  // Visible, non-railOnly sections that participate in the collapsible layout
  const expandableSections = () =>
    props.sections.filter(s => s.visible !== false && !s.railOnly)

  // ---------------------------------------------------------------------------
  // Stable ID lists for <For> iteration.
  //
  // SolidJS's <For> compares items by strict equality (===).  Strings with the
  // same value are ===, so iterating over ID strings keeps <For> callbacks
  // stable across reactive re-evaluations — DOM is preserved, DnD primitives
  // inside content() are created once and never orphaned.
  // ---------------------------------------------------------------------------

  const expandableSectionIds = createMemo(() =>
    props.sections.filter(s => s.visible !== false && !s.railOnly).map(s => s.id),
  )

  const railOnlySectionIds = createMemo(() =>
    props.sections.filter(s => s.visible !== false && s.railOnly).map(s => s.id),
  )

  // When visible sections change, ensure at least one is open (enforceOneOpen)
  // and open newly-visible sections.
  createEffect(on(
    () => expandableSections().map(s => s.id).join(','),
    (currentIds, prevIds) => {
      if (prevIds === undefined)
        return // Skip initial run
      const currentSet = new Set(currentIds.split(',').filter(Boolean))
      const prevSet = new Set((prevIds ?? '').split(',').filter(Boolean))

      // Populate state for newly visible sections, respecting saved preferences
      // and defaultOpen.  Only sections without an existing preference are added.
      const newlyVisible = [...currentSet].filter(id => !prevSet.has(id))
      if (newlyVisible.length > 0) {
        setOpenSections((prev) => {
          const next = { ...prev }
          let changed = false
          for (const id of newlyVisible) {
            if (!(id in next)) {
              const section = sectionById().get(id)
              next[id] = section?.defaultOpen ?? true
              changed = true
            }
          }
          return changed ? next : prev
        })

        // Redistribute sizes equally so new sections get a fair share.
        // Without this, normalization in expandedSizes would squeeze
        // the new section because existing sections keep their old
        // absolute sizes.
        const expanded = expandableSectionIds().filter(sid => isOpen(sid))
        if (expanded.length >= 2) {
          const equalSize = 1 / expanded.length
          setSectionSizes((prev) => {
            const next = { ...prev }
            for (const eid of expanded) next[eid] = equalSize
            return next
          })
        }

        notifyStateChange()
      }

      // Ensure at least one section is always open, but only when sections
      // were removed — not when new sections appear from async loading.
      // This preserves the user's saved collapsed preference on page reload.
      const removedSections = [...prevSet].filter(id => !currentSet.has(id))
      if (removedSections.length > 0) {
        const ids = expandableSectionIds()
        const anyOpen = ids.some(id => isOpen(id))
        if (!anyOpen && ids.length > 0) {
          setOpenSections(prev => ({ ...prev, [ids[0]]: true }))
          notifyStateChange()
        }
      }
    },
  ))

  // ---------------------------------------------------------------------------
  // Toggle logic
  // ---------------------------------------------------------------------------

  const handleToggle = (sectionId: string, collapsible: boolean | undefined, open: boolean) => {
    if (collapsible === false)
      return

    // Enforce at least one section always stays open.
    if (!open) {
      const ids = expandableSectionIds()

      // Only one section — prevent collapsing entirely.
      if (ids.length <= 1)
        return

      // If this is the last open section, expand the adjacent one.
      const othersOpen = ids.some(id => id !== sectionId && isOpen(id))
      if (!othersOpen) {
        const currentIdx = ids.indexOf(sectionId)
        const adjacentId = ids[currentIdx > 0 ? currentIdx - 1 : currentIdx + 1]
        if (adjacentId) {
          setOpenSections(prev => ({ ...prev, [sectionId]: false, [adjacentId]: true }))
          notifyStateChange()
          return
        }
      }
    }

    setOpenSections(prev => ({ ...prev, [sectionId]: open }))
    notifyStateChange()
  }

  // ---------------------------------------------------------------------------
  // Resize handle between panes (N-section support)
  // ---------------------------------------------------------------------------

  // Count how many expandable sections are currently expanded
  const expandedCount = () => {
    let count = 0
    for (const id of expandableSectionIds()) {
      if (isOpen(id))
        count++
    }
    return count
  }

  // Compute normalized fractional sizes for currently expanded sections
  const expandedSizes = createMemo(() => {
    const expandedIds = expandableSectionIds().filter(sid => isOpen(sid))
    if (expandedIds.length <= 1)
      return new Map<string, number>()

    const sizes = sectionSizes()
    const result = new Map<string, number>()
    let total = 0

    for (const id of expandedIds) {
      const size = sizes[id] ?? (1 / expandedIds.length)
      result.set(id, size)
      total += size
    }

    // Normalize to sum to 1.0
    if (total > 0 && Math.abs(total - 1) > 0.001) {
      for (const [id, size] of result) {
        result.set(id, size / total)
      }
    }

    return result
  })

  const MIN_FRACTION = 0.15

  /**
   * Start dragging a resize handle between two adjacent expanded sections.
   * handleIndex: 0-based index among expanded sections — handle sits between
   * expandedIds[handleIndex] and expandedIds[handleIndex + 1].
   */
  const handleResizeStart = (handleIndex: number, e: MouseEvent) => {
    e.preventDefault()
    if (!containerRef)
      return
    setDraggingHandleIndex(handleIndex)
    document.body.style.cursor = 'row-resize'

    const expandedIds = expandableSectionIds().filter(sid => isOpen(sid))
    const currentSizes = expandedSizes()

    const aboveId = expandedIds[handleIndex]
    const belowId = expandedIds[handleIndex + 1]
    const aboveSize = currentSizes.get(aboveId) ?? 0
    const belowSize = currentSizes.get(belowId) ?? 0
    const pairTotal = aboveSize + belowSize

    const startY = e.clientY
    const startAboveSize = aboveSize
    const containerHeight = containerRef.getBoundingClientRect().height

    const onMouseMove = (moveEvent: MouseEvent) => {
      const deltaY = moveEvent.clientY - startY
      const deltaFraction = deltaY / containerHeight

      const newAboveSize = Math.max(
        MIN_FRACTION * pairTotal,
        Math.min(
          (1 - MIN_FRACTION) * pairTotal,
          startAboveSize + deltaFraction,
        ),
      )
      const newBelowSize = pairTotal - newAboveSize

      setSectionSizes(prev => ({
        ...prev,
        [aboveId]: newAboveSize,
        [belowId]: newBelowSize,
      }))
    }

    const onMouseUp = () => {
      setDraggingHandleIndex(null)
      document.body.style.cursor = ''
      document.removeEventListener('mousemove', onMouseMove)
      document.removeEventListener('mouseup', onMouseUp)
      notifyStateChange()
    }

    document.addEventListener('mousemove', onMouseMove)
    document.addEventListener('mouseup', onMouseUp)
  }

  /** Reset all expanded sections to equal sizes. */
  const handleResetSplit = () => {
    const expandedIds = expandableSectionIds().filter(sid => isOpen(sid))
    if (expandedIds.length < 2)
      return
    const equalSize = 1 / expandedIds.length
    setSectionSizes((prev) => {
      const next = { ...prev }
      for (const eid of expandedIds)
        next[eid] = equalSize
      return next
    })
    notifyStateChange()
  }

  // ---------------------------------------------------------------------------
  // Collapse icon (stable — side doesn't change during component lifetime)
  // ---------------------------------------------------------------------------

  // eslint-disable-next-line solid/reactivity -- side is stable for the component lifetime
  const CollapseIcon = props.side === 'left' ? PanelLeftClose : PanelRightClose

  // ---------------------------------------------------------------------------
  // Rail (collapsed) rendering
  // ---------------------------------------------------------------------------

  const renderRail = () => {
    const ExpandIcon = props.side === 'left' ? PanelLeftOpen : PanelRightOpen
    const railVariant = props.side === 'left' ? styles.sidebarRailLeft : styles.sidebarRailRight

    const topSections = props.sections.filter(
      s => s.visible !== false && (s.railPosition ?? 'top') === 'top' && !s.railOnly,
    )
    const bottomSections = props.sections.filter(
      s => s.visible !== false && s.railPosition === 'bottom',
    )
    const railOnlyTop = props.sections.filter(
      s => s.visible !== false && s.railOnly && (s.railPosition ?? 'top') === 'top',
    )

    return (
      <div class={`${styles.sidebarRail} ${railVariant}`}>
        {/* Expand button */}
        <IconButton icon={ExpandIcon} iconSize={iconSize.lg} size="lg" title={`Expand ${props.side} sidebar`} onClick={() => props.onExpand()} />

        {/* Top-positioned section icons + badges */}
        <For each={topSections}>
          {section => (
            <>
              <IconButton
                icon={section.railIcon}
                iconSize={iconSize.lg}
                size="lg"
                title={section.railTitle ?? section.title}
                onClick={() => {
                  setOpenSections(prev => ({ ...prev, [section.id]: true }))
                  notifyStateChange()
                  props.onExpand()
                }}
              />
              <Show when={section.railBadge}>
                {section.railBadge?.()}
              </Show>
            </>
          )}
        </For>

        {/* Rail-only top sections */}
        <For each={railOnlyTop}>
          {section => (
            <Show
              when={section.railElement}
              fallback={<IconButton icon={section.railIcon} iconSize={iconSize.lg} size="lg" title={section.railTitle ?? section.title} />}
            >
              {section.railElement}
            </Show>
          )}
        </For>

        {/* Bottom-positioned sections (pushed to bottom) */}
        <Show when={bottomSections.length > 0}>
          <div class={styles.marginTopAuto}>
            <For each={bottomSections}>
              {section => (
                <Show
                  when={section.railElement}
                  fallback={(
                    <IconButton
                      icon={section.railIcon}
                      iconSize={iconSize.lg}
                      size="lg"
                      title={section.railTitle ?? section.title}
                      onClick={() => {
                        if (!section.railOnly) {
                          setOpenSections(prev => ({ ...prev, [section.id]: true }))
                          notifyStateChange()
                        }
                        props.onExpand()
                      }}
                    />
                  )}
                >
                  {section.railElement}
                </Show>
              )}
            </For>
          </div>
        </Show>
      </div>
    )
  }

  // ---------------------------------------------------------------------------
  // Main render
  // ---------------------------------------------------------------------------

  // Check if we're inside a DragDropProvider (true in real app, false in unit tests).
  // This is safer than checking props.sections for draggable flags, since sections
  // may load asynchronously after the component mounts.
  const hasDndContext = useDragDropContext() !== null

  // Access section drag context for drop indicator (non-throwing: safe in tests).
  const sectionDrag = useOptionalSectionDrag()
  const currentDropIndicator = () => sectionDrag?.dropIndicator() ?? null

  // Create a droppable zone for the whole sidebar (for cross-sidebar drops).

  const sidebarDroppable = hasDndContext
    ? createDroppable(`${SIDEBAR_ZONE_PREFIX}${props.side}`) // eslint-disable-line solid/reactivity -- side is stable for the component lifetime
    : null

  return (
    <Show when={!props.isCollapsed} fallback={renderRail()}>
      <div
        class={styles.sidebarInner}
        data-testid={`sidebar-${props.side}`}
        ref={(el) => {
          containerRef = el
          // Attach the droppable ref to the container
          if (sidebarDroppable)
            (sidebarDroppable as any).ref(el)
        }}
      >
        {/*
          Iterate over section IDs (stable strings) so that <For> callbacks
          persist across reactive updates.  Content factories are called once
          per section mount, creating DnD primitives in a stable owner scope.
        */}
        <For each={expandableSectionIds()}>
          {(id, index) => {
            const section = () => sectionById().get(id)!
            // Content is rendered once per section mount. Reactive props inside
            // the returned JSX update fine-grainedly via SolidJS prop getters.
            const renderedContent = section().content()

            const sectionOpen = () => isOpen(id)
            const isStatic = () => section().collapsible === false
            const isDraggable = () => section().draggable === true

            // Create draggable + droppable for the section header (used for
            // DnD reordering).  We use createDraggable + createDroppable
            // instead of createSortable to avoid requiring a SortableProvider
            // ancestor (reorder position logic lives in SectionDragProvider).
            // Only created when inside a DragDropProvider and section is draggable.

            const draggable = hasDndContext && section().draggable
              ? createDraggable(`${SECTION_DRAG_PREFIX}${id}`)
              : null

            const droppable = hasDndContext && section().draggable
              ? createDroppable(`${SECTION_DRAG_PREFIX}${id}`)
              : null

            // Whether this section can currently be collapsed.
            // False when marked non-collapsible OR when it's the only section.
            // When it's the last open section, handleToggle swaps to an adjacent
            // section so we still allow collapsing.
            const canCollapse = () => {
              if (isStatic())
                return false
              const ids = expandableSectionIds()
              if (ids.length <= 1)
                return false
              return true
            }

            // Show the resize handle on the first section whose previous
            // section is expanded, as long as at least one expanded section
            // exists at or after this index.  This places the handle right
            // after expanded content — even when the current section is
            // collapsed — avoiding an unnatural gap.
            const showResizeHandle = () => {
              if (index() === 0 || expandedCount() < 2)
                return false
              const ids = expandableSectionIds()
              if (!isOpen(ids[index() - 1]))
                return false
              return ids.slice(index()).some(sid => isOpen(sid))
            }

            const flexStyle = () => {
              if (expandedCount() < 2 || !sectionOpen())
                return undefined
              const size = expandedSizes().get(id)
              if (size !== undefined)
                return { flex: `${size} 0 0px` }
              return undefined
            }

            // Compute which handle index this is (position among expanded
            // sections).  Find the last expanded section *before* this one
            // and return its index in the expanded list — that identifies
            // the pair being resized.
            const handleIdx = () => {
              const ids = expandableSectionIds()
              const expandedIds = ids.filter(sid => isOpen(sid))
              for (let i = index() - 1; i >= 0; i--) {
                if (isOpen(ids[i]))
                  return expandedIds.indexOf(ids[i])
              }
              return -1
            }

            // Use a ref + createEffect to fully control the <details> open
            // state.  This prevents the browser's session-history restoration
            // from overriding our persisted collapsed preferences on reload.
            let detailsRef!: HTMLDetailsElement
            createEffect(() => {
              if (detailsRef)
                detailsRef.open = sectionOpen()
            })

            return (
              <>
                {/* Resize handle between expanded panes */}
                <Show when={showResizeHandle()}>
                  <div
                    class={`${styles.paneResizeHandle} ${draggingHandleIndex() === handleIdx() ? styles.paneResizeHandleActive : ''}`}
                    data-testid="pane-resize-handle"
                    onMouseDown={(e: MouseEvent) => handleResizeStart(handleIdx(), e)}
                    onDblClick={handleResetSplit}
                  />
                </Show>

                {/* Drop indicator: before this section */}
                <Show when={currentDropIndicator()?.targetSectionId === id && currentDropIndicator()?.position === 'before'}>
                  <div class={styles.dropIndicatorLine} data-testid="drop-indicator" />
                </Show>

                <details
                  ref={(el) => {
                    detailsRef = el
                    // Attach draggable + droppable refs for DnD
                    if (draggable)
                      draggable.ref(el)
                    if (droppable)
                      (droppable as any).ref(el)
                  }}
                  class={`${styles.collapsiblePane} ${sectionOpen() ? styles.collapsiblePaneExpanded : ''} ${draggable?.isActiveDraggable ? styles.collapsiblePaneDragging : ''}`}
                  style={flexStyle()}
                  data-testid={section().testId}
                >
                  <summary
                    class={`${styles.collapsibleTrigger} ${isStatic() || !canCollapse() ? styles.collapsibleTriggerStatic : ''} ${index() === 0 && props.side === 'right' ? styles.collapsibleTriggerNoChevron : ''}`}
                    data-testid={section().testId ? `${section().testId}-summary` : undefined}
                    onClick={(e) => {
                      // Prevent native <details> toggle — we control state
                      // entirely via signals to avoid browser auto-restoration
                      // issues on page reload.
                      e.preventDefault()
                      if (sectionOpen() && !canCollapse())
                        return
                      handleToggle(id, section().collapsible, !sectionOpen())
                    }}
                  >
                    <Show when={isDraggable()}>
                      <div
                        class={styles.sectionDragHandle}
                        data-testid={`section-drag-handle-${id}`}
                        onMouseDown={(e) => {
                          // Prevent the summary click from toggling open/close
                          e.stopPropagation()
                          e.preventDefault()
                        }}
                        // Use the draggable's activators for the drag handle only
                        {...(draggable?.dragActivators ?? {})}
                      >
                        <GripVertical size={12} />
                      </div>
                    </Show>
                    <Show when={index() === 0 && props.onCollapse && props.side === 'left'}>
                      <IconButton
                        icon={CollapseIcon}
                        iconSize={iconSize.lg}
                        size="md"
                        style={{ 'margin-left': '-6px' }}
                        title="Collapse sidebar"
                        onClick={(e) => {
                          e.stopPropagation()
                          e.preventDefault()
                          props.onCollapse?.()
                        }}
                      />
                    </Show>
                    <span class={styles.sidebarTitle}>{section().title}</span>
                    <div class={styles.sidebarHeaderActions}>
                      {section().headerActions}
                    </div>
                    <Show when={index() === 0 && props.onCollapse && props.side === 'right'}>
                      <IconButton
                        icon={CollapseIcon}
                        iconSize={iconSize.lg}
                        size="md"
                        style={{ 'margin-right': '-6px' }}
                        title="Collapse sidebar"
                        onClick={(e) => {
                          e.stopPropagation()
                          e.preventDefault()
                          props.onCollapse?.()
                        }}
                      />
                    </Show>
                  </summary>
                  <div class={styles.collapsibleContent}>
                    <div class={styles.sidebarContent}>
                      {renderedContent}
                    </div>
                  </div>
                </details>

                {/* Drop indicator: after this section */}
                <Show when={currentDropIndicator()?.targetSectionId === id && currentDropIndicator()?.position === 'after'}>
                  <div class={styles.dropIndicatorLine} data-testid="drop-indicator" />
                </Show>
              </>
            )
          }}
        </For>

        {/* Drop indicator for sidebar zone (empty sidebar or append at end) */}
        <Show when={currentDropIndicator()?.targetSectionId === `__zone_${props.side}__`}>
          <div class={styles.dropIndicatorLine} data-testid="drop-indicator" />
        </Show>

        {/* Empty drop zone shown when sidebar has no sections */}
        <Show when={expandableSectionIds().length === 0 && hasDndContext}>
          <div class={`${styles.emptyDropZone} ${sectionDrag?.draggedSectionId() ? styles.emptyDropZoneActive : ''}`} data-testid={`empty-drop-zone-${props.side}`}>
            <Show
              when={sectionDrag?.draggedSectionId()}
              fallback={<span>No sections</span>}
            >
              <span>Drop section here</span>
            </Show>
          </div>
        </Show>

        {/* Rail-only sections rendered at the bottom of the expanded sidebar */}
        <For each={railOnlySectionIds()}>
          {(id) => {
            const renderedContent = sectionById().get(id)!.content()
            return (
              <div class={styles.bottomSection}>
                {renderedContent}
              </div>
            )
          }}
        </For>
      </div>
    </Show>
  )
}
