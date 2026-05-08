import type { GridNode, LayoutNodeLocal, LeafNode, SplitNode, SplitOrientation, TilePredicates } from '~/stores/layout.store'
import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import {
  LayoutLeafSchema,
  LayoutNodeSchema,
  LayoutSplitSchema,
  SplitDirection,
} from '~/generated/leapmux/v1/workspace_pb'
import {
  addSiblingInSameDirectionSplit,
  buildTilePredicateMap,
  createLayoutStore,
  findGridById,
  findHeirTileId,
  fromProto,
  getAllTileIds,
  MAX_DEPTH,
  nextFocusAfterDisposal,
  nextFocusEnsuringValid,
  optimize,
  removeNode,
  replaceGridById,
  replaceNode,
  toProto,
} from '~/stores/layout.store'

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
  return Array.from({ length: n }).fill(1 / n)
}

function freshIdFactory() {
  let n = 0
  return () => {
    n++
    return `fresh-${n}`
  }
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
    // Pathological tree: closing tile has no sibling at its immediate parent;
    // findHeirTileId should walk up to the next ancestor.
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
    // grid cell holds a single leaf — no neighbor inside the cell, but the
    // grid is itself one half of an outer split.
    const innerGrid = grid('g1', 1, 1, [1], [1], leaf('A'))
    const root = split('s1', 'horizontal', [0.5, 0.5], innerGrid, leaf('B'))
    expect(findHeirTileId(root, 'A')).toBe('B')
  })
})

// --- optimize ---

describe('optimize', () => {
  it('returns leaf unchanged', () => {
    const node = leaf('A')
    expect(optimize(node)).toEqual(node)
  })

  it('unwraps single-child split', () => {
    const node = split('s1', 'horizontal', [1.0], leaf('A'))
    expect(optimize(node)).toEqual(leaf('A'))
  })

  it('flattens same-direction horizontal nesting', () => {
    // hsplit[ Leaf("A"), hsplit[ Leaf("B"), Leaf("C") ] ]
    // Should become hsplit[ Leaf("A"), Leaf("B"), Leaf("C") ]
    const inner = split('s2', 'horizontal', [0.5, 0.5], leaf('B'), leaf('C'))
    const node = split('s1', 'horizontal', [0.5, 0.5], leaf('A'), inner)
    const result = optimize(node)
    expect(result.type).toBe('split')
    if (result.type === 'split') {
      expect(result.direction).toBe('horizontal')
      expect(result.children.length).toBe(3)
      expect((result.children[0] as LeafNode).id).toBe('A')
      expect((result.children[1] as LeafNode).id).toBe('B')
      expect((result.children[2] as LeafNode).id).toBe('C')
      // Ratios: parent[0]=0.5 stays, parent[1]=0.5 * child[0]=0.5 = 0.25, parent[1]=0.5 * child[1]=0.5 = 0.25
      expect(result.ratios).toEqual([0.5, 0.25, 0.25])
    }
  })

  it('flattens same-direction vertical nesting', () => {
    // vsplit[ Leaf("A"), vsplit[ Leaf("B"), Leaf("C") ] ]
    // Should become vsplit[ Leaf("A"), Leaf("B"), Leaf("C") ]
    const inner = split('s2', 'vertical', [0.5, 0.5], leaf('B'), leaf('C'))
    const node = split('s1', 'vertical', [0.5, 0.5], leaf('A'), inner)
    const result = optimize(node)
    expect(result.type).toBe('split')
    if (result.type === 'split') {
      expect(result.direction).toBe('vertical')
      expect(result.children.length).toBe(3)
      expect(result.ratios).toEqual([0.5, 0.25, 0.25])
    }
  })

  it('does not flatten different direction nesting', () => {
    // hsplit[ Leaf("A"), vsplit[ Leaf("B"), Leaf("C") ] ]
    // Should remain unchanged (different directions)
    const inner = split('s2', 'vertical', [0.5, 0.5], leaf('B'), leaf('C'))
    const node = split('s1', 'horizontal', [0.5, 0.5], leaf('A'), inner)
    const result = optimize(node)
    expect(result.type).toBe('split')
    if (result.type === 'split') {
      expect(result.direction).toBe('horizontal')
      expect(result.children.length).toBe(2)
      expect(result.children[1]).toEqual(inner)
    }
  })

  it('no-op on already optimized layout', () => {
    const node = split('s1', 'horizontal', [0.33, 0.34, 0.33], leaf('A'), leaf('B'), leaf('C'))
    const result = optimize(node)
    expect(result).toEqual(node)
  })

  it('recursively optimizes nested single-child inside a split', () => {
    // hsplit[ Leaf("A"), hsplit[ Leaf("B") ] ]
    // Inner single-child should be unwrapped to just Leaf("B"), then flattened
    const inner = split('s2', 'horizontal', [1.0], leaf('B'))
    const node = split('s1', 'horizontal', [0.5, 0.5], leaf('A'), inner)
    const result = optimize(node)
    expect(result.type).toBe('split')
    if (result.type === 'split') {
      expect(result.children.length).toBe(2)
      expect(result.children[1]).toEqual(leaf('B'))
    }
  })

  it('flattens and then unwraps when result is single child', () => {
    // hsplit[ hsplit[ Leaf("A") ] ] → hsplit[ Leaf("A") ] → Leaf("A")
    const inner = split('s2', 'horizontal', [1.0], leaf('A'))
    const node = split('s1', 'horizontal', [1.0], inner)
    expect(optimize(node)).toEqual(leaf('A'))
  })

  it('returns the same reference when nothing needs changing (preserves SolidJS structural sharing)', () => {
    // Already-optimized tree: nothing to flatten or unwrap. Sibling subtrees
    // must keep identity so SolidJS doesn't dirty-mark unrelated panels.
    const root = split('root', 'horizontal', [0.5, 0.5], leaf('A'), grid('g1', 1, 2, [1], [0.5, 0.5], leaf('C1'), leaf('C2')))
    expect(optimize(root)).toBe(root)
  })

  it('preserves identity of unchanged sibling subtrees during a deep optimize', () => {
    // The right sibling has a flatten-able nested split; the left sibling is
    // already canonical and must come out by-reference unchanged.
    const stableLeft = grid('g1', 1, 2, [1], [0.5, 0.5], leaf('C1'), leaf('C2'))
    const flattenable = split('inner', 'horizontal', [0.5, 0.5], leaf('B1'), leaf('B2'))
    const root = split('root', 'horizontal', [0.5, 0.5], stableLeft, split('outer', 'horizontal', [0.5, 0.5], leaf('B0'), flattenable))
    const result = optimize(root)
    expect(result).not.toBe(root) // root mutated by flatten
    if (result.type !== 'split')
      throw new Error('expected split')
    expect(result.children[0]).toBe(stableLeft) // sibling kept identity
  })
})

