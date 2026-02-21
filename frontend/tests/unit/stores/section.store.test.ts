import type { Section, SectionItem } from '~/generated/leapmux/v1/section_pb'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { SectionType } from '~/generated/leapmux/v1/section_pb'
import { createSectionStore } from '~/stores/section.store'

function makeSection(id: string, name: string, sectionType: SectionType, position: string): Section {
  return { id, name, sectionType, position } as Section
}

function makeItem(workspaceId: string, sectionId: string, position: string): SectionItem {
  return { workspaceId, sectionId, position } as SectionItem
}

describe('createSectionStore', () => {
  it('should initialize with empty state', () => {
    createRoot((dispose) => {
      const store = createSectionStore()
      expect(store.state.sections).toEqual([])
      expect(store.state.items).toEqual([])
      expect(store.state.loading).toBe(false)
      expect(store.state.error).toBeNull()
      dispose()
    })
  })

  it('should set loading', () => {
    createRoot((dispose) => {
      const store = createSectionStore()
      store.setLoading(true)
      expect(store.state.loading).toBe(true)
      dispose()
    })
  })

  it('should set error', () => {
    createRoot((dispose) => {
      const store = createSectionStore()
      store.setError('something went wrong')
      expect(store.state.error).toBe('something went wrong')
      dispose()
    })
  })

  it('should set sections', () => {
    createRoot((dispose) => {
      const store = createSectionStore()
      const sections = [
        makeSection('s1', 'In Progress', SectionType.IN_PROGRESS, 'n'),
        makeSection('s2', 'Archived', SectionType.ARCHIVED, 'nn'),
      ]
      store.setSections(sections)
      expect(store.state.sections).toHaveLength(2)
      expect(store.state.sections[0].name).toBe('In Progress')
      dispose()
    })
  })

  it('should set items', () => {
    createRoot((dispose) => {
      const store = createSectionStore()
      const items = [
        makeItem('w1', 's1', 'n'),
        makeItem('w2', 's1', 'nn'),
      ]
      store.setItems(items)
      expect(store.state.items).toHaveLength(2)
      dispose()
    })
  })

  it('should add section', () => {
    createRoot((dispose) => {
      const store = createSectionStore()
      store.addSection(makeSection('s1', 'Custom', SectionType.CUSTOM, 'n'))
      expect(store.state.sections).toHaveLength(1)
      expect(store.state.sections[0].name).toBe('Custom')
      dispose()
    })
  })

  it('should remove section and its items', () => {
    createRoot((dispose) => {
      const store = createSectionStore()
      store.setSections([
        makeSection('s1', 'In Progress', SectionType.IN_PROGRESS, 'n'),
        makeSection('s2', 'Custom', SectionType.CUSTOM, 'nn'),
      ])
      store.setItems([
        makeItem('w1', 's1', 'n'),
        makeItem('w2', 's2', 'n'),
      ])

      store.removeSection('s2')
      expect(store.state.sections).toHaveLength(1)
      expect(store.state.sections[0].id).toBe('s1')
      expect(store.state.items).toHaveLength(1)
      expect(store.state.items[0].workspaceId).toBe('w1')
      dispose()
    })
  })

  it('should move workspace to a different section', () => {
    createRoot((dispose) => {
      const store = createSectionStore()
      store.setItems([makeItem('w1', 's1', 'n')])

      store.moveWorkspace('w1', 's2', 'nn')
      expect(store.state.items).toHaveLength(1)
      expect(store.state.items[0].sectionId).toBe('s2')
      expect(store.state.items[0].position).toBe('nn')
      dispose()
    })
  })

  it('should add new workspace item when moving unassigned workspace', () => {
    createRoot((dispose) => {
      const store = createSectionStore()

      store.moveWorkspace('w1', 's1', 'n')
      expect(store.state.items).toHaveLength(1)
      expect(store.state.items[0].workspaceId).toBe('w1')
      expect(store.state.items[0].sectionId).toBe('s1')
      dispose()
    })
  })

  it('should get section for workspace', () => {
    createRoot((dispose) => {
      const store = createSectionStore()
      store.setItems([makeItem('w1', 's1', 'n')])

      expect(store.getSectionForWorkspace('w1')).toBe('s1')
      expect(store.getSectionForWorkspace('w2')).toBeUndefined()
      dispose()
    })
  })

  it('should get in_progress section', () => {
    createRoot((dispose) => {
      const store = createSectionStore()
      store.setSections([
        makeSection('s1', 'In Progress', SectionType.IN_PROGRESS, 'n'),
        makeSection('s2', 'Archived', SectionType.ARCHIVED, 'nn'),
      ])

      const section = store.getInProgressSection()
      expect(section?.id).toBe('s1')
      expect(section?.sectionType).toBe(SectionType.IN_PROGRESS)
      dispose()
    })
  })

  it('should get archived section', () => {
    createRoot((dispose) => {
      const store = createSectionStore()
      store.setSections([
        makeSection('s1', 'In Progress', SectionType.IN_PROGRESS, 'n'),
        makeSection('s2', 'Archived', SectionType.ARCHIVED, 'nn'),
      ])

      const section = store.getArchivedSection()
      expect(section?.id).toBe('s2')
      expect(section?.sectionType).toBe(SectionType.ARCHIVED)
      dispose()
    })
  })

  it('should get items for section sorted by position', () => {
    createRoot((dispose) => {
      const store = createSectionStore()
      store.setItems([
        makeItem('w3', 's1', 'z'),
        makeItem('w1', 's1', 'a'),
        makeItem('w2', 's1', 'n'),
        makeItem('w4', 's2', 'n'),
      ])

      const items = store.getItemsForSection('s1')
      expect(items).toHaveLength(3)
      expect(items[0].workspaceId).toBe('w1')
      expect(items[1].workspaceId).toBe('w2')
      expect(items[2].workspaceId).toBe('w3')
      dispose()
    })
  })
})
