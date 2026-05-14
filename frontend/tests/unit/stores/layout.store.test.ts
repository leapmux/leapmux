import type { GridNode, LayoutNodeLocal, LeafNode, SplitNode, SplitOrientation, TilePredicates } from '~/stores/layout.store'
import { describe, expect, it } from 'vitest'
import {
  buildTilePredicateMap,
  findGridById,
  findHeirTileId,
  getAllTileIds,
  MAX_DEPTH,
  nextFocusAfterDisposal,
  nextFocusEnsuringValid,
} from '~/stores/layout.store'

// Pure-utility tests for the surviving exports of `~/stores/layout.store`.
// The legacy local-tree mutators (`splitTileInTree`, `makeGridInTree`,
// `optimize`, `replaceNode`, `removeNode`, `replaceGridById`,
// `addSiblingInSameDirectionSplit`, `applyGridRatios`, `applySplitRatios`,
// `planCloseTile`, etc.) were deleted as part of the projection-driven
// CRDT migration; their tests went with them. Behavioural coverage of the
// new store lives in `tests/unit/stores/layout.store.crdt.test.ts`.

// --- Helpers ---

function leaf(id: string): LeafNode {
  return { type: 'leaf', id }
}

function split(
  id: string,
  direction: SplitOrientation,
  ratios: number[],
  ...children: LayoutNodeLocal[]
): SplitNode {
  return { type: 'split', id, direction, ratios, children }
}

function grid(
  id: string,
  rows: number,
  cols: number,
  rowRatios: number[],
  colRatios: number[],
  ...cells: LayoutNodeLocal[]
): GridNode {
  return { type: 'grid', id, rows, cols, rowRatios, colRatios, cells }
}

function equalRatios(n: number): number[] {
  return (Array.from({ length: n }) as number[]).fill(1 / n)
}

// --- getAllTileIds ---

describe('getAllTileIds', () => {
  it('returns single id for leaf', () => {
    expect(getAllTileIds(leaf('A'))).toEqual(['A'])
  })

  it('returns all leaf ids from a split', () => {
    const node = split('s1', 'horizontal', [0.33, 0.34, 0.33], leaf('A'), leaf('B'), leaf('C'))
    expect(getAllTileIds(node)).toEqual(['A', 'B', 'C'])
  })

  it('returns all leaf ids from nested splits', () => {
    const inner = split('s2', 'vertical', [0.5, 0.5], leaf('B'), leaf('C'))
    const node = split('s1', 'horizontal', [0.5, 0.5], leaf('A'), inner)
    expect(getAllTileIds(node)).toEqual(['A', 'B', 'C'])
  })

  it('walks into grid cells', () => {
    const g = grid('g1', 2, 2, equalRatios(2), equalRatios(2), leaf('A'), leaf('B'), leaf('C'), leaf('D'))
    expect(getAllTileIds(g).sort()).toEqual(['A', 'B', 'C', 'D'])
  })

  it('walks into grids nested in splits and grids', () => {
    const innerGrid = grid('g2', 1, 2, [1.0], equalRatios(2), leaf('X'), leaf('Y'))
    const outer = split('s1', 'horizontal', [0.5, 0.5], leaf('A'), grid('g1', 1, 2, [1.0], equalRatios(2), innerGrid, leaf('Z')))
    expect(getAllTileIds(outer).sort()).toEqual(['A', 'X', 'Y', 'Z'])
  })
})

// --- findHeirTileId ---