// --- Proto conversion round-trip ---

describe('fromProto / toProto', () => {
  it('round-trips a single leaf', () => {
    const original = leaf('tile-1')
    const proto = toProto(original)
    const restored = fromProto(proto)
    expect(restored).toEqual(original)
  })

  it('round-trips a horizontal split', () => {
    const original = split('s1', 'horizontal', [0.5, 0.5], leaf('A'), leaf('B'))
    const proto = toProto(original)
    const restored = fromProto(proto)
    expect(restored).toEqual(original)
  })

  it('round-trips a vertical split', () => {
    const original = split('s1', 'vertical', [0.5, 0.5], leaf('A'), leaf('B'))
    const proto = toProto(original)
    const restored = fromProto(proto)
    expect(restored).toEqual(original)
  })

  it('round-trips nested split layout', () => {
    const inner = split('s2', 'vertical', [0.5, 0.5], leaf('B'), leaf('C'))
    const original = split('s1', 'horizontal', [0.33, 0.34, 0.33], leaf('A'), inner, leaf('D'))
    const proto = toProto(original)
    const restored = fromProto(proto)
    expect(restored).toEqual(original)
  })

  it('handles proto leaf node', () => {
    const protoNode = create(LayoutNodeSchema, {
      node: { case: 'leaf', value: create(LayoutLeafSchema, { id: 'x' }) },
    })
    expect(fromProto(protoNode)).toEqual({ type: 'leaf', id: 'x' })
  })

  it('handles proto split node', () => {
    const protoNode = create(LayoutNodeSchema, {
      node: {
        case: 'split',
        value: create(LayoutSplitSchema, {
          id: 's1',
          direction: SplitDirection.HORIZONTAL,
          ratios: [0.5, 0.5],
          children: [
            create(LayoutNodeSchema, { node: { case: 'leaf', value: create(LayoutLeafSchema, { id: 'A' }) } }),
            create(LayoutNodeSchema, { node: { case: 'leaf', value: create(LayoutLeafSchema, { id: 'B' }) } }),
          ],
        }),
      },
    })
    const result = fromProto(protoNode)
    expect(result.type).toBe('split')
    if (result.type === 'split') {
      expect(result.id).toBe('s1')
      expect(result.direction).toBe('horizontal')
      expect(result.ratios).toEqual([0.5, 0.5])
      expect(result.children.length).toBe(2)
    }
  })

  it('handles proto vertical split node', () => {
    const protoNode = create(LayoutNodeSchema, {
      node: {
        case: 'split',
        value: create(LayoutSplitSchema, {
          id: 's1',
          direction: SplitDirection.VERTICAL,
          ratios: [0.5, 0.5],
          children: [
            create(LayoutNodeSchema, { node: { case: 'leaf', value: create(LayoutLeafSchema, { id: 'A' }) } }),
            create(LayoutNodeSchema, { node: { case: 'leaf', value: create(LayoutLeafSchema, { id: 'B' }) } }),
          ],
        }),
      },
    })
    const result = fromProto(protoNode)
    expect(result.type).toBe('split')
    if (result.type === 'split') {
      expect(result.direction).toBe('vertical')
    }
  })
})

// --- createLayoutStore ---

