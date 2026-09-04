import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it } from 'vitest'
import {
  KEY_EXPANDED_WORKSPACES,
  sessionStorageClearForTests,
  sessionStorageGet,
  sessionStorageSet,
  setStorageAccount,
} from '~/lib/browserStorage'
import {
  isWorkspaceExpanded,
  resetExpandedWorkspacesForTests,
  setWorkspacesExpanded,
  toggleWorkspaceExpanded,
} from './expandedWorkspaces'

/**
 * The regression this module exists for: the expanded set is ONE document, and
 * several components read it at once.
 *
 * Every test that matters here has two readers. Comparing what a single reader
 * writes against what it reads back proves nothing -- the broken per-instance
 * version passed that, because each instance seeded from the same stored value
 * and only diverged after the first toggle.
 */

/** The stored set, as a sorted array so the assertions do not depend on order. */
function storedIds(): string[] {
  return [...(sessionStorageGet<string[]>(KEY_EXPANDED_WORKSPACES) ?? [])].sort()
}

/**
 * A reader, as a component is: it reads through the module's accessor inside
 * its own reactive root, and it can toggle.
 */
function reader() {
  return createRoot(dispose => ({
    dispose,
    sees: (id: string) => isWorkspaceExpanded(id),
    toggle: (id: string) => toggleWorkspaceExpanded(id),
  }))
}

describe('expandedWorkspaces', () => {
  beforeEach(() => {
    sessionStorageClearForTests()
    setStorageAccount('u-1')
    resetExpandedWorkspacesForTests()
  })

  it('shows one reader the row a SECOND reader expanded', () => {
    const a = reader()
    const b = reader()
    a.toggle('ws-1')
    expect(b.sees('ws-1')).toBe(true)
    a.dispose()
    b.dispose()
  })

  it('keeps each reader expansions when the other toggles', () => {
    const a = reader()
    const b = reader()
    a.toggle('ws-a')
    b.toggle('ws-b')
    expect(a.sees('ws-a')).toBe(true)
    expect(a.sees('ws-b')).toBe(true)
    expect(b.sees('ws-a')).toBe(true)
    expect(storedIds()).toEqual(['ws-a', 'ws-b'])
    a.dispose()
    b.dispose()
  })

  it('persists both readers rows rather than the last writer alone', () => {
    const a = reader()
    const b = reader()
    a.toggle('ws-a')
    b.toggle('ws-b')
    a.toggle('ws-c')
    expect(storedIds()).toEqual(['ws-a', 'ws-b', 'ws-c'])
    a.dispose()
    b.dispose()
  })

  it('restores the stored set on the first access', () => {
    sessionStorageSet(KEY_EXPANDED_WORKSPACES, ['ws-x', 'ws-y'])
    resetExpandedWorkspacesForTests()
    expect(isWorkspaceExpanded('ws-x')).toBe(true)
    expect(isWorkspaceExpanded('ws-y')).toBe(true)
    expect(isWorkspaceExpanded('ws-z')).toBe(false)
  })

  it('collapses a row back off', () => {
    toggleWorkspaceExpanded('ws-1')
    toggleWorkspaceExpanded('ws-1')
    expect(isWorkspaceExpanded('ws-1')).toBe(false)
    expect(storedIds()).toEqual([])
  })

  describe('setWorkspacesExpanded', () => {
    it('collapses only the ids it is given', () => {
      toggleWorkspaceExpanded('mine-1')
      toggleWorkspaceExpanded('mine-2')
      toggleWorkspaceExpanded('theirs')
      setWorkspacesExpanded(['mine-1', 'mine-2'], false)
      expect(isWorkspaceExpanded('mine-1')).toBe(false)
      expect(isWorkspaceExpanded('mine-2')).toBe(false)
      expect(isWorkspaceExpanded('theirs')).toBe(true)
    })

    it('expands every id it is given without disturbing the rest', () => {
      toggleWorkspaceExpanded('theirs')
      setWorkspacesExpanded(['mine-1', 'mine-2'], true)
      expect(storedIds()).toEqual(['mine-1', 'mine-2', 'theirs'])
    })

    it('accepts an empty list', () => {
      toggleWorkspaceExpanded('ws-1')
      setWorkspacesExpanded([], false)
      expect(storedIds()).toEqual(['ws-1'])
    })

    it('writes nothing when every id already holds the requested state', () => {
      toggleWorkspaceExpanded('ws-1')
      sessionStorageSet(KEY_EXPANDED_WORKSPACES, ['sentinel'])
      setWorkspacesExpanded(['ws-1'], true)
      // A no-op keeps the signal identity, so the persist effect never re-runs
      // and the sentinel survives.
      expect(storedIds()).toEqual(['sentinel'])
    })
  })

  // The account switch is the whole reason this module carries a seed latch,
  // and nothing exercised it. A subscribed reader must SEE the change: the
  // reset writes the module's `EMPTY` constant, which is referentially equal to
  // the un-seeded value, so Solid's `===` equality drops that write -- a reader
  // that only re-read on notification stayed on the previous account's set.
  it('re-reads under the new account when the namespace moves', () => {
    // Both accounts' documents are written first, so the switch has something
    // to find. The key is account-scoped, so each write lands under whichever
    // account is current.
    setStorageAccount('u-2')
    sessionStorageSet(KEY_EXPANDED_WORKSPACES, ['ws-owned-by-2'])
    setStorageAccount('u-1')
    sessionStorageSet(KEY_EXPANDED_WORKSPACES, ['ws-owned-by-1'])
    resetExpandedWorkspacesForTests()

    const r = reader()
    expect(r.sees('ws-owned-by-1')).toBe(true)

    setStorageAccount('u-2')

    expect(r.sees('ws-owned-by-1')).toBe(false)
    expect(r.sees('ws-owned-by-2')).toBe(true)
    r.dispose()
  })

  // The mirror must not carry one account's rows into another's sidebar, even
  // when the second account has nothing stored.
  it('does not leak one account\'s expanded rows into an account with none', () => {
    setStorageAccount('u-1')
    const r = reader()
    r.toggle('ws-1')
    expect(r.sees('ws-1')).toBe(true)

    setStorageAccount('u-2')

    expect(r.sees('ws-1')).toBe(false)
    r.dispose()
  })
})
