import { describe, expect, it } from 'vitest'
import {
  deserializeState,
  DIRECTORY_TREE_STATE_VERSION,
  formatTruncationNotice,
  isDescendantPath,
  samePath,
  sameTreeEntries,
  serializeState,
  visibleSortedChildren,
} from './directoryTreeState'

function node(path: string, over: Partial<{ displayName: string, isDir: boolean, hidden: boolean, size: number, modTime: string }> = {}) {
  return {
    path,
    displayName: over.displayName ?? path.split('/').pop()!,
    isDir: over.isDir ?? false,
    hidden: over.hidden ?? false,
    size: over.size ?? 0,
    modTime: over.modTime ?? '2026-01-01T00:00:00Z',
  }
}

describe('isDescendantPath', () => {
  it('accepts a path strictly under the parent', () => {
    expect(isDescendantPath('/a/b', '/a', 'posix')).toBe(true)
    expect(isDescendantPath('/a/b/c', '/a', 'posix')).toBe(true)
  })

  // A node never counts as its own descendant: the reveal effect uses this to
  // decide what to auto-expand, and a node must not expand itself.
  it('refuses the parent itself and a sibling', () => {
    expect(isDescendantPath('/a', '/a', 'posix')).toBe(false)
    expect(isDescendantPath('/ab', '/a', 'posix')).toBe(false)
  })

  /**
   * `PathInput` submits whatever the user typed, so a win32 selection reaches
   * the tree spelled `C:/Users/alice` while the worker's listings spell it
   * `C:\Users\alice`. An un-normalized comparison answers "not under", which
   * collapses the whole reveal.
   */
  it('normalizes separators and case on win32', () => {
    expect(isDescendantPath('C:/Users/alice', 'C:\\Users', 'win32')).toBe(true)
    expect(isDescendantPath('c:\\users\\alice', 'C:\\Users', 'win32')).toBe(true)
  })

  // `\` is a legal character in a POSIX file name, so it is NOT a separator
  // there and `a\b` is one component.
  it('does not treat a backslash as a separator on posix', () => {
    expect(isDescendantPath('/a\\b', '/a', 'posix')).toBe(false)
  })
})

describe('samePath', () => {
  it('matches across win32 separator and case spellings', () => {
    expect(samePath('C:/Users', 'C:\\users', 'win32')).toBe(true)
  })

  it('is byte-exact on posix', () => {
    expect(samePath('/A', '/a', 'posix')).toBe(false)
    expect(samePath('/a', '/a', 'posix')).toBe(true)
  })
})

describe('sameTreeEntries', () => {
  it('reports identical content as unchanged', () => {
    expect(sameTreeEntries([node('/a/x')], [node('/a/x')])).toBe(true)
    expect(sameTreeEntries([], [])).toBe(true)
  })

  // size and modTime are in the comparison because the tree SORTS and DISPLAYS
  // them: a file whose contents changed but whose name did not would otherwise
  // keep its stale size and its stale position under a size sort.
  it('reports a changed size or modTime as changed', () => {
    expect(sameTreeEntries([node('/a/x', { size: 1 })], [node('/a/x', { size: 2 })])).toBe(false)
    expect(sameTreeEntries(
      [node('/a/x', { modTime: '2026-01-01T00:00:00Z' })],
      [node('/a/x', { modTime: '2026-02-01T00:00:00Z' })],
    )).toBe(false)
  })

  it('reports a different length or a different path as changed', () => {
    expect(sameTreeEntries([node('/a/x')], [node('/a/x'), node('/a/y')])).toBe(false)
    expect(sameTreeEntries([node('/a/x')], [node('/a/y')])).toBe(false)
  })
})

describe('serializeState and deserializeState', () => {
  const state = {
    expandedPaths: { '/a': true },
    childrenCache: { '/a': [node('/a/x')] },
    truncatedDirs: { '/a': 300 },
  }

  it('round-trips a payload at the current version', () => {
    expect(deserializeState(serializeState(state.expandedPaths, state.childrenCache, state.truncatedDirs)))
      .toEqual(state)
  })

  // A version mismatch discards EVERYTHING, expansion included: a partial
  // restore would have to prove, per key, that the old shape still reads
  // correctly under the new code.
  it('discards a payload stamped with another version', () => {
    const stored = serializeState(state.expandedPaths, state.childrenCache, state.truncatedDirs)
    expect(deserializeState({ ...stored, v: DIRECTORY_TREE_STATE_VERSION + 1 })).toBeNull()
    expect(deserializeState({ ...stored, v: undefined })).toBeNull()
    expect(deserializeState(undefined)).toBeNull()
  })

  // The version answers "is this shape current"; this answers "is this value
  // well formed" -- for a hand edit, or a truncated write.
  it('drops a malformed directory but keeps the rest', () => {
    const restored = deserializeState({
      v: DIRECTORY_TREE_STATE_VERSION,
      expandedPaths: { '/a': true },
      childrenCache: { '/a': [node('/a/x')], '/bad': 'nope' as never, '/partial': [{ path: '/p' } as never] },
      truncatedDirs: {},
    })
    expect(Object.keys(restored!.childrenCache)).toEqual(['/a'])
  })
})

describe('formatTruncationNotice', () => {
  // The worker reports what the directory really held, so the notice gives the
  // size of what is hidden rather than only that something is.
  it('gives the real total when the worker sent one', () => {
    expect(formatTruncationNotice(256, 300, 'name')).toBe('256 of 300 entries, listing truncated')
  })

  // `total` is 0 for a listing restored from a cache written before the worker
  // sent it, so the notice falls back rather than claiming a total of zero.
  it('falls back to N+ when the total is unknown', () => {
    expect(formatTruncationNotice(256, 0, 'name')).toBe('256+ entries, listing truncated')
  })

  // The worker cuts BY NAME before stat-ing, so any other sort orders only
  // what survived that cut. The notice has to say so.
  it('says the cut came first for a non-name sort', () => {
    expect(formatTruncationNotice(256, 300, 'size')).toBe('256 of 300 entries, truncated by name before sorting')
  })
})

describe('visibleSortedChildren', () => {
  const byName = (a: { displayName: string }, b: { displayName: string }) => a.displayName.localeCompare(b.displayName)
  const all = [node('/a/c'), node('/a/.hidden'), node('/a/b')]

  it('drops hidden entries and sorts the rest', () => {
    expect(visibleSortedChildren([node('/a/c'), node('/a/.h', { hidden: true }), node('/a/b')], false, undefined, byName)
      .map(n => n.displayName)).toEqual(['b', 'c'])
  })

  it('keeps hidden entries when asked', () => {
    expect(visibleSortedChildren(all, true, undefined, byName)).toHaveLength(3)
  })

  // The git filter runs even when hidden files are shown, so a filtered tree
  // does not quietly widen when the user toggles hidden files on.
  it('applies the visibility filter even with hidden shown', () => {
    const visible = (p: string) => p !== '/a/c'
    expect(visibleSortedChildren(all, true, visible, byName).map(n => n.path)).not.toContain('/a/c')
  })

  it('does not mutate the input array', () => {
    const input = [node('/a/c'), node('/a/b')]
    visibleSortedChildren(input, true, undefined, byName)
    expect(input.map(n => n.displayName)).toEqual(['c', 'b'])
  })
})
