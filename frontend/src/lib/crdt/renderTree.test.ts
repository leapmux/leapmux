import type { RenderTree } from './project'
import type { LocalTreeCache } from './renderTree'
import { describe, expect, it } from 'vitest'
import { NodeKind } from '~/generated/proto/leapmux/v1/user_crdt_pb'
import { SplitDirection } from '~/generated/proto/leapmux/v1/workspace_pb'
import { renderTreeToLocal } from './renderTree'

function tree(over: Partial<RenderTree>): RenderTree {
  return {
    nodeId: 'n',
    kind: NodeKind.LEAF,
    direction: 0,
    ratios: [],
    rows: 0,
    cols: 0,
    rowRatios: [],
    colRatios: [],
    children: [],
    ...over,
  }
}

/**
 * One cache for the file, standing in for the module-level `sharedTrees` these
 * calls used to reach through a default argument. `renderTreeToLocal` now makes
 * every caller name its cache -- a node handed out of a SHARED one must not be
 * written to -- and the memoization tests below need the calls to share one.
 * Keyed by `RenderTree` identity, and each test builds its own trees, so there
 * is no bleed between them.
 */
const cache: LocalTreeCache = new WeakMap()
const toLocal = (rt?: RenderTree) => renderTreeToLocal(rt, cache)

describe('renderTreeToLocal', () => {
  it('returns null for an absent tree and for the empty node id', () => {
    expect(toLocal(undefined)).toBeNull()
    // `project()` returns one shared empty tree for an unseeded workspace root
    // and for a grid cell no live child claims.
    expect(toLocal(tree({ nodeId: '' }))).toBeNull()
  })

  it('converts a leaf', () => {
    expect(toLocal(tree({ nodeId: 'leaf' }))).toEqual({ type: 'leaf', id: 'leaf' })
  })

  it('maps split direction by the divider-line convention', () => {
    const vertical = toLocal(tree({
      nodeId: 's',
      kind: NodeKind.SPLIT,
      direction: SplitDirection.VERTICAL,
      ratios: [0.3, 0.7],
      children: [tree({ nodeId: 'a' }), tree({ nodeId: 'b' })],
    }))
    expect(vertical).toEqual({
      type: 'split',
      id: 's',
      direction: 'vertical',
      ratios: [0.3, 0.7],
      children: [{ type: 'leaf', id: 'a' }, { type: 'leaf', id: 'b' }],
    })
    const horizontal = toLocal(tree({
      nodeId: 's2',
      kind: NodeKind.SPLIT,
      direction: SplitDirection.HORIZONTAL,
      children: [tree({ nodeId: 'a2' })],
    }))
    expect(horizontal).toMatchObject({ direction: 'horizontal' })
  })

  it('names an unclaimed grid cell after its parent and index, not after the cell', () => {
    const local = toLocal(tree({
      nodeId: 'g',
      kind: NodeKind.GRID,
      rows: 1,
      cols: 2,
      rowRatios: [1],
      colRatios: [0.5, 0.5],
      // The second cell is the shared empty tree, which converts to null.
      children: [tree({ nodeId: 'c00' }), tree({ nodeId: '' })],
    }))
    expect(local).toEqual({
      type: 'grid',
      id: 'g',
      rows: 1,
      cols: 2,
      rowRatios: [1],
      colRatios: [0.5, 0.5],
      // Keyed off the PARENT plus the index, which is exactly why `project()` can
      // share one empty-cell object across every grid without two holes
      // colliding on the same placeholder id.
      cells: [{ type: 'leaf', id: 'c00' }, { type: 'leaf', id: '__empty_g_1' }],
    })
  })

  it('drops a split child that converts to nothing', () => {
    const local = toLocal(tree({
      nodeId: 's',
      kind: NodeKind.SPLIT,
      children: [tree({ nodeId: 'a' }), tree({ nodeId: '' })],
    }))
    expect(local).toMatchObject({ children: [{ type: 'leaf', id: 'a' }] })
  })

  it('copies the ratio arrays out rather than aliasing the projection', () => {
    const rt = tree({ nodeId: 's', kind: NodeKind.SPLIT, ratios: [0.5, 0.5], children: [tree({ nodeId: 'a' }), tree({ nodeId: 'b' })] })
    const local = toLocal(rt)
    expect(local).toMatchObject({ ratios: [0.5, 0.5] })
    expect((local as { ratios: number[] }).ratios).not.toBe(rt.ratios)
  })

  /**
   * The memo is not an optimisation detail: `ProjectionCache` hands back the same
   * `RenderTree` for any subtree a CRDT tick left alone, and this is what carries
   * that identity into the shape the renderer keys on. `layout.store`'s
   * `tileOrderFor` WeakMap and `floatingWindow.store`'s `reconcile` both rest on
   * it.
   */
  it('returns the same local node for the same projected node', () => {
    const rt = tree({ nodeId: 'leaf' })
    expect(toLocal(rt)).toBe(toLocal(rt))
  })

  it('returns the same subtree objects when only an ancestor was rebuilt', () => {
    const child = tree({ nodeId: 'a' })
    const first = toLocal(tree({ nodeId: 's', kind: NodeKind.SPLIT, ratios: [0.5, 0.5], children: [child, tree({ nodeId: 'b' })] }))
    // What a ratio drag produces: a fresh parent over the very same children.
    const second = toLocal(tree({ nodeId: 's', kind: NodeKind.SPLIT, ratios: [0.3, 0.7], children: [child, tree({ nodeId: 'b' })] }))
    expect(second).not.toBe(first)
    expect((second as { children: unknown[] }).children[0]).toBe((first as { children: unknown[] }).children[0])
  })

  /**
   * The memo is keyed on the `RenderTree` OBJECT, never on `nodeId`. Keying on
   * the id would look equivalent -- ids are unique in a live tree -- but the
   * projection is total over states the hub rejects, and two workspaces naming
   * the same root is one it must answer for (see `registeredRoots`). Under an
   * id-keyed memo the second conversion would be served the first one's node.
   */
  it('does not conflate two different nodes that share an id', () => {
    const asLeaf = toLocal(tree({ nodeId: 'x' }))
    const asSplit = toLocal(tree({
      nodeId: 'x',
      kind: NodeKind.SPLIT,
      ratios: [0.5, 0.5],
      children: [tree({ nodeId: 'c1' }), tree({ nodeId: 'c2' })],
    }))
    expect(asLeaf?.type).toBe('leaf')
    expect(asSplit?.type, 'the second conversion is its own node, not the first one').toBe('split')
  })
})