describe('createLayoutStore', () => {
  it('initializes with a single leaf', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      expect(store.state.root.type).toBe('leaf')
      expect(store.getAllTileIds().length).toBe(1)
      dispose()
    })
  })

  it('initSingleTile creates a new single leaf', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const oldId = store.focusedTileId()
      const newId = store.initSingleTile()
      expect(newId).not.toBe(oldId)
      expect(store.state.root.type).toBe('leaf')
      expect(store.focusedTileId()).toBe(newId)
      dispose()
    })
  })

  it('splitTile horizontal creates a horizontal split', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const originalTileId = store.focusedTileId()
      const newTileId = store.splitTile(originalTileId, 'horizontal')!

      expect(store.getAllTileIds().length).toBe(2)
      expect(store.getAllTileIds()).toContain(originalTileId)
      expect(store.getAllTileIds()).toContain(newTileId)

      const root = store.state.root
      expect(root.type).toBe('split')
      if (root.type === 'split') {
        expect(root.direction).toBe('horizontal')
        expect(root.ratios).toEqual([0.5, 0.5])
      }
      dispose()
    })
  })

  it('splitTile vertical creates a vertical split', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const originalTileId = store.focusedTileId()
      const newTileId = store.splitTile(originalTileId, 'vertical')!

      expect(store.getAllTileIds().length).toBe(2)
      expect(store.getAllTileIds()).toContain(originalTileId)
      expect(store.getAllTileIds()).toContain(newTileId)

      const root = store.state.root
      expect(root.type).toBe('split')
      if (root.type === 'split') {
        expect(root.direction).toBe('vertical')
        expect(root.ratios).toEqual([0.5, 0.5])
      }
      dispose()
    })
  })

  it('nested same-direction splits are flattened', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileId = store.focusedTileId()
      store.splitTile(tileId, 'horizontal')
      // Split the original tile again in same direction
      store.splitTile(tileId, 'horizontal')
      expect(store.getAllTileIds().length).toBe(3)

      // Should be flattened to a single horizontal split with 3 children
      const root = store.state.root
      expect(root.type).toBe('split')
      if (root.type === 'split') {
        expect(root.direction).toBe('horizontal')
        expect(root.children.length).toBe(3)
      }
      dispose()
    })
  })

  it('closeTile in 2-child split collapses to single leaf', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const originalTileId = store.focusedTileId()
      const newTileId = store.splitTile(originalTileId, 'horizontal')!

      expect(store.getAllTileIds().length).toBe(2)

      store.closeTile(newTileId)
      expect(store.getAllTileIds().length).toBe(1)
      expect(store.getAllTileIds()).toContain(originalTileId)
      expect(store.state.root.type).toBe('leaf')
      dispose()
    })
  })

  it('closeTile in 3-child split becomes 2-child split', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileId = store.focusedTileId()
      // Split twice to get 3 tiles (same direction flattening)
      store.splitTile(tileId, 'horizontal')
      const newTile = store.splitTile(tileId, 'horizontal')!
      expect(store.getAllTileIds().length).toBe(3)

      // Close one tile
      store.closeTile(newTile)
      expect(store.getAllTileIds().length).toBe(2)
      dispose()
    })
  })

  it('closeTile in nested splits preserves structure', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileId = store.focusedTileId()

      // Create: hsplit[ A, vsplit[ B, C ] ]
      const tileB = store.splitTile(tileId, 'horizontal')!
      const tileC = store.splitTile(tileB, 'vertical')!

      expect(store.getAllTileIds().length).toBe(3)

      // Close C -> hsplit[ A, B ]
      store.closeTile(tileC)
      expect(store.getAllTileIds().length).toBe(2)
      expect(store.getAllTileIds()).toContain(tileId)
      expect(store.getAllTileIds()).toContain(tileB)
      dispose()
    })
  })

  it('closeTile in nested splits collapses to single leaf', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileId = store.focusedTileId()

      // Create: hsplit[ A, vsplit[ B, C ] ]
      const tileB = store.splitTile(tileId, 'horizontal')!
      const tileC = store.splitTile(tileB, 'vertical')!

      store.closeTile(tileC)
      store.closeTile(tileB)
      expect(store.getAllTileIds().length).toBe(1)
      expect(store.getAllTileIds()).toContain(tileId)
      expect(store.state.root.type).toBe('leaf')
      dispose()
    })
  })

  it('updateRatios modifies ratios on a specific split', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileId = store.focusedTileId()
      store.splitTile(tileId, 'horizontal')

      const root = store.state.root
      if (root.type === 'split') {
        store.updateRatios(root.id, [0.3, 0.7])
        expect(store.state.root.type).toBe('split')
        if (store.state.root.type === 'split') {
          expect(store.state.root.ratios).toEqual([0.3, 0.7])
        }
      }
      dispose()
    })
  })

  it('setFocusedTile changes the focused tile', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileId = store.focusedTileId()
      const newTileId = store.splitTile(tileId, 'horizontal')!

      store.setFocusedTile(newTileId)
      expect(store.focusedTileId()).toBe(newTileId)
      dispose()
    })
  })

  it('setLayout replaces the root and updates focused tile', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const newLayout = split('s1', 'horizontal', [0.5, 0.5], leaf('A'), leaf('B'))
      store.setLayout(newLayout)
      expect(store.state.root).toEqual(newLayout)
      expect(store.focusedTileId()).toBe('A') // First tile becomes focused
      dispose()
    })
  })

  it('fromProto sets layout from proto node', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const protoNode = create(LayoutNodeSchema, {
        node: { case: 'leaf', value: create(LayoutLeafSchema, { id: 'proto-tile' }) },
      })
      store.fromProto(protoNode)
      expect(store.state.root.type).toBe('leaf')
      expect((store.state.root as LeafNode).id).toBe('proto-tile')
      dispose()
    })
  })

  it('focusedTileId falls back to first tile when focused tile is removed', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileId = store.focusedTileId()
      const newTileId = store.splitTile(tileId, 'horizontal')!

      store.setFocusedTile(newTileId)
      expect(store.focusedTileId()).toBe(newTileId)

      store.closeTile(newTileId)
      expect(store.focusedTileId()).toBe(tileId)
      dispose()
    })
  })

  it('same-direction split adds sibling with equal ratios instead of nesting', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileA = store.focusedTileId()
      const tileB = store.splitTile(tileA, 'vertical')!

      // Split B in same direction -> should add as sibling, not nest
      store.splitTile(tileB, 'vertical')
      expect(store.getAllTileIds().length).toBe(3)

      const root = store.state.root
      expect(root.type).toBe('split')
      if (root.type === 'split') {
        expect(root.direction).toBe('vertical')
        expect(root.children.length).toBe(3)
        // All three should be leaves
        expect(root.children.every(c => c.type === 'leaf')).toBe(true)
        // Ratios should be equal (1/3 each)
        const expectedRatio = 1 / 3
        for (const r of root.ratios) {
          expect(r).toBeCloseTo(expectedRatio)
        }
      }
      dispose()
    })
  })

  it('different-direction split creates nested split', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileA = store.focusedTileId()
      const tileB = store.splitTile(tileA, 'vertical')!

      // Split B in different direction -> should create nested split
      store.splitTile(tileB, 'horizontal')
      expect(store.getAllTileIds().length).toBe(3)

      const root = store.state.root
      expect(root.type).toBe('split')
      if (root.type === 'split') {
        expect(root.direction).toBe('vertical')
        expect(root.children.length).toBe(2)
        // First child is leaf A
        expect(root.children[0].type).toBe('leaf')
        // Second child is a horizontal split containing B and C
        expect(root.children[1].type).toBe('split')
        if (root.children[1].type === 'split') {
          expect(root.children[1].direction).toBe('horizontal')
          expect(root.children[1].children.length).toBe(2)
        }
      }
      dispose()
    })
  })

  it('same-direction split three times results in 4 flat children', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileA = store.focusedTileId()
      store.splitTile(tileA, 'horizontal')
      store.splitTile(tileA, 'horizontal')
      store.splitTile(tileA, 'horizontal')
      expect(store.getAllTileIds().length).toBe(4)

      const root = store.state.root
      expect(root.type).toBe('split')
      if (root.type === 'split') {
        expect(root.direction).toBe('horizontal')
        expect(root.children.length).toBe(4)
        expect(root.children.every(c => c.type === 'leaf')).toBe(true)
      }
      dispose()
    })
  })

  it('closing tile in flattened split renormalizes ratios', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileA = store.focusedTileId()
      const tileB = store.splitTile(tileA, 'vertical')!
      const tileC = store.splitTile(tileB, 'vertical')!
      expect(store.getAllTileIds().length).toBe(3)

      // Close the rightmost tile
      store.closeTile(tileC)
      expect(store.getAllTileIds().length).toBe(2)

      const root = store.state.root
      expect(root.type).toBe('split')
      if (root.type === 'split') {
        expect(root.children.length).toBe(2)
        // Ratios should be renormalized to sum to 1
        const sum = root.ratios.reduce((a, b) => a + b, 0)
        expect(sum).toBeCloseTo(1)
      }
      dispose()
    })
  })

  it('closing middle tile in flattened split preserves remaining tiles', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileA = store.focusedTileId()
      const tileB = store.splitTile(tileA, 'vertical')!
      const tileC = store.splitTile(tileB, 'vertical')!
      expect(store.getAllTileIds().length).toBe(3)

      // Close the middle tile
      store.closeTile(tileB)
      expect(store.getAllTileIds().length).toBe(2)
      expect(store.getAllTileIds()).toContain(tileA)
      expect(store.getAllTileIds()).toContain(tileC)
      dispose()
    })
  })

  it('mixed-direction split then close collapses nested split correctly', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileA = store.focusedTileId()

      // Split vertically: vsplit[ A, B ]
      const tileB = store.splitTile(tileA, 'vertical')!
      expect(store.getAllTileIds().length).toBe(2)

      // Split B horizontally: vsplit[ A, hsplit[ B, C ] ]
      const tileC = store.splitTile(tileB, 'horizontal')!
      expect(store.getAllTileIds().length).toBe(3)

      const root3 = store.state.root
      expect(root3.type).toBe('split')
      if (root3.type === 'split') {
        expect(root3.direction).toBe('vertical')
        expect(root3.children.length).toBe(2)
        expect(root3.children[0].type).toBe('leaf')
        expect(root3.children[1].type).toBe('split')
        if (root3.children[1].type === 'split') {
          expect(root3.children[1].direction).toBe('horizontal')
          expect(root3.children[1].children.length).toBe(2)
        }
      }

      // Close B: vsplit[ A, C ]
      store.closeTile(tileB)
      expect(store.getAllTileIds().length).toBe(2)
      expect(store.getAllTileIds()).toContain(tileA)
      expect(store.getAllTileIds()).toContain(tileC)

      const root2 = store.state.root
      expect(root2.type).toBe('split')
      if (root2.type === 'split') {
        expect(root2.direction).toBe('vertical')
        expect(root2.children.length).toBe(2)
        expect(root2.children.every(c => c.type === 'leaf')).toBe(true)
        // Ratios should be [0.5, 0.5]
        expect(root2.ratios[0]).toBeCloseTo(0.5)
        expect(root2.ratios[1]).toBeCloseTo(0.5)
      }

      // Close C: single leaf A
      store.closeTile(tileC)
      expect(store.getAllTileIds().length).toBe(1)
      expect(store.getAllTileIds()).toContain(tileA)
      expect(store.state.root.type).toBe('leaf')
      dispose()
    })
  })

  it('nested same-direction split produces flat children via addSiblingInSameDirectionSplit', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileA = store.focusedTileId()

      // hsplit[ A, B ]
      const tileB = store.splitTile(tileA, 'horizontal')!

      // hsplit[ A, vsplit[ B, C ] ]
      const tileC = store.splitTile(tileB, 'vertical')!

      // hsplit[ A, vsplit[ B, C, D ] ] — D added as sibling in same-direction split
      const tileD = store.splitTile(tileC, 'vertical')!
      expect(store.getAllTileIds().length).toBe(4)

      const root = store.state.root
      expect(root.type).toBe('split')
      if (root.type === 'split') {
        expect(root.direction).toBe('horizontal')
        expect(root.children.length).toBe(2)
        expect(root.children[0].type).toBe('leaf') // A

        const inner = root.children[1]
        expect(inner.type).toBe('split')
        if (inner.type === 'split') {
          expect(inner.direction).toBe('vertical')
          // Should be flat 3-child split, not nested
          expect(inner.children.length).toBe(3)
          expect(inner.children.every(c => c.type === 'leaf')).toBe(true)
          // Equal ratios
          expect(inner.ratios[0]).toBeCloseTo(1 / 3)
          expect(inner.ratios[1]).toBeCloseTo(1 / 3)
          expect(inner.ratios[2]).toBeCloseTo(1 / 3)
        }
      }

      // Verify all tile IDs present
      expect(store.getAllTileIds()).toContain(tileA)
      expect(store.getAllTileIds()).toContain(tileB)
      expect(store.getAllTileIds()).toContain(tileC)
      expect(store.getAllTileIds()).toContain(tileD)
      dispose()
    })
  })
})

