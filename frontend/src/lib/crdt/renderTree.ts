import type { RenderTree } from './project'
import type { LayoutNodeLocal } from '~/stores/layout.store'
import { NodeKind } from '~/generated/proto/leapmux/v1/user_crdt_pb'
import { SplitDirection } from '~/generated/proto/leapmux/v1/workspace_pb'

/**
 * Converted trees, keyed by the `RenderTree` node they came from.
 *
 * `ProjectionCache` hands back the SAME `RenderTree` object for any subtree the
 * CRDT tick left alone, and this is what turns that into a saving rather than
 * just a shorter comparison: an untouched workspace's local tree, and an
 * untouched floating window's `layoutRoot`, are then identity-stable for free,
 * so `layout.store`'s `localTrees` settles on `===` and
 * `floatingWindow.store`'s `reconcile` finds no diff.
 *
 * Keyed on the `RenderTree`, which IS immutable once `project()` returns it --
 * every field is `readonly` and says so. `LayoutNodeLocal` is NOT, so the nodes
 * handed out of this shared map are read-only by convention: a consumer that
 * hands its conversion to something which MUTATES the node must pass a cache of
 * its own, so the objects being written are private to it. Solid's
 * `reconcile({ key: 'id' })` is exactly such a consumer -- it updates a matched
 * node's fields in place rather than swapping the reference -- which is why
 * `floatingWindow.store` owns a `LocalTreeCache` instead of using this one.
 *
 * A `WeakMap` so entries die with the subtree they describe; nothing to sweep.
 */
export const sharedTrees: LocalTreeCache = new WeakMap()

/**
 * A caller-owned conversion cache. Same shape as the shared one; see it for why
 * a mutating consumer needs its own.
 *
 * Nothing is lost by not sharing: a workspace's `mainTree` and a floating
 * window's `innerTree` are disjoint subtrees -- `emitAddFloatingWindow` seeds
 * every window root with an empty `parent_id`, so no node hangs under both.
 */
export type LocalTreeCache = WeakMap<RenderTree, LayoutNodeLocal>

/**
 * Convert a CRDT-projected RenderTree (the canonical, authoritative
 * tree shape derived from UserCrdtState) into the LayoutNodeLocal
 * shape consumed by the renderer + tile predicates. The two shapes
 * carry the same data; LayoutNodeLocal is the discriminated-union
 * form Solid components understand.
 *
 * Returns null only on the trivial empty-projection case (no node id
 * — happens when the workspace's root_node_id hasn't been seeded yet).
 *
 * `cache` is REQUIRED, deliberately, and there is no default. Nodes handed out
 * of `sharedTrees` are shared across every consumer, so one that writes into a
 * node it received — `reconcile({ key: 'id' })` mutates its target IN PLACE —
 * rewrites the subtree every other holder still references. That is a bug
 * `floatingWindow.store` already paid for once, and it was reachable precisely
 * because opting OUT (naming a private cache) was the thing you had to remember
 * to do. Making the choice explicit turns a convention into a decision the
 * compiler makes every caller take: pass {@link sharedTrees} to read, or your
 * own {@link LocalTreeCache} if anything downstream will write.
 */
export function renderTreeToLocal(rt: RenderTree | undefined, cache: LocalTreeCache): LayoutNodeLocal | null {
  if (!rt || rt.nodeId === '')
    return null
  const cached = cache.get(rt)
  if (cached)
    return cached
  const local = convert(rt, cache)
  cache.set(rt, local)
  return local
}

/**
 * The conversion itself. Total: the only `null` answer is the empty-node-id case
 * its caller handles, which is why the memo above needs no absent-vs-null
 * distinction.
 */
function convert(rt: RenderTree, cache: LocalTreeCache): LayoutNodeLocal {
  switch (rt.kind) {
    case NodeKind.SPLIT: {
      // Proto SplitDirection and the renderer's internal SplitOrientation
      // both use the divider-line convention: VERTICAL = a vertical
      // divider (`|`) between two side-by-side panes; HORIZONTAL = a
      // horizontal divider (`-`) between two stacked panes. The mapping
      // is direct.
      const direction = rt.direction === SplitDirection.VERTICAL ? 'vertical' : 'horizontal'
      const children = rt.children
        .map(c => renderTreeToLocal(c, cache))
        .filter((c): c is LayoutNodeLocal => c !== null)
      return {
        type: 'split',
        id: rt.nodeId,
        direction,
        ratios: [...rt.ratios],
        children,
      }
    }
    case NodeKind.GRID: {
      const cells = rt.children
        .map(c => renderTreeToLocal(c, cache))
        // Cells that didn't project to a real node still need a
        // visual placeholder so the renderer can show an empty cell.
        // Named after the PARENT plus the cell index, which is also why
        // `project()` can share one empty-cell `RenderTree` across every grid.
        .map((c, i) => c ?? { type: 'leaf' as const, id: `__empty_${rt.nodeId}_${i}` })
      return {
        type: 'grid',
        id: rt.nodeId,
        rows: rt.rows,
        cols: rt.cols,
        rowRatios: [...rt.rowRatios],
        colRatios: [...rt.colRatios],
        cells,
      }
    }
    case NodeKind.LEAF:
    default:
      return { type: 'leaf', id: rt.nodeId }
  }
}
