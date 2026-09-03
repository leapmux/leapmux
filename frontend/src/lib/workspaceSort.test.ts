import type { WorkspaceSortKey, WorkspaceSortOrder } from './workspaceSort'
import type { Workspace } from '~/generated/proto/leapmux/v1/workspace_pb'
import { describe, expect, it } from 'vitest'
import {
  canReorderWithin,
  DEFAULT_WORKSPACE_SORT_ORDER,
  filterWorkspaces,
  parseWorkspaceSortOrder,
  sortDirectionLabel,
  sortKeyLabel,
  sortWorkspaces,
  WORKSPACE_SORT_DIRECTIONS,
  WORKSPACE_SORT_KEYS,
} from './workspaceSort'

function ws(id: string, title: string, createdAt = ''): Workspace {
  return { id, title, createdAt } as Workspace
}

function order(key: WorkspaceSortKey, direction: 'asc' | 'desc' = 'asc'): WorkspaceSortOrder {
  return { key, direction }
}

const NO_RECENCY = () => undefined

function ids(list: readonly Workspace[]): string[] {
  return list.map(w => w.id)
}

describe('sortKeyLabel', () => {
  it('names every key', () => {
    for (const key of WORKSPACE_SORT_KEYS)
      expect(sortKeyLabel(key)).toBeTruthy()
  })
})

describe('sortDirectionLabel', () => {
  it('answers for every (key, direction) pair, including manual', () => {
    // The menu draws both order radios whatever the key is, so a missing entry
    // would throw on the lookup rather than degrade.
    for (const key of WORKSPACE_SORT_KEYS) {
      for (const direction of WORKSPACE_SORT_DIRECTIONS)
        expect(sortDirectionLabel(key, direction)).toBeTruthy()
    }
  })

  it('words the direction for the criterion, not generically', () => {
    expect(sortDirectionLabel('name', 'asc')).not.toBe(sortDirectionLabel('created', 'asc'))
  })
})

describe('parseWorkspaceSortOrder', () => {
  it('accepts a well-formed order', () => {
    expect(parseWorkspaceSortOrder({ key: 'name', direction: 'desc' }))
      .toEqual({ key: 'name', direction: 'desc' })
  })

  it.each([
    ['null', null],
    ['a string', 'name'],
    ['a number', 3],
    ['an unknown key', { key: 'colour', direction: 'asc' }],
    ['an unknown direction', { key: 'name', direction: 'sideways' }],
    ['a missing direction', { key: 'name' }],
  ])('falls back to the default for %s', (_label, raw) => {
    // An unrecognized key would otherwise reach the direction-label lookup,
    // which throws.
    expect(parseWorkspaceSortOrder(raw)).toEqual(DEFAULT_WORKSPACE_SORT_ORDER)
  })

  it('defaults to the manual order, which is the lexorank one', () => {
    expect(DEFAULT_WORKSPACE_SORT_ORDER.key).toBe('manual')
  })
})