// --- Grid traversal helpers ---

describe('getAllTileIds with grids', () => {
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

describe('replaceNode through grids', () => {
  it('replaces a leaf inside a grid cell', () => {
    const g = grid('g1', 1, 2, [1.0], equalRatios(2), leaf('A'), leaf('B'))
    const replaced = replaceNode(g, 'A', () => leaf('Z'))
    expect(replaced).toEqual(grid('g1', 1, 2, [1.0], equalRatios(2), leaf('Z'), leaf('B')))
  })

  it('returns the same grid id and shape when no leaf matches', () => {
    const g = grid('g1', 1, 2, [1.0], equalRatios(2), leaf('A'), leaf('B'))
    const out = replaceNode(g, 'missing', () => leaf('Z'))
    expect(out).toEqual(g)
  })

  it('returns the same reference when no leaf matches (preserves structural sharing)', () => {
    const root = split('s1', 'horizontal', [0.5, 0.5], leaf('A'), grid('g1', 1, 2, [1], [0.5, 0.5], leaf('C1'), leaf('C2')))
    expect(replaceNode(root, 'missing', () => leaf('Z'))).toBe(root)
  })

  it('preserves identity of unchanged sibling subtrees during a deep replace', () => {
    const stableRight = grid('g1', 1, 2, [1], [0.5, 0.5], leaf('C1'), leaf('C2'))
    const root = split('s1', 'horizontal', [0.5, 0.5], leaf('A'), stableRight)
    const out = replaceNode(root, 'A', () => leaf('Z'))
    if (out.type !== 'split')
      throw new Error('expected split')
    expect(out.children[1]).toBe(stableRight)
  })
})

describe('addSiblingInSameDirectionSplit through grids', () => {
  it('descends into grid cells to find a same-direction split', () => {
    const innerSplit = split('s2', 'horizontal', [0.5, 0.5], leaf('X'), leaf('Y'))
    const root = grid('g1', 1, 2, [1.0], equalRatios(2), innerSplit, leaf('B'))
    const [out, found] = addSiblingInSameDirectionSplit(root, 'X', 'NEW', 'horizontal')
    expect(found).toBe(true)
    if (out.type !== 'grid')
      throw new Error('expected grid')
    const cell = out.cells[0]
    if (cell.type !== 'split')
      throw new Error('expected split')
    expect(cell.children.length).toBe(3)
    expect(cell.children.map(c => (c as LeafNode).id)).toEqual(['X', 'NEW', 'Y'])
  })

  it('never inserts a sibling at the grid level', () => {
    const root = grid('g1', 1, 2, [1.0], equalRatios(2), leaf('A'), leaf('B'))
    const [out, found] = addSiblingInSameDirectionSplit(root, 'A', 'NEW', 'horizontal')
    expect(found).toBe(false)
    expect(out).toEqual(root)
  })
})

describe('removeNode with grid cells', () => {
  it('replaces an emptied cell with a fresh leaf instead of collapsing the grid', () => {
    const cellSplit = split('s1', 'horizontal', [0.5, 0.5], leaf('X'), leaf('Y'))
    const g = grid('g1', 1, 2, [1.0], equalRatios(2), cellSplit, leaf('B'))
    const factory = freshIdFactory()
    // Remove both X and Y so the cell is empty and should become a fresh leaf.
    const afterX = removeNode(g, 'X', factory)
    if (!afterX || afterX.type !== 'grid')
      throw new Error('expected grid')
    // First removal: split collapses to a single leaf (Y).
    expect(afterX.cells[0]).toEqual(leaf('Y'))
    const afterY = removeNode(afterX, 'Y', factory)
    if (!afterY || afterY.type !== 'grid')
      throw new Error('expected grid')
    expect(afterY.cells[0].type).toBe('leaf')
    expect((afterY.cells[0] as LeafNode).id).toBe('fresh-1')
    // Outer cell B is untouched.
    expect(afterY.cells[1]).toEqual(leaf('B'))
  })

  it('does nothing when removing a non-existent leaf in a grid', () => {
    const g = grid('g1', 1, 2, [1.0], equalRatios(2), leaf('A'), leaf('B'))
    const out = removeNode(g, 'missing', freshIdFactory())
    expect(out).toEqual(g)
  })

  // Identity preservation: a no-op removeNode must return the SAME reference,
  // not a freshly-spread copy. Solid's `setState('root', sameRef)` short-
  // circuits subscriber emits when the root reference is unchanged; the
  // store's no-op short-circuits depend on this.
  it('preserves reference identity when no leaf matches (split + grid + nested)', () => {
    const tree = split(
      's',
      'horizontal',
      [0.5, 0.5],
      grid('g1', 1, 2, [1.0], equalRatios(2), leaf('A'), leaf('B')),
      split('s2', 'vertical', [0.5, 0.5], leaf('C'), leaf('D')),
    )
    expect(removeNode(tree, 'missing', freshIdFactory())).toBe(tree)
  })

  it('preserves reference identity of unchanged sibling subtrees during a deep remove', () => {
    // 3-child split so the surviving children stay inside a split (rather
    // than collapsing to a sole-survivor leaf), exercising the identity
    // preservation path.
    const siblingA = split('s2', 'vertical', [0.5, 0.5], leaf('X'), leaf('Y'))
    const siblingB = grid('g', 1, 2, [1.0], equalRatios(2), leaf('P'), leaf('Q'))
    const root = split(
      's',
      'horizontal',
      [1 / 3, 1 / 3, 1 / 3],
      leaf('A'),
      siblingA,
      siblingB,
    )
    const result = removeNode(root, 'A', freshIdFactory())
    if (!result || result.type !== 'split')
      throw new Error('expected split')
    // Both unchanged subtrees keep their identity; only the dropped child's
    // siblings get re-spread into the new children array.
    expect(result.children).toContain(siblingA)
    expect(result.children).toContain(siblingB)
  })
})

describe('optimize with grids', () => {
  it('leaves a canonical grid unchanged', () => {
    const g = grid('g1', 2, 2, equalRatios(2), equalRatios(2), leaf('A'), leaf('B'), leaf('C'), leaf('D'))
    expect(optimize(g)).toEqual(g)
  })

  it('optimises a sub-split inside a grid cell', () => {
    const wrapped = split('outer', 'horizontal', [1.0], leaf('A'))
    const g = grid('g1', 1, 2, [1.0], equalRatios(2), wrapped, leaf('B'))
    const out = optimize(g)
    if (out.type !== 'grid')
      throw new Error('expected grid')
    expect(out.cells[0]).toEqual(leaf('A'))
  })
})

describe('findGridById and replaceGridById', () => {
  it('finds a grid by id at any depth', () => {
    const g = grid('g1', 1, 2, [1.0], equalRatios(2), leaf('A'), leaf('B'))
    const root = split('s1', 'horizontal', [0.5, 0.5], leaf('Z'), g)
    expect(findGridById(root, 'g1')?.id).toBe('g1')
    expect(findGridById(root, 'missing')).toBeNull()
  })

  it('replaceGridById swaps the grid in place', () => {
    const g = grid('g1', 1, 2, [1.0], equalRatios(2), leaf('A'), leaf('B'))
    const root = split('s1', 'horizontal', [0.5, 0.5], leaf('Z'), g)
    const out = replaceGridById(root, 'g1', () => leaf('NEW'))
    if (out.type !== 'split')
      throw new Error('expected split')
    expect(out.children[1]).toEqual(leaf('NEW'))
  })
})

// --- Store-level grid actions ---

describe('layoutStore.makeGrid', () => {
  it('replaces a leaf with a 2×3 grid preserving the original tile id at (0,0)', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const root = store.state.root as LeafNode
      const original = root.id

      const { gridId, cellTileIds } = store.makeGrid(original, 2, 3)
      expect(gridId).toBeTruthy()
      expect(cellTileIds.length).toBe(6)
      expect(cellTileIds[0]).toBe(original)

      const newRoot = store.state.root
      if (newRoot.type !== 'grid')
        throw new Error('expected grid root')
      expect(newRoot.rows).toBe(2)
      expect(newRoot.cols).toBe(3)
      expect(newRoot.rowRatios.length).toBe(2)
      expect(newRoot.colRatios.length).toBe(3)
      expect(newRoot.rowRatios.reduce((a, b) => a + b, 0)).toBeCloseTo(1)
      expect(newRoot.colRatios.reduce((a, b) => a + b, 0)).toBeCloseTo(1)
      // (0,0) preserves the original tile id; others are unique
      expect((newRoot.cells[0] as LeafNode).id).toBe(original)
      const ids = newRoot.cells.map(c => (c as LeafNode).id)
      expect(new Set(ids).size).toBe(6)
      dispose()
    })
  })

  it('rejects rows/cols outside [1, 20]', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const root = store.state.root as LeafNode
      expect(() => store.makeGrid(root.id, 0, 2)).toThrow()
      expect(() => store.makeGrid(root.id, 2, 21)).toThrow()
      expect(() => store.makeGrid(root.id, 2.5, 2)).toThrow()
      dispose()
    })
  })
})