describe('findHeirTileId', () => {
  it('returns null for a single-leaf root', () => {
    expect(findHeirTileId(leaf('A'), 'A')).toBeNull()
  })

  it('returns null when the closing tile is not in the tree', () => {
    expect(findHeirTileId(leaf('A'), 'missing')).toBeNull()
  })

  it('returns the surviving sibling in a 2-tile split (close right)', () => {
    const root = split('s1', 'horizontal', [0.5, 0.5], leaf('A'), leaf('B'))
    expect(findHeirTileId(root, 'B')).toBe('A')
  })

  it('returns the surviving sibling in a 2-tile split (close left)', () => {
    const root = split('s1', 'horizontal', [0.5, 0.5], leaf('A'), leaf('B'))
    expect(findHeirTileId(root, 'A')).toBe('B')
  })

  it('prefers the left/upper sibling when there are neighbors on both sides', () => {
    const root = split('s1', 'horizontal', equalRatios(3), leaf('A'), leaf('B'), leaf('C'))
    expect(findHeirTileId(root, 'B')).toBe('A')
  })

  it('falls back to the right neighbor when the closing tile is the leftmost', () => {
    const root = split('s1', 'horizontal', equalRatios(3), leaf('A'), leaf('B'), leaf('C'))
    expect(findHeirTileId(root, 'A')).toBe('B')
  })

  it('descends into adjacent subtree to pick the first leaf', () => {
    const inner = split('s2', 'vertical', [0.5, 0.5], leaf('B1'), leaf('B2'))
    const root = split('s1', 'horizontal', [0.5, 0.5], inner, leaf('C'))
    expect(findHeirTileId(root, 'C')).toBe('B1')
  })

  it('walks up past a single-child ancestor to find a sibling', () => {
    const innerSplit = split('s2', 'horizontal', [1], leaf('A'))
    const root = split('s1', 'horizontal', [0.5, 0.5], innerSplit, leaf('B'))
    expect(findHeirTileId(root, 'A')).toBe('B')
  })

  it('finds a sibling cell within a grid', () => {
    const root = grid('g1', 1, 2, [1], [0.5, 0.5], leaf('A'), leaf('B'))
    expect(findHeirTileId(root, 'A')).toBe('B')
    expect(findHeirTileId(root, 'B')).toBe('A')
  })

  it('walks out of a grid cell to a split sibling when the cell has no internal neighbor', () => {
    const innerGrid = grid('g1', 1, 1, [1], [1], leaf('A'))
    const root = split('s1', 'horizontal', [0.5, 0.5], innerGrid, leaf('B'))
    expect(findHeirTileId(root, 'A')).toBe('B')
  })
})

// --- findGridById ---

describe('findGridById', () => {
  it('returns null when no grid matches', () => {
    expect(findGridById(leaf('A'), 'g1')).toBeNull()
  })

  it('returns the matching grid', () => {
    const g = grid('g1', 1, 2, [1], [0.5, 0.5], leaf('A'), leaf('B'))
    expect(findGridById(g, 'g1')).toBe(g)
  })

  it('descends into splits to find a grid', () => {
    const g = grid('g1', 1, 2, [1], [0.5, 0.5], leaf('A'), leaf('B'))
    const root = split('s1', 'horizontal', [0.5, 0.5], leaf('X'), g)
    expect(findGridById(root, 'g1')).toBe(g)
  })

  it('descends into grids to find a nested grid', () => {
    const inner = grid('g2', 1, 1, [1], [1], leaf('A'))
    const outer = grid('g1', 1, 2, [1], [0.5, 0.5], leaf('B'), inner)
    expect(findGridById(outer, 'g2')).toBe(inner)
  })
})

// --- buildTilePredicateMap ---

