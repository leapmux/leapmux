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
 * One sidebar mounts one `WorkspaceSectionContent` per workspace section, a
 * custom workspace section can sit on the other sidebar, and the section header
 * MENU is a further reader outside all of them. So every test below has two
 * readers -- a single reader agreeing with itself proves nothing about the
 * state this module exists to share.
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

  // The account switch is the whole reason this module carries a seed latch,
  // and nothing exercised it. The reset writes `DEFAULT_WORKSPACE_SORT_ORDER`,
  // a module-level constant, so on the un-seeded path Solid's `===` equality
  // drops that write -- a reader that only re-read on notification stayed on
  // the previous account's order.
  it('re-reads the sort order under the new account', () => {
    setStorageAccount('u-2')
    localStorageSet(KEY_WORKSPACE_SORT, { key: 'created', direction: 'asc' })
    setStorageAccount('u-1')
    localStorageSet(KEY_WORKSPACE_SORT, { key: 'name', direction: 'desc' })
    resetWorkspaceListStateForTests()

    const r = reader()
    expect(r.order()).toEqual({ key: 'name', direction: 'desc' })

    setStorageAccount('u-2')

    expect(r.order()).toEqual({ key: 'created', direction: 'asc' })
    r.dispose()
  })

  // The filter is NOT persisted, so a switch must leave the incoming account
  // with none -- its section ids are not the outgoing account's.
  it('drops every section filter when the account moves', () => {
    setStorageAccount('u-1')
    resetWorkspaceListStateForTests()
    toggleSectionFilter('sec-1')
    setSectionFilterQuery('sec-1', 'infra')
    expect(isSectionFilterShown('sec-1')).toBe(true)

    setStorageAccount('u-2')

    expect(isSectionFilterShown('sec-1')).toBe(false)
    expect(sectionFilterQuery('sec-1')).toBeUndefined()
  })
})
