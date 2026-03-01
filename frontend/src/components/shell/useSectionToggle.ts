import type { Accessor, Setter } from 'solid-js'
import type { SidebarSectionDef } from './CollapsibleSidebar'
import { createEffect, createMemo, on } from 'solid-js'

// ---------------------------------------------------------------------------
// Hook
// ---------------------------------------------------------------------------

export interface UseSectionToggleOptions {
  /** Section definitions (reactive). */
  sections: Accessor<SidebarSectionDef[]>
  /** Current open/closed state per section. */
  openSections: Accessor<Record<string, boolean>>
  /** Setter for open/closed state per section. */
  setOpenSections: Setter<Record<string, boolean>>
  /** Setter for per-section fractional sizes. */
  setSectionSizes: Setter<Record<string, number>>
  /** Callback from the parent when open sections or sizes change. */
  onStateChange?: (openSections: Record<string, boolean>, sectionSizes: Record<string, number>) => void
  /** Current per-section fractional sizes. */
  sectionSizes: Accessor<Record<string, number>>
  /** Quick lookup for the latest section definition by ID. */
  sectionById: Accessor<Map<string, SidebarSectionDef>>
  /** Whether a given section is open. */
  isOpen: (id: string) => boolean
}

export interface UseSectionToggleReturn {
  /** IDs of visible, non-railOnly sections (stable for <For> iteration). */
  expandableSectionIds: Accessor<string[]>
  /** IDs of visible, railOnly sections (stable for <For> iteration). */
  railOnlySectionIds: Accessor<string[]>
  /** Notify parent of state changes. */
  notifyStateChange: () => void
  /** Toggle a section open or closed. */
  handleToggle: (sectionId: string, collapsible: boolean | undefined, open: boolean) => void
}

export function useSectionToggle(options: UseSectionToggleOptions): UseSectionToggleReturn {
  const {
    sections,
    openSections,
    setOpenSections,
    setSectionSizes,
    onStateChange,
    sectionSizes,
    sectionById,
    isOpen,
  } = options

  const notifyStateChange = () => {
    onStateChange?.(openSections(), sectionSizes())
  }

  // Visible, non-railOnly sections that participate in the collapsible layout
  const expandableSections = () =>
    sections().filter(s => s.visible !== false && !s.railOnly)

  // -------------------------------------------------------------------------
  // Stable ID lists for <For> iteration.
  //
  // SolidJS's <For> compares items by strict equality (===).  Strings with the
  // same value are ===, so iterating over ID strings keeps <For> callbacks
  // stable across reactive re-evaluations -- DOM is preserved, DnD primitives
  // inside content() are created once and never orphaned.
  // -------------------------------------------------------------------------

  const expandableSectionIds = createMemo(() =>
    sections().filter(s => s.visible !== false && !s.railOnly).map(s => s.id),
  )

  const railOnlySectionIds = createMemo(() =>
    sections().filter(s => s.visible !== false && s.railOnly).map(s => s.id),
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
      // were removed -- not when new sections appear from async loading.
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

  // -------------------------------------------------------------------------
  // Toggle logic
  // -------------------------------------------------------------------------

  const handleToggle = (sectionId: string, collapsible: boolean | undefined, open: boolean) => {
    if (collapsible === false)
      return

    // Enforce at least one section always stays open.
    if (!open) {
      const ids = expandableSectionIds()

      // Only one section -- prevent collapsing entirely.
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

  return {
    expandableSectionIds,
    railOnlySectionIds,
    notifyStateChange,
    handleToggle,
  }
}
