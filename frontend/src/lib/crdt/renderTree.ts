import type { RenderTree } from './project'
import type { LayoutNodeLocal } from '~/stores/layout.store'
import { NodeKind } from '~/generated/leapmux/v1/org_crdt_pb'
import { SplitDirection } from '~/generated/leapmux/v1/workspace_pb'

/**
 * Convert a CRDT-projected RenderTree (the canonical, authoritative
 * tree shape derived from OrgCrdtState) into the LayoutNodeLocal
 * shape consumed by the renderer + tile predicates. The two shapes
 * carry the same data; LayoutNodeLocal is the discriminated-union
 * form Solid components understand.
 *
 * Returns null only on the trivial empty-projection case (no node id
 * — happens when the workspace's root_node_id hasn't been seeded yet).
 */
export function renderTreeToLocal(rt: RenderTree | undefined): LayoutNodeLocal | null {
  if (!rt || rt.nodeId === '')
    return null
  switch (rt.kind) {
    case NodeKind.SPLIT: {
      // Proto SplitDirection and the renderer's internal SplitOrientation
      // both use the divider-line convention: VERTICAL = a vertical
      // divider (`|`) between two side-by-side panes; HORIZONTAL = a
      // horizontal divider (`-`) between two stacked panes. The mapping
      // is direct.
      const direction = rt.direction === SplitDirection.VERTICAL ? 'vertical' : 'horizontal'
      const children = rt.children
        .map(c => renderTreeToLocal(c))
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
        .map(c => renderTreeToLocal(c))
        // Cells that didn't project to a real node still need a
        // visual placeholder so the renderer can show an empty cell.
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
