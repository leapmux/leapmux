import type { Section, SectionItem, Sidebar } from '~/generated/leapmux/v1/section_pb'
import { createStore } from 'solid-js/store'
import { SectionType } from '~/generated/leapmux/v1/section_pb'

interface SectionStoreState {
  sections: Section[]
  items: SectionItem[]
  loading: boolean
  error: string | null
}

export function createSectionStore() {
  const [state, setState] = createStore<SectionStoreState>({
    sections: [],
    items: [],
    loading: false,
    error: null,
  })

  return {
    state,

    setSections(sections: Section[]) {
      setState('sections', sections)
    },

    setItems(items: SectionItem[]) {
      setState('items', items)
    },

    setLoading(loading: boolean) {
      setState('loading', loading)
    },

    setError(error: string | null) {
      setState('error', error)
    },

    addSection(section: Section) {
      setState('sections', prev => [...prev, section])
    },

    removeSection(id: string) {
      setState('sections', prev => prev.filter(s => s.id !== id))
      setState('items', prev => prev.filter(i => i.sectionId !== id))
    },

    updateSection(id: string, updates: Partial<Section>) {
      setState('sections', s => s.id === id, updates)
    },

    moveWorkspace(workspaceId: string, sectionId: string, position: string) {
      const existing = state.items.find(i => i.workspaceId === workspaceId)
      if (existing) {
        setState('items', i => i.workspaceId === workspaceId, { sectionId, position })
      }
      else {
        setState('items', prev => [...prev, { workspaceId, sectionId, position } as SectionItem])
      }
    },

    /** Optimistically move a section to a new sidebar + position. */
    moveSection(sectionId: string, sidebar: Sidebar, position: string) {
      setState('sections', s => s.id === sectionId, { sidebar, position })
    },

    /** Get the section ID for a workspace. */
    getSectionForWorkspace(workspaceId: string): string | undefined {
      return state.items.find(i => i.workspaceId === workspaceId)?.sectionId
    },

    /** Get the "In progress" section. */
    getInProgressSection(): Section | undefined {
      return state.sections.find(s => s.sectionType === SectionType.WORKSPACES_IN_PROGRESS)
    },

    /** Get the "Archived" section. */
    getArchivedSection(): Section | undefined {
      return state.sections.find(s => s.sectionType === SectionType.WORKSPACES_ARCHIVED)
    },

    /** Get sections for a specific sidebar, sorted by position. */
    getSectionsForSidebar(sidebar: Sidebar): Section[] {
      return state.sections
        .filter(s => s.sidebar === sidebar)
        .sort((a, b) => a.position.localeCompare(b.position))
    },

    /** Get items for a specific section, sorted by position. */
    getItemsForSection(sectionId: string): SectionItem[] {
      return state.items
        .filter(i => i.sectionId === sectionId)
        .sort((a, b) => a.position.localeCompare(b.position))
    },
  }
}
