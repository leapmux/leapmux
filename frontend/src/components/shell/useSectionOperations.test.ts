import type { Section } from '~/generated/proto/leapmux/v1/section_pb'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { SectionType, Sidebar } from '~/generated/proto/leapmux/v1/section_pb'
import { createSectionStore } from '~/stores/section.store'
import { useSectionOperations } from './useSectionOperations'

const mockCreateSection = vi.fn<(req: { name: string, sidebar: Sidebar }) => Promise<{ section?: Section }>>()
const mockRenameSection = vi.fn<(req: { sectionId: string, name: string }) => Promise<unknown>>()
const mockDeleteSection = vi.fn<(req: { sectionId: string }) => Promise<unknown>>()
const mockShowWarnToast = vi.fn()

vi.mock('~/api/clients', () => ({
  sectionClient: {
    createSection: (...args: unknown[]) => mockCreateSection(...args as [{ name: string, sidebar: Sidebar }]),
    renameSection: (...args: unknown[]) => mockRenameSection(...args as [{ sectionId: string, name: string }]),
    deleteSection: (...args: unknown[]) => mockDeleteSection(...args as [{ sectionId: string }]),
  },
}))

vi.mock('~/components/common/Toast', () => ({
  showWarnToast: (...args: unknown[]) => mockShowWarnToast(...args),
}))

function section(id: string, name: string, sectionType = SectionType.WORKSPACES_CUSTOM): Section {
  return { id, name, position: 'n', sectionType, sidebar: Sidebar.LEFT } as Section
}

function harness(sections: Section[] = []) {
  const loadSections = vi.fn(async () => {})
  return createRoot((dispose) => {
    const sectionStore = createSectionStore()
    sectionStore.setSections(sections)
    return { dispose, sectionStore, loadSections, ops: useSectionOperations({ sectionStore, loadSections }) }
  })
}

describe('useSectionOperations', () => {
  beforeEach(() => {
    // `clearAllMocks` clears the CALL history but keeps any implementation a
    // case installed, so every rejection below is `...Once`.
    vi.clearAllMocks()
  })

  describe('createSection', () => {
    it('stores the row the SERVER computed, position and all', async () => {
      // The response carries the whole section because the server owns the
      // lexorank. Re-deriving it here would be a second source of truth for the
      // section order.
      const created = section('sec-new', 'Reviews')
      mockCreateSection.mockResolvedValue({ section: created })
      const h = harness()

      await h.ops.createSection('Reviews', Sidebar.LEFT)

      expect(mockCreateSection).toHaveBeenCalledWith({ name: 'Reviews', sidebar: Sidebar.LEFT })
      expect(h.sectionStore.state.sections).toEqual([created])
      h.dispose()
    })

    it('sends the cleaned name, because the hub sanitizes whatever arrives', async () => {
      mockCreateSection.mockResolvedValue({ section: section('sec-new', 'Reviews') })
      const h = harness()

      await h.ops.createSection('  Reviews  ', Sidebar.RIGHT)

      expect(mockCreateSection).toHaveBeenCalledWith({ name: 'Reviews', sidebar: Sidebar.RIGHT })
      h.dispose()
    })

    // REJECTS rather than resolving. A resolve is the dialog's success signal,
    // so a quiet return closed the dialog as though a section had been made.
    it('rejects a name that cleans to nothing, and sends nothing', async () => {
      const h = harness()
      await expect(h.ops.createSection('   ', Sidebar.LEFT)).rejects.toThrow(/needs a name/)
      expect(mockCreateSection).not.toHaveBeenCalled()
      h.dispose()
    })

    it('rejects rather than toasting, so the dialog shows the failure once', async () => {
      mockCreateSection.mockRejectedValueOnce(new Error('nope'))
      const h = harness()

      await expect(h.ops.createSection('Reviews', Sidebar.LEFT)).rejects.toThrow('nope')
      expect(mockShowWarnToast).not.toHaveBeenCalled()
      expect(h.sectionStore.state.sections).toEqual([])
      h.dispose()
    })

    it('rejects a response with no section, rather than storing nothing quietly', async () => {
      mockCreateSection.mockResolvedValue({})
      const h = harness()

      await expect(h.ops.createSection('Reviews', Sidebar.LEFT)).rejects.toThrow(/No section/)
      h.dispose()
    })
  })

  describe('renameSection', () => {
    it('renames the stored row', async () => {
      mockRenameSection.mockResolvedValue({})
      const h = harness([section('sec-1', 'Old')])

      await h.ops.renameSection('sec-1', '  New  ')

      expect(mockRenameSection).toHaveBeenCalledWith({ sectionId: 'sec-1', name: 'New' })
      expect(h.sectionStore.state.sections[0].name).toBe('New')
      h.dispose()
    })

    it('leaves the row alone when the RPC fails', async () => {
      mockRenameSection.mockRejectedValueOnce(new Error('nope'))
      const h = harness([section('sec-1', 'Old')])

      await expect(h.ops.renameSection('sec-1', 'New')).rejects.toThrow('nope')
      expect(h.sectionStore.state.sections[0].name).toBe('Old')
      h.dispose()
    })

    it('rejects a name that cleans to nothing, and sends nothing', async () => {
      const h = harness([section('sec-1', 'Old')])
      await expect(h.ops.renameSection('sec-1', '​')).rejects.toThrow(/needs a name/)
      expect(mockRenameSection).not.toHaveBeenCalled()
      h.dispose()
    })
  })

  describe('deleteSection', () => {
    it('drops the row and re-reads the sections', async () => {
      // The hub re-stamps every relocated item's lexorank, and those positions
      // are the server's to compute.
      mockDeleteSection.mockResolvedValue({})
      const h = harness([section('sec-1', 'Reviews')])

      await h.ops.deleteSection('sec-1')

      expect(mockDeleteSection).toHaveBeenCalledWith({ sectionId: 'sec-1' })
      expect(h.sectionStore.state.sections).toEqual([])
      expect(h.loadSections).toHaveBeenCalledOnce()
      h.dispose()
    })

    // One policy for the module: all three reject, and the confirm's call site
    // owns the toast. A caller that swallowed the rejection here would leave a
    // new call site free to pick up the wrong surface by accident.
    it('rejects when the RPC fails, and keeps the row', async () => {
      mockDeleteSection.mockRejectedValueOnce(new Error('nope'))
      const h = harness([section('sec-1', 'Reviews')])

      await expect(h.ops.deleteSection('sec-1')).rejects.toThrow('nope')

      expect(mockShowWarnToast).not.toHaveBeenCalled()
      expect(h.sectionStore.state.sections).toHaveLength(1)
      h.dispose()
    })

    // The reload runs AFTER the section is already gone, so a failure there is
    // not a failed delete. Reporting it as one told the user a completed
    // destructive operation had failed.
    it('reports a failed refresh separately, and keeps the row deleted', async () => {
      const h = harness([section('sec-1', 'Reviews')])
      h.loadSections.mockRejectedValueOnce(new Error('offline'))

      await h.ops.deleteSection('sec-1')

      expect(mockDeleteSection).toHaveBeenCalled()
      expect(h.sectionStore.state.sections).toHaveLength(0)
      expect(mockShowWarnToast).toHaveBeenCalledWith(
        'Deleted the section, but could not refresh the sidebar',
        expect.any(Error),
      )
      h.dispose()
    })
  })
})