describe('layoutStore.removeGrid', () => {
  it('replaces a root grid with a fresh empty leaf and re-focuses it', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const original = (store.state.root as LeafNode).id
      const { gridId, cellTileIds } = store.makeGrid(original, 2, 2)
      store.setFocusedTile(cellTileIds[3])

      store.removeGrid(gridId)
      expect(store.state.root.type).toBe('leaf')
      const newId = (store.state.root as LeafNode).id
      // Focus must move to the replacement leaf because the previous focused
      // tile lived inside the removed grid.
      expect(store.state.focusedTileId).toBe(newId)
      expect(newId).not.toBe(original)
      dispose()
    })
  })

  it('collapses a 2-way split parent when its grid child is removed', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      // Start with a single tile, split horizontally to get [A, B].
      const tileA = (store.state.root as LeafNode).id
      const tileB = store.splitTile(tileA, 'horizontal')!
      // Turn B into a 2×2 grid.
      const { gridId } = store.makeGrid(tileB, 2, 2)
      // Remove that grid; the parent split should collapse to leaf A.
      store.removeGrid(gridId)
      expect(store.state.root.type).toBe('leaf')
      expect((store.state.root as LeafNode).id).toBe(tileA)
      dispose()
    })
  })

  it('replaces a grid that lives directly inside another grid with a fresh empty leaf', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileA = (store.state.root as LeafNode).id
      const { cellTileIds: outerCells } = store.makeGrid(tileA, 2, 2)
      // Make the (0, 1) cell an inner grid.
      const inner = store.makeGrid(outerCells[1], 2, 2)
      store.setFocusedTile(inner.cellTileIds[0])

      store.removeGrid(inner.gridId)
      // Outer grid is preserved; the (0,1) cell is now a fresh leaf.
      const outer = store.state.root
      if (outer.type !== 'grid')
        throw new Error('expected grid')
      const cell = outer.cells[1]
      expect(cell.type).toBe('leaf')
      expect((cell as LeafNode).id).not.toBe(outerCells[1])
      // Focus moves to the replacement leaf.
      expect(store.state.focusedTileId).toBe((cell as LeafNode).id)
      dispose()
    })
  })
})