describe('sortWorkspaces', () => {
  const list = [ws('c', 'Charlie', '2026-03-01'), ws('a', 'alpha', '2026-01-01'), ws('b', 'Bravo', '2026-02-01')]

  it('leaves the manual order untouched, in either direction', () => {
    // That order IS the lexorank the caller already assembled; re-deriving it
    // would be a second answer to what the model says.
    expect(ids(sortWorkspaces(list, order('manual'), NO_RECENCY))).toEqual(['c', 'a', 'b'])
    expect(ids(sortWorkspaces(list, order('manual', 'desc'), NO_RECENCY))).toEqual(['c', 'a', 'b'])
  })

  it('does not mutate its input', () => {
    const input = [...list]
    sortWorkspaces(input, order('name'), NO_RECENCY)
    expect(ids(input)).toEqual(['c', 'a', 'b'])
  })

  it('sorts by name, case-insensitively, in both directions', () => {
    expect(ids(sortWorkspaces(list, order('name'), NO_RECENCY))).toEqual(['a', 'b', 'c'])
    expect(ids(sortWorkspaces(list, order('name', 'desc'), NO_RECENCY))).toEqual(['c', 'b', 'a'])
  })

  it('sorts by creation in both directions', () => {
    expect(ids(sortWorkspaces(list, order('created'), NO_RECENCY))).toEqual(['a', 'b', 'c'])
    expect(ids(sortWorkspaces(list, order('created', 'desc'), NO_RECENCY))).toEqual(['c', 'b', 'a'])
  })

  it('sorts by recency in both directions', () => {
    const recency = (id: string) => ({ a: 3, b: 1, c: 2 })[id]
    expect(ids(sortWorkspaces(list, order('recent'), recency))).toEqual(['b', 'c', 'a'])
    expect(ids(sortWorkspaces(list, order('recent', 'desc'), recency))).toEqual(['a', 'c', 'b'])
  })

  it('pins a workspace with NO recency last under both directions', () => {
    // `mru` is a per-session counter, so "never activated this session" is a
    // real answer -- and it must not migrate to the top when the direction
    // flips.
    const recency = (id: string) => (id === 'b' ? undefined : 1)
    expect(ids(sortWorkspaces(list, order('recent'), recency)).at(-1)).toBe('b')
    expect(ids(sortWorkspaces(list, order('recent', 'desc'), recency)).at(-1)).toBe('b')
  })

  it('breaks a recency tie by title, so the order is total', () => {
    const recency = () => 5
    expect(ids(sortWorkspaces(list, order('recent'), recency))).toEqual(['a', 'b', 'c'])
  })

  it('breaks a creation tie by title', () => {
    const tied = [ws('c', 'Charlie', '2026-01-01'), ws('a', 'alpha', '2026-01-01')]
    expect(ids(sortWorkspaces(tied, order('created'), NO_RECENCY))).toEqual(['a', 'c'])
  })

  it('pins a workspace with no creation time last', () => {
    const partial = [ws('a', 'alpha'), ws('b', 'Bravo', '2026-01-01')]
    expect(ids(sortWorkspaces(partial, order('created'), NO_RECENCY))).toEqual(['b', 'a'])
    expect(ids(sortWorkspaces(partial, order('created', 'desc'), NO_RECENCY))).toEqual(['b', 'a'])
  })

  it('handles an empty list and a single entry', () => {
    expect(sortWorkspaces([], order('name'), NO_RECENCY)).toEqual([])
    expect(ids(sortWorkspaces([ws('a', 'alpha')], order('recent'), NO_RECENCY))).toEqual(['a'])
  })

  it('sorts an untitled workspace without throwing', () => {
    const untitled = [ws('b', ''), ws('a', 'alpha')]
    expect(ids(sortWorkspaces(untitled, order('name'), NO_RECENCY))).toEqual(['b', 'a'])
  })
})

describe('filterWorkspaces', () => {
  const list = [ws('a', 'gentle-amber-fox'), ws('b', 'Bold Blue Bear'), ws('c', 'amber tooling')]

  it('keeps everything for an empty query', () => {
    expect(ids(filterWorkspaces(list, ''))).toEqual(['a', 'b', 'c'])
    expect(ids(filterWorkspaces(list, '   '))).toEqual(['a', 'b', 'c'])
  })

  it('matches a substring, case-insensitively', () => {
    expect(ids(filterWorkspaces(list, 'AMBER'))).toEqual(['a', 'c'])
  })

  it('trims the query', () => {
    expect(ids(filterWorkspaces(list, '  bear  '))).toEqual(['b'])
  })

  it('answers empty when nothing matches', () => {
    expect(filterWorkspaces(list, 'zzz')).toEqual([])
  })

  it('does not mutate its input', () => {
    const input = [...list]
    filterWorkspaces(input, 'amber')
    expect(ids(input)).toEqual(['a', 'b', 'c'])
  })
})

describe('canReorderWithin', () => {
  it('allows reordering under the manual order with no filter', () => {
    expect(canReorderWithin(order('manual'), '')).toBe(true)
    expect(canReorderWithin(order('manual', 'desc'), '')).toBe(true)
  })

  it('refuses under EVERY non-manual key', () => {
    // The sort is global, so one non-manual pick disables reordering in every
    // section at once. Only the between-row drop targets go: the grip stays,
    // and move-to-section and drag-to-archive keep working.
    for (const key of WORKSPACE_SORT_KEYS) {
      if (key === 'manual')
        continue
      for (const direction of WORKSPACE_SORT_DIRECTIONS)
        expect(canReorderWithin(order(key, direction), '')).toBe(false)
    }
  })

  it('refuses while a filter narrows the list', () => {
    expect(canReorderWithin(order('manual'), 'amber')).toBe(false)
  })

  it('allows reordering while the filter box is OPEN but empty', () => {
    // An open input is not a narrowed list, and the view order still equals
    // the model order.
    expect(canReorderWithin(order('manual'), '')).toBe(true)
    expect(canReorderWithin(order('manual'), '   ')).toBe(true)
  })
})