describe('buildTilePredicateMap', () => {
  it('single leaf: closeMode=none for kind=\'main\'', () => {
    const root = leaf('A')
    const map = buildTilePredicateMap(root, 'main')
    expect(map.get('A')).toEqual({ closeMode: { kind: 'none' }, canSplit: true, canMakeGrid: true })
  })

  it('single leaf: closeMode=none for kind=\'floating\' too — the window chrome owns close', () => {
    const root = leaf('A')
    const map = buildTilePredicateMap(root, 'floating')
    expect(map.get('A')).toEqual({ closeMode: { kind: 'none' }, canSplit: true, canMakeGrid: true })
  })

  it('grid anchor leaf carries the grid id in closeMode', () => {
    const root = grid('g1', 1, 2, [1], [0.5, 0.5], leaf('A'), leaf('ANCHOR'))
    const map = buildTilePredicateMap(root, 'main')
    expect(map.get('ANCHOR')!.closeMode).toEqual({ kind: 'grid', gridId: 'g1' })
    expect(map.get('A')!.closeMode.kind).not.toBe('grid')
  })

  it('nested grids: anchor leaf carries the innermost grid id', () => {
    const innerGrid = grid('g2', 1, 2, [1], [0.5, 0.5], leaf('IL'), leaf('IR'))
    const outerGrid = grid('g1', 1, 2, [1], [0.5, 0.5], leaf('A'), innerGrid)
    const map = buildTilePredicateMap(outerGrid, 'main')
    expect(map.get('IR')!.closeMode).toEqual({ kind: 'grid', gridId: 'g2' })
  })

  it('grid id threads through a split inside the anchor cell', () => {
    const root = grid('g1', 1, 2, [1], [0.5, 0.5], leaf('A'), split('s1', 'vertical', [0.5, 0.5], leaf('L'), leaf('R')))
    const map = buildTilePredicateMap(root, 'main')
    expect(map.get('R')!.closeMode).toEqual({ kind: 'grid', gridId: 'g1' })
    expect(map.get('L')!.closeMode.kind).not.toBe('grid')
  })

  it('canSplit/canMakeGrid flip at MAX_DEPTH for kind=\'main\'', () => {
    let node: LayoutNodeLocal = leaf('deep')
    for (let i = 0; i < MAX_DEPTH; i++)
      node = split(`s${i}`, 'horizontal', [0.5, 0.5], leaf(`sib${i}`), node)
    const map = buildTilePredicateMap(node, 'main')
    expect(map.get('deep')).toMatchObject({ canSplit: false, canMakeGrid: false })
    expect(map.get('sib0')).toMatchObject({ canSplit: false, canMakeGrid: false })
    expect(map.get(`sib${MAX_DEPTH - 1}`)).toMatchObject({ canSplit: true, canMakeGrid: true })
  })

  it('kind=\'floating\': every leaf is splittable regardless of depth', () => {
    let node: LayoutNodeLocal = leaf('deep')
    for (let i = 0; i < MAX_DEPTH + 2; i++)
      node = split(`s${i}`, 'horizontal', [0.5, 0.5], leaf(`sib${i}`), node)
    const map = buildTilePredicateMap(node, 'floating')
    expect(map.get('deep')).toMatchObject({ canSplit: true, canMakeGrid: true })
  })

  it('grid cells: only the (0, lastCol) anchor cell gets closeMode=grid; others get none', () => {
    const root = grid('g1', 2, 3, [0.5, 0.5], [1 / 3, 1 / 3, 1 / 3], leaf('A'), leaf('B'), leaf('ANCHOR'), leaf('D'), leaf('E'), leaf('F'))
    const map = buildTilePredicateMap(root, 'main')
    expect(map.get('ANCHOR')!.closeMode.kind).toBe('grid')
    for (const id of ['A', 'B', 'D', 'E', 'F'])
      expect(map.get(id)!.closeMode.kind).toBe('none')
  })

  it('vertical-divider split inside the top-right cell: only the rightmost descendant is the anchor', () => {
    const root = grid('g1', 1, 2, [1], [0.5, 0.5], leaf('A'), split('s1', 'vertical', [0.5, 0.5], leaf('L'), leaf('R')))
    const map = buildTilePredicateMap(root, 'main')
    expect(map.get('R')!.closeMode.kind).toBe('grid')
    expect(map.get('L')!.closeMode.kind).toBe('tile')
    expect(map.get('A')!.closeMode.kind).toBe('none')
  })

  it('horizontal-divider split inside the top-right cell: only the topmost descendant is the anchor', () => {
    const root = grid('g1', 1, 2, [1], [0.5, 0.5], leaf('A'), split('s1', 'horizontal', [0.5, 0.5], leaf('TOP'), leaf('BOT')))
    const map = buildTilePredicateMap(root, 'main')
    expect(map.get('TOP')!.closeMode.kind).toBe('grid')
    expect(map.get('BOT')!.closeMode.kind).toBe('tile')
  })

  it('split inside a NON-anchor cell does not make any descendant an anchor of the outer grid', () => {
    const root = grid('g1', 1, 2, [1], [0.5, 0.5], split('s1', 'horizontal', [0.5, 0.5], leaf('L'), leaf('R')), leaf('ANCHOR'))
    const map = buildTilePredicateMap(root, 'main')
    expect(map.get('ANCHOR')!.closeMode.kind).toBe('grid')
    expect(map.get('L')!.closeMode.kind).toBe('tile')
    expect(map.get('R')!.closeMode.kind).toBe('tile')
  })

  it('nested grids: inner grid\'s top-right leaf is a close anchor', () => {
    const innerGrid = grid('g2', 1, 2, [1], [0.5, 0.5], leaf('IL'), leaf('IR'))
    const outerGrid = grid('g1', 1, 2, [1], [0.5, 0.5], leaf('A'), innerGrid)
    const map = buildTilePredicateMap(outerGrid, 'main')
    expect(map.get('IR')!.closeMode.kind).toBe('grid')
    expect(map.get('IL')!.closeMode.kind).toBe('none')
    expect(map.get('A')!.closeMode.kind).toBe('none')
  })

  it('classifies every leaf in a complex fixture (split-of-grid-of-split + nested grid)', () => {
    const root = split('root', 'vertical', [0.4, 0.6], grid('g1', 2, 2, [0.5, 0.5], [0.5, 0.5], leaf('A1'), split('sNa', 'vertical', [0.5, 0.5], leaf('NaL'), leaf('NaR')), leaf('A3'), leaf('A4')), split('s2', 'horizontal', [0.5, 0.5], leaf('B1'), grid('g2', 1, 2, [1], [0.5, 0.5], leaf('C1'), leaf('C2'))))
    const map = buildTilePredicateMap(root, 'main')
    const expected: Record<string, TilePredicates> = {
      A1: { closeMode: { kind: 'none' }, canSplit: true, canMakeGrid: true },
      NaL: { closeMode: { kind: 'tile' }, canSplit: false, canMakeGrid: false },
      NaR: { closeMode: { kind: 'grid', gridId: 'g1' }, canSplit: false, canMakeGrid: false },
      A3: { closeMode: { kind: 'none' }, canSplit: true, canMakeGrid: true },
      A4: { closeMode: { kind: 'none' }, canSplit: true, canMakeGrid: true },
      B1: { closeMode: { kind: 'tile' }, canSplit: true, canMakeGrid: true },
      C1: { closeMode: { kind: 'none' }, canSplit: false, canMakeGrid: false },
      C2: { closeMode: { kind: 'grid', gridId: 'g2' }, canSplit: false, canMakeGrid: false },
    }
    expect([...map.keys()].sort()).toEqual(Object.keys(expected).sort())
    for (const [id, want] of Object.entries(expected))
      expect(map.get(id), id).toEqual(want)
  })

  it('produces an entry for every leaf in the tree', () => {
    const root = split('s', 'horizontal', [0.5, 0.5], leaf('A'), grid('g', 2, 2, [0.5, 0.5], [0.5, 0.5], leaf('B'), leaf('C'), leaf('D'), leaf('E')))
    const map = buildTilePredicateMap(root, 'main')
    expect([...map.keys()].sort()).toEqual(['A', 'B', 'C', 'D', 'E'])
  })
})