describe('layoutStore.replaceGridWithLeaf', () => {
  it('replaces the grid with a new leaf and re-focuses it', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const original = (store.state.root as LeafNode).id
      const { gridId } = store.makeGrid(original, 2, 2)
      const newTileId = store.replaceGridWithLeaf(gridId)
      expect(store.state.root.type).toBe('leaf')
      expect((store.state.root as LeafNode).id).toBe(newTileId)
      expect(store.state.focusedTileId).toBe(newTileId)
      dispose()
    })
  })
})

describe('layoutStore grid ratios', () => {
  it('updates rowRatios when the new array is valid', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const original = (store.state.root as LeafNode).id
      const { gridId } = store.makeGrid(original, 2, 2)
      store.updateGridRatios(gridId, 'row', [0.3, 0.7])
      const root = store.state.root
      if (root.type !== 'grid')
        throw new Error('expected grid')
      expect(root.rowRatios).toEqual([0.3, 0.7])
      dispose()
    })
  })

  it('updates colRatios when the new array is valid', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const original = (store.state.root as LeafNode).id
      const { gridId } = store.makeGrid(original, 2, 2)
      store.updateGridRatios(gridId, 'col', [0.4, 0.6])
      const root = store.state.root
      if (root.type !== 'grid')
        throw new Error('expected grid')
      expect(root.colRatios).toEqual([0.4, 0.6])
      // Row ratios untouched.
      expect(root.rowRatios[0]).toBeCloseTo(0.5)
      expect(root.rowRatios[1]).toBeCloseTo(0.5)
      dispose()
    })
  })

  it('rejects bad rowRatios', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const original = (store.state.root as LeafNode).id
      const { gridId } = store.makeGrid(original, 2, 2)
      // Wrong length.
      store.updateGridRatios(gridId, 'row', [0.5, 0.3, 0.2])
      // Sum off.
      store.updateGridRatios(gridId, 'row', [0.2, 0.2])
      // Non-finite.
      store.updateGridRatios(gridId, 'row', [Number.NaN, 1.0])
      const root = store.state.root
      if (root.type !== 'grid')
        throw new Error('expected grid')
      // Original equal-share ratios are preserved.
      expect(root.rowRatios[0]).toBeCloseTo(0.5)
      expect(root.rowRatios[1]).toBeCloseTo(0.5)
      dispose()
    })
  })
})

describe('layoutStore.updateRatios through grids', () => {
  it('updates a split that lives inside a grid cell', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const root = (store.state.root as LeafNode).id
      const { cellTileIds } = store.makeGrid(root, 1, 2)
      // Split the (0,1) cell horizontally so a sub-split exists inside the grid.
      store.splitTile(cellTileIds[1], 'horizontal')
      // Locate the split id by walking the tree.
      let splitId: string | null = null
      const walk = (n: LayoutNodeLocal) => {
        if (n.type === 'split') {
          splitId = n.id
          return
        }
        if (n.type === 'grid') {
          for (const c of n.cells) walk(c)
        }
      }
      walk(store.state.root)
      expect(splitId).not.toBeNull()
      store.updateRatios(splitId!, [0.2, 0.8])
      let foundRatios: number[] = []
      const verify = (n: LayoutNodeLocal) => {
        if (n.type === 'split' && n.id === splitId) {
          foundRatios = n.ratios
          return
        }
        if (n.type === 'grid') {
          for (const c of n.cells) verify(c)
        }
      }
      verify(store.state.root)
      expect(foundRatios).toEqual([0.2, 0.8])
      dispose()
    })
  })
})

describe('layoutStore proto roundtrip with grids', () => {
  it('preserves a grid containing a sub-split and a nested grid', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const original = (store.state.root as LeafNode).id
      const { cellTileIds } = store.makeGrid(original, 2, 2)
      // Sub-split in (0,1)
      store.splitTile(cellTileIds[1], 'horizontal')
      // Nested grid in (1,0)
      store.makeGrid(cellTileIds[2], 1, 2)

      const proto = store.toProto()
      const round = fromProto(proto)
      // Compare structure deeply via toProto -> fromProto round trip.
      expect(getAllTileIds(round).sort()).toEqual(getAllTileIds(store.state.root).sort())
      dispose()
    })
  })
})

