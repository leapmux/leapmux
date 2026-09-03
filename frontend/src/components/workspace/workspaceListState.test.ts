import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it } from 'vitest'
import {
  KEY_WORKSPACE_SORT,
  localStorageClearForTests,
  localStorageGet,
  localStorageSet,
  setStorageAccount,
} from '~/lib/browserStorage'
import { DEFAULT_WORKSPACE_SORT_ORDER } from '~/lib/workspaceSort'
import {
  canReorderWithinSection,
  isSectionFilterShown,
  resetWorkspaceListStateForTests,
  sectionFilterQuery,
  setSectionFilterQuery,
  setWorkspaceSortOrder,
  toggleSectionFilter,
  workspaceSortOrder,
} from './workspaceListState'

/**
 * One sidebar mounts one `WorkspaceSectionContent` per workspace section, the
 * app mounts the sidebar twice, and the section header MENU is a fourth reader
 * outside all of them. So every test below has two readers -- a single reader
 * agreeing with itself proves nothing about the state this module exists to
 * share.
 */
function reader() {
  return createRoot(dispose => ({
    dispose,
    order: () => workspaceSortOrder(),
    filter: (sectionId: string) => sectionFilterQuery(sectionId),
    canReorder: (sectionId: string) => canReorderWithinSection(sectionId),
  }))
}

describe('workspaceListState', () => {
  beforeEach(() => {
    localStorageClearForTests()
    setStorageAccount('u-1')
    resetWorkspaceListStateForTests()
  })

  describe('the sort order', () => {
    it('starts on the manual order, which is the lexorank one', () => {
      expect(workspaceSortOrder()).toEqual(DEFAULT_WORKSPACE_SORT_ORDER)
    })

    it('shows one reader the order a SECOND reader set', () => {
      const a = reader()
      const b = reader()
      a.dispose()
      setWorkspaceSortOrder({ key: 'name', direction: 'desc' })
      expect(b.order()).toEqual({ key: 'name', direction: 'desc' })
      b.dispose()
    })

    it('persists the order, and restores it on the next read', () => {
      setWorkspaceSortOrder({ key: 'recent', direction: 'desc' })
      expect(localStorageGet(KEY_WORKSPACE_SORT)).toEqual({ key: 'recent', direction: 'desc' })

      resetWorkspaceListStateForTests()
      expect(workspaceSortOrder()).toEqual({ key: 'recent', direction: 'desc' })
    })

    it('falls back to the default for a stored value that no longer parses', () => {
      localStorageSet(KEY_WORKSPACE_SORT, { key: 'colour', direction: 'asc' })
      resetWorkspaceListStateForTests()
      expect(workspaceSortOrder()).toEqual(DEFAULT_WORKSPACE_SORT_ORDER)
    })

    it('writes nothing before the first read, so it cannot erase a stored order', () => {
      localStorageSet(KEY_WORKSPACE_SORT, { key: 'created', direction: 'asc' })
      resetWorkspaceListStateForTests()
      // No read yet: the stored value must still be there.
      expect(localStorageGet(KEY_WORKSPACE_SORT)).toEqual({ key: 'created', direction: 'asc' })
    })
  })

  describe('the per-section filter', () => {
    it('is hidden for every section until it is toggled', () => {
      expect(isSectionFilterShown('sec-a')).toBe(false)
      expect(sectionFilterQuery('sec-a')).toBeUndefined()
    })

    it('opens EMPTY rather than absent, so "open" and "typed" are one value', () => {
      toggleSectionFilter('sec-a')
      expect(isSectionFilterShown('sec-a')).toBe(true)
      expect(sectionFilterQuery('sec-a')).toBe('')
    })

    it('clears the query when it closes', () => {
      toggleSectionFilter('sec-a')
      setSectionFilterQuery('sec-a', 'amber')
      toggleSectionFilter('sec-a')
      expect(sectionFilterQuery('sec-a')).toBeUndefined()

      toggleSectionFilter('sec-a')
      expect(sectionFilterQuery('sec-a')).toBe('')
    })

    it('keeps two sections independent', () => {
      toggleSectionFilter('sec-a')
      setSectionFilterQuery('sec-a', 'amber')
      expect(isSectionFilterShown('sec-b')).toBe(false)
      expect(sectionFilterQuery('sec-b')).toBeUndefined()
    })

    it('shows one reader what a SECOND reader typed', () => {
      const a = reader()
      const b = reader()
      toggleSectionFilter('sec-a')
      setSectionFilterQuery('sec-a', 'amber')
      expect(a.filter('sec-a')).toBe('amber')
      expect(b.filter('sec-a')).toBe('amber')
      a.dispose()
      b.dispose()
    })

    it('is NOT persisted: a filter that survives a reload hides rows silently', () => {
      toggleSectionFilter('sec-a')
      setSectionFilterQuery('sec-a', 'amber')
      resetWorkspaceListStateForTests()
      expect(isSectionFilterShown('sec-a')).toBe(false)
    })
  })

  describe('canReorderWithinSection', () => {
    it('allows reordering by default', () => {
      expect(canReorderWithinSection('sec-a')).toBe(true)
    })

    it('refuses in EVERY section once a non-manual sort is picked', () => {
      // The sort is global, so one pick changes every section at once.
      setWorkspaceSortOrder({ key: 'name', direction: 'asc' })
      expect(canReorderWithinSection('sec-a')).toBe(false)
      expect(canReorderWithinSection('sec-b')).toBe(false)
    })

    it('refuses in the FILTERED section alone', () => {
      toggleSectionFilter('sec-a')
      setSectionFilterQuery('sec-a', 'amber')
      expect(canReorderWithinSection('sec-a')).toBe(false)
      expect(canReorderWithinSection('sec-b')).toBe(true)
    })

    it('still allows reordering while the filter box is open but empty', () => {
      toggleSectionFilter('sec-a')
      expect(canReorderWithinSection('sec-a')).toBe(true)
    })
  })
})