// --- Focus helpers ---

describe('nextFocusAfterDisposal', () => {
  const root = split('s', 'horizontal', [0.5, 0.5], leaf('A'), leaf('B'))

  it('returns the replacement when current focus was inside the disposed set', () => {
    expect(nextFocusAfterDisposal(root, 'X', new Set(['X', 'Y']), 'A')).toBe('A')
  })

  it('falls back to firstLeafId when focus was disposed but no replacement is provided', () => {
    expect(nextFocusAfterDisposal(root, 'X', new Set(['X']), null)).toBe('A')
  })

  it('keeps the existing focus when it was not disposed and is still in the new tree', () => {
    expect(nextFocusAfterDisposal(root, 'B', new Set(['X']), 'A')).toBe('B')
  })

  it('falls back to firstLeafId when focus was not disposed but is no longer in the new tree', () => {
    expect(nextFocusAfterDisposal(root, 'orphan', new Set(['X']), 'A')).toBe('A')
  })

  it('returns firstLeafId when current focus is null, even when a replacement is provided', () => {
    expect(nextFocusAfterDisposal(root, null, new Set(['X']), 'B')).toBe('A')
  })

  it('returns firstLeafId when focus is null and replacement is null', () => {
    expect(nextFocusAfterDisposal(root, null, new Set(['X']), null)).toBe('A')
  })

  it('returns null when the new tree is empty (no leaves to fall back to)', () => {
    const empty = split('s', 'horizontal', [])
    expect(nextFocusAfterDisposal(empty, 'X', new Set(['X']), null)).toBeNull()
    expect(nextFocusAfterDisposal(empty, null, new Set([]), null)).toBeNull()
  })
})

describe('nextFocusEnsuringValid', () => {
  const root = split('s', 'horizontal', [0.5, 0.5], leaf('A'), leaf('B'))

  it('keeps the existing focus when it is still in the tree', () => {
    expect(nextFocusEnsuringValid(root, 'B')).toBe('B')
  })

  it('falls back to firstLeafId when focus is no longer in the tree', () => {
    expect(nextFocusEnsuringValid(root, 'gone')).toBe('A')
  })

  it('falls back to firstLeafId when focus is null', () => {
    expect(nextFocusEnsuringValid(root, null)).toBe('A')
  })

  it('returns null when the tree has no leaves', () => {
    const empty = split('s', 'horizontal', [])
    expect(nextFocusEnsuringValid(empty, null)).toBeNull()
    expect(nextFocusEnsuringValid(empty, 'gone')).toBeNull()
  })
})