describe('layoutStore.owner()', () => {
  it('returns a LayoutOwner with all required methods', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const owner = store.owner()
      expect(typeof owner.collectTileIdsInGrid).toBe('function')
      expect(typeof owner.splitTile).toBe('function')
      expect(typeof owner.makeGrid).toBe('function')
      expect(typeof owner.removeGrid).toBe('function')
      expect(typeof owner.replaceGridWithLeaf).toBe('function')
      dispose()
    })
  })

  it('owner reads are lazy: an owner held across mutations sees fresh state', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const owner = store.owner() // captured before any mutation
      const t1 = (store.state.root as LeafNode).id
      const t2 = store.splitTile(t1, 'horizontal')!
      const { gridId } = store.makeGrid(t2, 2, 2)
      // Mutation observed through the owner without re-fetching it.
      expect(owner.collectTileIdsInGrid(gridId)).toHaveLength(4)
      dispose()
    })
  })

  it('owner().splitTile delegates to store.splitTile with the given direction', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const t1 = (store.state.root as LeafNode).id
      store.owner().splitTile(t1, 'horizontal')
      expect(getAllTileIds(store.state.root)).toHaveLength(2)
      const root = store.state.root as SplitNode
      expect(root.type).toBe('split')
      expect(root.direction).toBe('horizontal')
      dispose()
    })
  })

  it('makeGrid delegates and produces a grid', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const t1 = (store.state.root as LeafNode).id
      store.owner().makeGrid(t1, 2, 2)
      expect(store.state.root.type).toBe('grid')
      expect(getAllTileIds(store.state.root)).toHaveLength(4)
      dispose()
    })
  })

  it('removeGrid delegates: replaces root grid with a fresh leaf', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const t1 = (store.state.root as LeafNode).id
      const { gridId } = store.makeGrid(t1, 2, 2)
      const tilesBefore = getAllTileIds(store.state.root)
      expect(tilesBefore).toHaveLength(4)
      store.owner().removeGrid(gridId)
      // Root grid replaced with a single fresh leaf.
      expect(store.state.root.type).toBe('leaf')
      expect(getAllTileIds(store.state.root)).toHaveLength(1)
      dispose()
    })
  })

  it('replaceGridWithLeaf returns the new tile id and replaces the grid', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const t1 = (store.state.root as LeafNode).id
      const { gridId } = store.makeGrid(t1, 2, 2)
      const newTileId = store.owner().replaceGridWithLeaf(gridId)
      expect(newTileId).toBeTruthy()
      expect(store.state.root.type).toBe('leaf')
      expect(getAllTileIds(store.state.root)).toEqual([newTileId])
      dispose()
    })
  })

  it('returns a stable reference across calls and across structural mutations', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const owner1 = store.owner()
      const owner2 = store.owner()
      expect(owner2).toBe(owner1)
      // Reference must survive mutations: callers (TileRenderer) re-read
      // owner from the store after every event, and a fresh allocation per
      // call would defeat downstream identity-based memoization.
      const t1 = (store.state.root as LeafNode).id
      store.splitTile(t1, 'horizontal')
      expect(store.owner()).toBe(owner1)
      dispose()
    })
  })
})

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
    // Outer grid's anchor cell is an h-split; rightmost child is the anchor leaf.
    const root = grid('g1', 1, 2, [1], [0.5, 0.5], leaf('A'), split('s1', 'horizontal', [0.5, 0.5], leaf('L'), leaf('R')))
    const map = buildTilePredicateMap(root, 'main')
    expect(map.get('R')!.closeMode).toEqual({ kind: 'grid', gridId: 'g1' })
    expect(map.get('L')!.closeMode.kind).not.toBe('grid')
  })

  it('canSplit/canMakeGrid flip at MAX_DEPTH for kind=\'main\'', () => {
    // Build a chain of MAX_DEPTH levels of nested splits. The OUTERMOST
    // sibling (last added) ends up at depth 1; deeper siblings ratchet
    // down to depth MAX_DEPTH. canSplit flips to false at depth MAX_DEPTH.
    let node: LayoutNodeLocal = leaf('deep')
    for (let i = 0; i < MAX_DEPTH; i++) {
      node = split(`s${i}`, 'horizontal', [0.5, 0.5], leaf(`sib${i}`), node)
    }
    const map = buildTilePredicateMap(node, 'main')
    expect(map.get('deep')).toMatchObject({ canSplit: false, canMakeGrid: false })
    expect(map.get('sib0')).toMatchObject({ canSplit: false, canMakeGrid: false }) // depth 3
    expect(map.get(`sib${MAX_DEPTH - 1}`)).toMatchObject({ canSplit: true, canMakeGrid: true }) // depth 1
  })

  it('kind=\'floating\': every leaf is splittable regardless of depth', () => {
    let node: LayoutNodeLocal = leaf('deep')
    for (let i = 0; i < MAX_DEPTH + 2; i++) {
      node = split(`s${i}`, 'horizontal', [0.5, 0.5], leaf(`sib${i}`), node)
    }
    const map = buildTilePredicateMap(node, 'floating')
    expect(map.get('deep')).toMatchObject({ canSplit: true, canMakeGrid: true })
  })

  it('grid cells: only the (0, lastCol) anchor cell gets closeMode=grid; others get none', () => {
    // 2x3 grid. Anchor cell index = 0 * cols + (cols - 1) = 2.
    const root = grid('g1', 2, 3, [0.5, 0.5], [1 / 3, 1 / 3, 1 / 3], leaf('A'), leaf('B'), leaf('ANCHOR'), leaf('D'), leaf('E'), leaf('F'))
    const map = buildTilePredicateMap(root, 'main')
    expect(map.get('ANCHOR')!.closeMode.kind).toBe('grid')
    for (const id of ['A', 'B', 'D', 'E', 'F']) {
      expect(map.get(id)!.closeMode.kind).toBe('none')
    }
  })

  it('h-split inside the top-right cell: only the rightmost descendant is the anchor', () => {
    // 1x2 grid where cells[1] = h-split(L, R). Outer-grid's anchor path
    // descends into cells[1], then into the h-split's last child (R). L is
    // a regular split-child (not anchor, not a direct grid cell) → 'tile'.
    // A is a direct grid cell (not anchor) → 'none'.
    const root = grid('g1', 1, 2, [1], [0.5, 0.5], leaf('A'), split('s1', 'horizontal', [0.5, 0.5], leaf('L'), leaf('R')))
    const map = buildTilePredicateMap(root, 'main')
    expect(map.get('R')!.closeMode.kind).toBe('grid')
    expect(map.get('L')!.closeMode.kind).toBe('tile')
    expect(map.get('A')!.closeMode.kind).toBe('none')
  })

  it('v-split inside the top-right cell: only the topmost descendant is the anchor', () => {
    const root = grid('g1', 1, 2, [1], [0.5, 0.5], leaf('A'), split('s1', 'vertical', [0.5, 0.5], leaf('TOP'), leaf('BOT')))
    const map = buildTilePredicateMap(root, 'main')
    expect(map.get('TOP')!.closeMode.kind).toBe('grid')
    expect(map.get('BOT')!.closeMode.kind).toBe('tile')
  })

  it('split inside a NON-anchor cell does not make any descendant an anchor of the outer grid', () => {
    // 1x2 grid where cells[0] (NOT top-right) = h-split(L, R). Neither L nor
    // R is an anchor of the outer grid. They aren't direct grid cells either,
    // so they fall back to the regular 'tile' rule (siblings exist).
    // cells[1] is the outer grid's direct anchor leaf.
    const root = grid('g1', 1, 2, [1], [0.5, 0.5], split('s1', 'horizontal', [0.5, 0.5], leaf('L'), leaf('R')), leaf('ANCHOR'))
    const map = buildTilePredicateMap(root, 'main')
    expect(map.get('ANCHOR')!.closeMode.kind).toBe('grid')
    expect(map.get('L')!.closeMode.kind).toBe('tile')
    expect(map.get('R')!.closeMode.kind).toBe('tile')
  })

  it('nested grids: inner grid\'s top-right leaf is a close anchor', () => {
    // outer-grid's cell[lastCol] = inner-grid. The inner grid spawns its
    // own top-right descent. The inner top-right leaf is anchored by
    // both grids — closeMode='grid'.
    const innerGrid = grid('g2', 1, 2, [1], [0.5, 0.5], leaf('IL'), leaf('IR'))
    const outerGrid = grid('g1', 1, 2, [1], [0.5, 0.5], leaf('A'), innerGrid)
    const map = buildTilePredicateMap(outerGrid, 'main')
    expect(map.get('IR')!.closeMode.kind).toBe('grid')
    // IL is a direct cell of the inner grid → 'none', not anchor.
    expect(map.get('IL')!.closeMode.kind).toBe('none')
    // A is a direct cell of the outer grid (not anchor) → 'none'.
    expect(map.get('A')!.closeMode.kind).toBe('none')
  })

  it('classifies every leaf in a complex fixture (split-of-grid-of-split + nested grid)', () => {
    // Tree structure (depths in parens):
    //   root split horizontal (0)
    //   ├─ g1 grid 2×2 (1)              ← (0,1) is the anchor cell
    //   │  ├─ A1   leaf      cells[0,0] (2)
    //   │  ├─ sNa  split h.  cells[0,1] (2)  ← anchor cell
    //   │  │  ├─ NaL leaf (3)
    //   │  │  └─ NaR leaf (3)            ← anchor for g1 (h-split → last child)
    //   │  ├─ A3   leaf      cells[1,0] (2)
    //   │  └─ A4   leaf      cells[1,1] (2)
    //   └─ s2 split vertical (1)
    //      ├─ B1   leaf (2)
    //      └─ g2   grid 1×2 (2)
    //         ├─ C1 leaf cells[0,0] (3)
    //         └─ C2 leaf cells[0,1] (3)  ← anchor for g2 (direct grid leaf at last col)
    // MAX_DEPTH = 3, so leaves at depth ≥ 3 have canSplit=false.
    const root = split('root', 'horizontal', [0.4, 0.6], grid('g1', 2, 2, [0.5, 0.5], [0.5, 0.5], leaf('A1'), split('sNa', 'horizontal', [0.5, 0.5], leaf('NaL'), leaf('NaR')), leaf('A3'), leaf('A4')), split('s2', 'vertical', [0.5, 0.5], leaf('B1'), grid('g2', 1, 2, [1], [0.5, 0.5], leaf('C1'), leaf('C2'))))
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
  // Used by both layout.store and floatingWindow.store after a disposal-style
  // mutation (close-tile / remove-grid / replace-grid-with-leaf) to compute
  // the new focused tile. The truth table below mirrors the six branches the
  // helper was built to consolidate from the prior duplicated implementations.

  const root = split('s', 'horizontal', [0.5, 0.5], leaf('A'), leaf('B'))

  it('returns the replacement when current focus was inside the disposed set', () => {
    const result = nextFocusAfterDisposal(root, 'X', new Set(['X', 'Y']), 'A')
    expect(result).toBe('A')
  })

  it('falls back to firstLeafId when focus was disposed but no replacement is provided', () => {
    const result = nextFocusAfterDisposal(root, 'X', new Set(['X']), null)
    expect(result).toBe('A')
  })

  it('keeps the existing focus when it was not disposed and is still in the new tree', () => {
    const result = nextFocusAfterDisposal(root, 'B', new Set(['X']), 'A')
    expect(result).toBe('B')
  })

  it('falls back to firstLeafId when focus was not disposed but is no longer in the new tree', () => {
    // e.g. focus pointed into a sibling subtree that was rebuilt by `optimize`.
    const result = nextFocusAfterDisposal(root, 'orphan', new Set(['X']), 'A')
    expect(result).toBe('A')
  })

  it('returns firstLeafId when current focus is null, even when a replacement is provided', () => {
    // Replacement only fires when focus was inside the disposed set; null was
    // never inside any set, so the helper falls through to the default.
    const result = nextFocusAfterDisposal(root, null, new Set(['X']), 'B')
    expect(result).toBe('A')
  })

  it('returns firstLeafId when focus is null and replacement is null', () => {
    const result = nextFocusAfterDisposal(root, null, new Set(['X']), null)
    expect(result).toBe('A')
  })

  it('returns null when the new tree is empty (no leaves to fall back to)', () => {
    // Construct a degenerate root whose firstLeafId is undefined: a 1×1 grid
    // with a leaf cell still produces a leaf, so use an empty split here.
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
