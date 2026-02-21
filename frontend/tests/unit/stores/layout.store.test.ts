import type { LayoutNodeLocal, LeafNode, SplitNode } from '~/stores/layout.store'
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
  createLayoutStore,
  fromProto,
  getAllTileIds,
  optimize,
  toProto,
} from '~/stores/layout.store'

// --- Helpers ---

function leaf(id: string): LeafNode {
  return { type: 'leaf', id }
}

function split(
  id: string,
  direction: 'horizontal' | 'vertical',
  ratios: number[],
  ...children: LayoutNodeLocal[]
): SplitNode {
  return { type: 'split', id, direction, ratios, children }
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

  it('splitTileHorizontal creates a horizontal split', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const originalTileId = store.focusedTileId()
      const newTileId = store.splitTileHorizontal(originalTileId)

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

  it('splitTileVertical creates a vertical split', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const originalTileId = store.focusedTileId()
      const newTileId = store.splitTileVertical(originalTileId)

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
      store.splitTileHorizontal(tileId)
      // Split the original tile again in same direction
      store.splitTileHorizontal(tileId)
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
      const newTileId = store.splitTileHorizontal(originalTileId)

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
      store.splitTileHorizontal(tileId)
      const newTile = store.splitTileHorizontal(tileId)
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
      const tileB = store.splitTileHorizontal(tileId)
      const tileC = store.splitTileVertical(tileB)

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
      const tileB = store.splitTileHorizontal(tileId)
      const tileC = store.splitTileVertical(tileB)

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
      store.splitTileHorizontal(tileId)

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
      const newTileId = store.splitTileHorizontal(tileId)

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
      const newTileId = store.splitTileHorizontal(tileId)

      store.setFocusedTile(newTileId)
      expect(store.focusedTileId()).toBe(newTileId)

      store.closeTile(newTileId)
      expect(store.focusedTileId()).toBe(tileId)
      dispose()
    })
  })

  it('canSplitTile returns true for root leaf (depth 0)', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileId = store.focusedTileId()
      expect(store.canSplitTile(tileId)).toBe(true)
      dispose()
    })
  })

  it('canSplitTile returns true for depth 1 tiles', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileId = store.focusedTileId()
      const newTileId = store.splitTileHorizontal(tileId)
      expect(store.canSplitTile(tileId)).toBe(true)
      expect(store.canSplitTile(newTileId)).toBe(true)
      dispose()
    })
  })

  it('canSplitTile returns true for depth 2 tiles', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileId = store.focusedTileId()
      const tileB = store.splitTileHorizontal(tileId)
      const tileC = store.splitTileVertical(tileB)
      // tileC is at depth 2 (root split -> inner split -> leaf)
      expect(store.canSplitTile(tileC)).toBe(true)
      dispose()
    })
  })

  it('canSplitTile returns false for depth 3 tiles', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileId = store.focusedTileId()
      const tileB = store.splitTileHorizontal(tileId)
      const tileC = store.splitTileVertical(tileB)
      const tileD = store.splitTileHorizontal(tileC)
      // tileD is at depth 3 (root -> split -> split -> split -> leaf)
      expect(store.canSplitTile(tileD)).toBe(false)
      dispose()
    })
  })

  it('same-direction split adds sibling with equal ratios instead of nesting', () => {
    createRoot((dispose) => {
      const store = createLayoutStore()
      const tileA = store.focusedTileId()
      const tileB = store.splitTileVertical(tileA)

      // Split B in same direction -> should add as sibling, not nest
      store.splitTileVertical(tileB)
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
      const tileB = store.splitTileVertical(tileA)

      // Split B in different direction -> should create nested split
      store.splitTileHorizontal(tileB)
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
      store.splitTileHorizontal(tileA)
      store.splitTileHorizontal(tileA)
      store.splitTileHorizontal(tileA)
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
      const tileB = store.splitTileVertical(tileA)
      const tileC = store.splitTileVertical(tileB)
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
      const tileB = store.splitTileVertical(tileA)
      const tileC = store.splitTileVertical(tileB)
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
      const tileB = store.splitTileVertical(tileA)
      expect(store.getAllTileIds().length).toBe(2)

      // Split B horizontally: vsplit[ A, hsplit[ B, C ] ]
      const tileC = store.splitTileHorizontal(tileB)
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
      const tileB = store.splitTileHorizontal(tileA)

      // hsplit[ A, vsplit[ B, C ] ]
      const tileC = store.splitTileVertical(tileB)

      // hsplit[ A, vsplit[ B, C, D ] ] — D added as sibling in same-direction split
      const tileD = store.splitTileVertical(tileC)
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
