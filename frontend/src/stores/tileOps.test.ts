import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import {
  NodeKind,
  WorkspaceContentsRecordSchema,
} from '~/generated/leapmux/v1/org_crdt_pb'
import { SplitDirection, TabType } from '~/generated/leapmux/v1/workspace_pb'
import { applyOp, newState } from '~/lib/crdt/apply'
import { HLCClock } from '~/lib/crdt/hlc'
import {
  setFloatingRootNodeId,
  setFloatingWorkspaceId,
  setNodeCols,
  setNodeDirection,
  setNodeKind,
  setNodeParentId,
  setNodePosition,
  setNodeRatios,
  setNodeRowRatios,
  setNodeRows,
  setTabPosition,
  setTabTileId,
} from '~/lib/crdt/ops'
import { project } from '~/lib/crdt/project'
import { after, first } from '~/lib/lexorank'
import { buildCloseTileOps } from './tileOps'

// Apply ops produced by the production builders. The builders only
// stamp client_hlc; tests stand in for the hub by using each op's
// client_hlc as its canonical_hlc.
function applyBuiltOps(state: ReturnType<typeof newState>, ops: ReadonlyArray<{ clientHlc?: unknown }>): void {
  for (const op of ops as Array<Parameters<typeof applyOp>[1]>) {
    applyOp(state, op, op.clientHlc)
  }
}

function newCtx(clientId = 'cli-A') {
  return {
    orgId: 'org-1',
    originClientId: clientId,
    clock: new HLCClock(clientId),
  }
}

// Inline the op shape from emitSplitTile so the test exercises the
// production split semantics without needing a CRDTBridge. Mirrors
// `layoutOps.ts:emitSplitTile`.
function buildSplitTileOps(
  ctx: ReturnType<typeof newCtx>,
  state: ReturnType<typeof newState>,
  parentTileId: string,
  direction: SplitDirection,
  childA: string,
  childB: string,
) {
  const posA = first()
  const posB = after(posA)
  const tabs = Object.values(state.tabs)
    .filter(t => (t.tileId?.value ?? '') === parentTileId)
    .sort((a, b) => (a.position?.value ?? '').localeCompare(b.position?.value ?? ''))
  const ops = [
    setNodeKind(ctx, parentTileId, NodeKind.SPLIT),
    setNodeDirection(ctx, parentTileId, direction),
    setNodeRatios(ctx, parentTileId, [0.5, 0.5]),
    setNodeKind(ctx, childA, NodeKind.LEAF),
    setNodeParentId(ctx, childA, parentTileId),
    setNodePosition(ctx, childA, posA),
    setNodeKind(ctx, childB, NodeKind.LEAF),
    setNodeParentId(ctx, childB, parentTileId),
    setNodePosition(ctx, childB, posB),
  ]
  tabs.forEach((t, i) => {
    ops.push(setTabTileId(ctx, t.tabType, t.tabId, childA))
    ops.push(setTabPosition(ctx, t.tabType, t.tabId, `pos-${i}`))
  })
  return ops
}

function seedWorkspaceWithTab(orgId: string, wsId: string, rootId: string, tabId: string) {
  const state = newState(orgId)
  state.workspaces[wsId] = create(WorkspaceContentsRecordSchema, { workspaceId: wsId, rootNodeId: rootId })
  const ctx = newCtx()
  applyBuiltOps(state, [
    setNodeKind(ctx, rootId, NodeKind.LEAF),
    setTabTileId(ctx, TabType.AGENT, tabId, rootId),
    setTabPosition(ctx, TabType.AGENT, tabId, 'pos-0'),
  ])
  return { state, ctx }
}

// Summarise an OrgOp as "kind:nodeId" or "field:tab-id:value" so
// tests can assert against an order-independent set without
// unpacking the oneof at every assertion. Mirrors the Go-side
// `opCases` helper in `tile_close_ops_test.go`.
type AnyOp = ReturnType<typeof setNodeKind>
function opCase(op: AnyOp): string {
  const body = op.body
  switch (body.case) {
    case 'tombstoneNode':
      return `tombstoneNode:${body.value.nodeId}`
    case 'tombstoneTab':
      return `tombstoneTab:${body.value.tabId}`
    case 'setNodeRegister': {
      const v = body.value
      const nodeId = v.nodeId
      const f = v.field
      if (f.case === 'kind')
        return `setNodeKind:${nodeId}=${NodeKind[f.value]}`
      return `setNodeRegister:${nodeId}`
    }
    case 'setTabRegister': {
      const v = body.value
      const tabId = v.tabId
      const f = v.field
      if (f.case === 'tileId')
        return `setTabTileId:${tabId}=${f.value}`
      if (f.case === 'position')
        return `setTabPosition:${tabId}`
      return `setTabRegister:${tabId}`
    }
  }
  return 'unknown'
}
function opCases(ops: ReadonlyArray<AnyOp>): string[] {
  return ops.map(opCase)
}

describe('buildCloseTileOps', () => {
  it('keeps the surviving tab visible after split → split → close → close', () => {
    // Step 0: Workspace w1 with tab X on root tile A.
    const { state, ctx } = seedWorkspaceWithTab('org-1', 'w1', 'A', 'X')

    // Step 1: Split A horizontally. A → SPLIT(A_top, A_bot). Tab X
    // migrates to A_top.
    applyBuiltOps(state, buildSplitTileOps(ctx, state, 'A', SplitDirection.HORIZONTAL, 'A_top', 'A_bot'))

    // Step 2: Split A_top vertically. A_top → SPLIT(A_TL, A_TR). Tab
    // X migrates to A_TL.
    applyBuiltOps(state, buildSplitTileOps(ctx, state, 'A_top', SplitDirection.VERTICAL, 'A_TL', 'A_TR'))

    // Step 3: Close A_bot. Sibling A_top is a SPLIT, so no undo-split
    // fires — A is left as a single-child SPLIT and the projection
    // collapses A's render to its only live child A_top (re-keyed to
    // 'A'). The CRDT structure is now A(SPLIT) → A_top(SPLIT) → {A_TL, A_TR}.
    applyBuiltOps(state, buildCloseTileOps(ctx, state, 'A_bot'))

    // Sanity: after step 3 the projection renders a single SPLIT with
    // two leaves and tab X is visible on A_TL.
    {
      const proj = project(state)
      const ws = proj.workspaces.get('w1')!
      expect(ws.mainTree.nodeId).toBe('A')
      expect(ws.mainTree.kind).toBe(NodeKind.SPLIT)
      expect(ws.mainTree.children.map(c => c.nodeId)).toEqual(['A_TL', 'A_TR'])
      const tabX = proj.renderedTabs.find(t => t.tabId === 'X')
      expect(tabX?.tileId).toBe('A_TL')
    }

    // Step 4: Close A_TR. Sibling A_TL is a LEAF → undo-split fires
    // on the immediate parent A_top: tab X migrates from A_TL to
    // A_top, A_TL is tombstoned, A_top flips to LEAF.
    applyBuiltOps(state, buildCloseTileOps(ctx, state, 'A_TR'))

    // Expectation: the user sees a single tile that still holds tab
    // X. The projection's rendered leaf nodeId must match the tab's
    // tileId, otherwise the renderer (which queries
    // `tabStore.getTabsForTile(renderedNodeId)`) shows an empty tile.
    const proj = project(state)
    const ws = proj.workspaces.get('w1')!
    expect(ws.mainTree.kind).toBe(NodeKind.LEAF)
    const tabX = proj.renderedTabs.find(t => t.tabId === 'X')
    expect(tabX).toBeDefined()
    expect(tabX!.tileId).toBe(ws.mainTree.nodeId)
    // The grandparent SPLIT collapsed in the same batch; its kind is
    // now LEAF and the intermediate SPLIT is tombstoned.
    expect(state.nodes.A.kind?.value).toBe(NodeKind.LEAF)
    expect(state.nodes.A_top.tombstoneAt?.physical).toBeTruthy()
  })

  it('collapses an arbitrarily deep single-child SPLIT chain in one batch', () => {
    // Build a chain of three nested splits where each outer SPLIT
    // becomes single-child after its other half is closed:
    //   R(SPLIT) → M(SPLIT) → P(SPLIT, children L + K).
    // Tab X rides the split-migrations all the way down to L. Then
    // close K (the empty side): sibling L is LEAF and holds X, so
    // undo-split fires. With M and R sitting as single-child SPLITs
    // above P, the fix should collapse the whole chain and migrate
    // X up to R.
    // Do all splits first so each level is multi-child at split time
    // (the close-K step at the bottom is what should collapse the
    // chain — the close-other-side steps just have to leave a chain
    // of single-child SPLITs above the closing tile).
    const { state, ctx } = seedWorkspaceWithTab('org-1', 'w1', 'R', 'X')
    applyBuiltOps(state, buildSplitTileOps(ctx, state, 'R', SplitDirection.HORIZONTAL, 'M', 'R_other'))
    applyBuiltOps(state, buildSplitTileOps(ctx, state, 'M', SplitDirection.HORIZONTAL, 'P', 'M_other'))
    applyBuiltOps(state, buildSplitTileOps(ctx, state, 'P', SplitDirection.HORIZONTAL, 'L', 'K'))
    // Close the unrelated empty sides last — sibling is a SPLIT at
    // each step, so close ops just tombstone the empty leaf without
    // touching the chain.
    applyBuiltOps(state, buildCloseTileOps(ctx, state, 'R_other'))
    applyBuiltOps(state, buildCloseTileOps(ctx, state, 'M_other'))

    // Tab X now lives on L (split migration moves tabs to childA at
    // every level: R→M, M→P, P→L).
    expect(state.tabs.X.tileId?.value).toBe('L')

    // Close K (the empty sibling). Sibling L holds X — undo-split
    // fires with L's tabs migrating up, and the single-child chain
    // M and R above P should collapse all the way to R.
    applyBuiltOps(state, buildCloseTileOps(ctx, state, 'K'))

    const proj = project(state)
    const ws = proj.workspaces.get('w1')!
    expect(ws.mainTree.nodeId).toBe('R')
    expect(ws.mainTree.kind).toBe(NodeKind.LEAF)
    expect(state.nodes.R.kind?.value).toBe(NodeKind.LEAF)
    expect(state.nodes.M.tombstoneAt?.physical).toBeTruthy()
    expect(state.nodes.P.tombstoneAt?.physical).toBeTruthy()
    const tabX = proj.renderedTabs.find(t => t.tabId === 'X')
    expect(tabX?.tileId).toBe('R')
  })

  it('stops the upward walk at a SPLIT ancestor that still has another live child', () => {
    // Build: R(SPLIT, children P + R_other) → P(SPLIT, children L + K).
    // Close K (sibling L holds X). The undo-split target is P, but
    // the walk must STOP at R because R is a live multi-child SPLIT
    // (R_other is alive) — projection won't collapse R, so tabs must
    // land on P, not R.
    const { state, ctx } = seedWorkspaceWithTab('org-1', 'w1', 'R', 'X')
    applyBuiltOps(state, buildSplitTileOps(ctx, state, 'R', SplitDirection.HORIZONTAL, 'P', 'R_other'))
    applyBuiltOps(state, buildSplitTileOps(ctx, state, 'P', SplitDirection.HORIZONTAL, 'L', 'K'))
    expect(state.tabs.X.tileId?.value).toBe('L')

    applyBuiltOps(state, buildCloseTileOps(ctx, state, 'K'))

    expect(state.nodes.P.kind?.value).toBe(NodeKind.LEAF)
    expect(state.nodes.R.kind?.value).toBe(NodeKind.SPLIT)
    expect(state.nodes.P.tombstoneAt?.physical).toBeFalsy()
    const proj = project(state)
    const ws = proj.workspaces.get('w1')!
    // Rendered tree: SPLIT R → [P (leaf), R_other (leaf)].
    expect(ws.mainTree.kind).toBe(NodeKind.SPLIT)
    const tabX = proj.renderedTabs.find(t => t.tabId === 'X')
    expect(tabX?.tileId).toBe('P')
  })

  it('collapses single-child SPLIT chain rooted at a floating-window root', () => {
    // Same shape as the workspace-root case but with a floating
    // window's root. The projection treats both root kinds the same
    // (`registeredRoots` indexes both into `roots`), so the fix's
    // upward walk must terminate at a floating root the same way.
    const state = newState('org-1')
    state.workspaces.w1 = create(WorkspaceContentsRecordSchema, { workspaceId: 'w1', rootNodeId: 'wsRoot' })
    const ctx = newCtx()
    applyBuiltOps(state, [
      setNodeKind(ctx, 'wsRoot', NodeKind.LEAF),
      // Seed the floating window with rootNodeId=F and a workspaceId
      // mapping so registeredRoots picks it up.
      setFloatingRootNodeId(ctx, 'fw1', 'F'),
      setFloatingWorkspaceId(ctx, 'fw1', 'w1'),
      setNodeKind(ctx, 'F', NodeKind.LEAF),
      setTabTileId(ctx, TabType.AGENT, 'X', 'F'),
      setTabPosition(ctx, TabType.AGENT, 'X', 'pos-0'),
    ])
    applyBuiltOps(state, buildSplitTileOps(ctx, state, 'F', SplitDirection.HORIZONTAL, 'F_top', 'F_bot'))
    applyBuiltOps(state, buildSplitTileOps(ctx, state, 'F_top', SplitDirection.VERTICAL, 'F_TL', 'F_TR'))
    applyBuiltOps(state, buildCloseTileOps(ctx, state, 'F_bot'))

    expect(state.tabs.X.tileId?.value).toBe('F_TL')

    applyBuiltOps(state, buildCloseTileOps(ctx, state, 'F_TR'))

    // Floating-window root must remain alive (never tombstoned, only
    // kind-flipped), and the tab must land on the surviving rendered
    // leaf id.
    expect(state.nodes.F.tombstoneAt?.physical).toBeFalsy()
    expect(state.nodes.F.kind?.value).toBe(NodeKind.LEAF)
    expect(state.tabs.X.tileId?.value).toBe('F')

    const proj = project(state)
    const tabX = proj.renderedTabs.find(t => t.tabId === 'X')
    expect(tabX?.tileId).toBe('F')
  })

  it('preserves order of multiple sibling tabs migrating up the chain', () => {
    // Sibling holds three tabs (X1, X2, X3) in lexorank order. After
    // the close they should land on the destination tile in the same
    // order with freshly-stamped lexorank positions.
    const state = newState('org-1')
    state.workspaces.w1 = create(WorkspaceContentsRecordSchema, { workspaceId: 'w1', rootNodeId: 'A' })
    const ctx = newCtx()
    applyBuiltOps(state, [
      setNodeKind(ctx, 'A', NodeKind.LEAF),
      setTabTileId(ctx, TabType.AGENT, 'X1', 'A'),
      setTabPosition(ctx, TabType.AGENT, 'X1', 'a0'),
      setTabTileId(ctx, TabType.AGENT, 'X2', 'A'),
      setTabPosition(ctx, TabType.AGENT, 'X2', 'a1'),
      setTabTileId(ctx, TabType.AGENT, 'X3', 'A'),
      setTabPosition(ctx, TabType.AGENT, 'X3', 'a2'),
    ])
    applyBuiltOps(state, buildSplitTileOps(ctx, state, 'A', SplitDirection.HORIZONTAL, 'A_top', 'A_bot'))
    applyBuiltOps(state, buildSplitTileOps(ctx, state, 'A_top', SplitDirection.VERTICAL, 'A_TL', 'A_TR'))
    applyBuiltOps(state, buildCloseTileOps(ctx, state, 'A_bot'))
    applyBuiltOps(state, buildCloseTileOps(ctx, state, 'A_TR'))

    const proj = project(state)
    const ws = proj.workspaces.get('w1')!
    const tabs = proj.renderedTabs.filter(t => t.tabId.startsWith('X'))
    expect(tabs.map(t => t.tabId)).toEqual(['X1', 'X2', 'X3'])
    // Every migrated tab landed on the surviving rendered leaf.
    for (const t of tabs) expect(t.tileId).toBe(ws.mainTree.nodeId)
    // Lexorank positions are strictly ascending in the order X1<X2<X3.
    const positions = tabs.map(t => t.position)
    expect(positions[0] < positions[1]).toBe(true)
    expect(positions[1] < positions[2]).toBe(true)
  })

  it('stops the upward walk at a GRID ancestor', () => {
    // GRIDs don't have the single-child collapse rule, so the chain
    // must terminate when it hits one even if the GRID has only one
    // populated cell. Build: GRID(1×1, single cell P) → P(SPLIT,
    // children L + K). Tab X lands on L. Closing K should migrate X
    // to P, NOT propagate up into the GRID root.
    const state = newState('org-1')
    state.workspaces.w1 = create(WorkspaceContentsRecordSchema, { workspaceId: 'w1', rootNodeId: 'G' })
    const ctx = newCtx()
    applyBuiltOps(state, [
      setNodeKind(ctx, 'G', NodeKind.GRID),
      setNodeRows(ctx, 'G', 1),
      setNodeCols(ctx, 'G', 1),
      setNodeRowRatios(ctx, 'G', [1]),
      setNodeKind(ctx, 'P', NodeKind.LEAF),
      setNodeParentId(ctx, 'P', 'G'),
      setNodePosition(ctx, 'P', '0,0'),
      setTabTileId(ctx, TabType.AGENT, 'X', 'P'),
      setTabPosition(ctx, TabType.AGENT, 'X', 'pos-0'),
    ])
    applyBuiltOps(state, buildSplitTileOps(ctx, state, 'P', SplitDirection.HORIZONTAL, 'L', 'K'))
    expect(state.tabs.X.tileId?.value).toBe('L')

    applyBuiltOps(state, buildCloseTileOps(ctx, state, 'K'))

    // P flips to LEAF; G stays a GRID; no propagation.
    expect(state.nodes.P.kind?.value).toBe(NodeKind.LEAF)
    expect(state.nodes.G.kind?.value).toBe(NodeKind.GRID)
    expect(state.nodes.P.tombstoneAt?.physical).toBeFalsy()
    expect(state.tabs.X.tileId?.value).toBe('P')
  })

  it('tombstones the closing tile\'s own tabs while migrating sibling tabs up the chain', () => {
    // Both tile sides hold tabs when the close fires (the close-tile
    // dialog's "close all tabs" branch). The closing tile's tabs
    // tombstone, the sibling's tabs migrate up the collapsed chain,
    // and the rendered leaf id matches the surviving tabs' tileId.
    const state = newState('org-1')
    state.workspaces.w1 = create(WorkspaceContentsRecordSchema, { workspaceId: 'w1', rootNodeId: 'A' })
    const ctx = newCtx()
    applyBuiltOps(state, [
      setNodeKind(ctx, 'A', NodeKind.LEAF),
      setTabTileId(ctx, TabType.AGENT, 'X', 'A'),
      setTabPosition(ctx, TabType.AGENT, 'X', 'a0'),
    ])
    applyBuiltOps(state, buildSplitTileOps(ctx, state, 'A', SplitDirection.HORIZONTAL, 'A_top', 'A_bot'))
    applyBuiltOps(state, buildSplitTileOps(ctx, state, 'A_top', SplitDirection.VERTICAL, 'A_TL', 'A_TR'))
    applyBuiltOps(state, buildCloseTileOps(ctx, state, 'A_bot'))
    // Place an extra tab Y on A_TR so the close-A_TR step has tabs
    // on BOTH sides of the split.
    applyBuiltOps(state, [
      setTabTileId(ctx, TabType.AGENT, 'Y', 'A_TR'),
      setTabPosition(ctx, TabType.AGENT, 'Y', 'a0'),
    ])
    applyBuiltOps(state, buildCloseTileOps(ctx, state, 'A_TR'))

    // Y is tombstoned (closing tile's tabs always die in the close).
    expect(state.tabs.Y.tombstoneAt?.physical).toBeTruthy()
    // X migrates up the full chain and the rendered leaf carries it.
    const proj = project(state)
    const ws = proj.workspaces.get('w1')!
    expect(ws.mainTree.kind).toBe(NodeKind.LEAF)
    const tabX = proj.renderedTabs.find(t => t.tabId === 'X')
    expect(tabX?.tileId).toBe(ws.mainTree.nodeId)
    expect(proj.renderedTabs.find(t => t.tabId === 'Y')).toBeUndefined()
  })

  // -------- Basic (non-chain) cases — mirror the Go-side
  // `tile_close_ops_test.go` suite so both implementations are held
  // to the same contract. Each case here corresponds to a
  // `TestBuildCloseTileOps_*` function in Go. --------

  it('inverse-split fires with an empty sibling (tabbed parent → close empty new leaf)', () => {
    // T(SPLIT, root) → {childA(leaf with tabs), childB(empty leaf)}.
    // Closing childB: tabs on childA migrate to T, childA tombstoned,
    // T flips back to LEAF.
    const state = newState('org-1')
    state.workspaces.w1 = create(WorkspaceContentsRecordSchema, { workspaceId: 'w1', rootNodeId: 'T' })
    const ctx = newCtx()
    applyBuiltOps(state, [
      setNodeKind(ctx, 'T', NodeKind.SPLIT),
      setNodeKind(ctx, 'childA', NodeKind.LEAF),
      setNodeParentId(ctx, 'childA', 'T'),
      setNodeKind(ctx, 'childB', NodeKind.LEAF),
      setNodeParentId(ctx, 'childB', 'T'),
      setTabTileId(ctx, TabType.TERMINAL, 'tab-1', 'childA'),
      setTabPosition(ctx, TabType.TERMINAL, 'tab-1', 'a0'),
      setTabTileId(ctx, TabType.AGENT, 'tab-2', 'childA'),
      setTabPosition(ctx, TabType.AGENT, 'tab-2', 'a1'),
    ])
    const cases = opCases(buildCloseTileOps(ctx, state, 'childB'))
    expect(cases).toContain('tombstoneNode:childB')
    expect(cases).toContain('setTabTileId:tab-1=T')
    expect(cases).toContain('setTabTileId:tab-2=T')
    expect(cases).toContain('setTabPosition:tab-1')
    expect(cases).toContain('setTabPosition:tab-2')
    expect(cases).toContain('tombstoneNode:childA')
    expect(cases).not.toContain('tombstoneNode:T')
    expect(cases).toContain('setNodeKind:T=LEAF')
  })

  it('inverse-split fires with a tabbed closing tile and empty sibling', () => {
    // Mirror image of the previous: close the tile that holds the
    // tabs. Tabs tombstone; empty sibling tombstones; parent flips
    // back to LEAF.
    const state = newState('org-1')
    state.workspaces.w1 = create(WorkspaceContentsRecordSchema, { workspaceId: 'w1', rootNodeId: 'T' })
    const ctx = newCtx()
    applyBuiltOps(state, [
      setNodeKind(ctx, 'T', NodeKind.SPLIT),
      setNodeKind(ctx, 'childA', NodeKind.LEAF),
      setNodeParentId(ctx, 'childA', 'T'),
      setNodeKind(ctx, 'childB', NodeKind.LEAF),
      setNodeParentId(ctx, 'childB', 'T'),
      setTabTileId(ctx, TabType.TERMINAL, 'tab-1', 'childA'),
      setTabPosition(ctx, TabType.TERMINAL, 'tab-1', 'a0'),
    ])
    const cases = opCases(buildCloseTileOps(ctx, state, 'childA'))
    expect(cases).toContain('tombstoneTab:tab-1')
    expect(cases).toContain('tombstoneNode:childA')
    expect(cases).toContain('tombstoneNode:childB')
    expect(cases).toContain('setNodeKind:T=LEAF')
  })

  it('no inverse-split when the parent is not a SPLIT', () => {
    // Plain leaf-under-leaf-root: closing the child just tombstones it
    // and any tabs. No kind flip, no sibling rewiring.
    const state = newState('org-1')
    state.workspaces.w1 = create(WorkspaceContentsRecordSchema, { workspaceId: 'w1', rootNodeId: 'root' })
    const ctx = newCtx()
    applyBuiltOps(state, [
      // Leave root.kind unset (UNSPECIFIED).
      setNodeKind(ctx, 'orphanChild', NodeKind.LEAF),
      setNodeParentId(ctx, 'orphanChild', 'root'),
      setTabTileId(ctx, TabType.TERMINAL, 'tab-1', 'orphanChild'),
      setTabPosition(ctx, TabType.TERMINAL, 'tab-1', 'a0'),
    ])
    const cases = opCases(buildCloseTileOps(ctx, state, 'orphanChild'))
    expect(cases).toContain('tombstoneTab:tab-1')
    expect(cases).toContain('tombstoneNode:orphanChild')
    for (const c of cases) expect(c.startsWith('setNodeKind')).toBe(false)
    expect(cases).not.toContain('tombstoneNode:root')
  })

  it('no inverse-split when the sibling is a GRID', () => {
    // The validator would reject tombstoning a GRID with live cells +
    // tabs. The plain close path tombstones only the closing leaf;
    // projection's single-child SPLIT collapse renders the GRID at
    // the parent's slot.
    const state = newState('org-1')
    state.workspaces.w1 = create(WorkspaceContentsRecordSchema, { workspaceId: 'w1', rootNodeId: 'T' })
    const ctx = newCtx()
    applyBuiltOps(state, [
      setNodeKind(ctx, 'T', NodeKind.SPLIT),
      setNodeKind(ctx, 'G', NodeKind.GRID),
      setNodeParentId(ctx, 'G', 'T'),
      setNodePosition(ctx, 'G', '0'),
      setNodeRows(ctx, 'G', 1),
      setNodeCols(ctx, 'G', 2),
      setNodeRowRatios(ctx, 'G', [1]),
      setNodeKind(ctx, 'cellA', NodeKind.LEAF),
      setNodeParentId(ctx, 'cellA', 'G'),
      setNodePosition(ctx, 'cellA', '0,0'),
      setNodeKind(ctx, 'cellB', NodeKind.LEAF),
      setNodeParentId(ctx, 'cellB', 'G'),
      setNodePosition(ctx, 'cellB', '0,1'),
      setNodeKind(ctx, 'emptyLeaf', NodeKind.LEAF),
      setNodeParentId(ctx, 'emptyLeaf', 'T'),
      setNodePosition(ctx, 'emptyLeaf', '1'),
      setTabTileId(ctx, TabType.TERMINAL, 'tab-1', 'cellA'),
      setTabPosition(ctx, TabType.TERMINAL, 'tab-1', 'a0'),
      setTabTileId(ctx, TabType.AGENT, 'tab-2', 'cellB'),
      setTabPosition(ctx, TabType.AGENT, 'tab-2', 'a0'),
    ])
    const cases = opCases(buildCloseTileOps(ctx, state, 'emptyLeaf'))
    expect(cases).toContain('tombstoneNode:emptyLeaf')
    expect(cases).not.toContain('tombstoneNode:G')
    expect(cases).not.toContain('tombstoneNode:cellA')
    expect(cases).not.toContain('tombstoneNode:cellB')
    for (const c of cases) {
      expect(c.startsWith('setTabTileId')).toBe(false)
      expect(c.startsWith('setNodeKind')).toBe(false)
    }
    expect(cases).not.toContain('tombstoneTab:tab-1')
    expect(cases).not.toContain('tombstoneTab:tab-2')
  })

  it('no inverse-split when the sibling is a SPLIT', () => {
    // Same reasoning as the GRID-sibling case: a nested SPLIT with
    // live leaves can't be naively tombstoned.
    const state = newState('org-1')
    state.workspaces.w1 = create(WorkspaceContentsRecordSchema, { workspaceId: 'w1', rootNodeId: 'T' })
    const ctx = newCtx()
    applyBuiltOps(state, [
      setNodeKind(ctx, 'T', NodeKind.SPLIT),
      setNodeKind(ctx, 'S', NodeKind.SPLIT),
      setNodeParentId(ctx, 'S', 'T'),
      setNodePosition(ctx, 'S', '0'),
      setNodeKind(ctx, 'leafA', NodeKind.LEAF),
      setNodeParentId(ctx, 'leafA', 'S'),
      setNodePosition(ctx, 'leafA', '0'),
      setNodeKind(ctx, 'leafB', NodeKind.LEAF),
      setNodeParentId(ctx, 'leafB', 'S'),
      setNodePosition(ctx, 'leafB', '1'),
      setNodeKind(ctx, 'emptyLeaf', NodeKind.LEAF),
      setNodeParentId(ctx, 'emptyLeaf', 'T'),
      setNodePosition(ctx, 'emptyLeaf', '1'),
      setTabTileId(ctx, TabType.TERMINAL, 'tab-1', 'leafA'),
      setTabPosition(ctx, TabType.TERMINAL, 'tab-1', 'a0'),
    ])
    const cases = opCases(buildCloseTileOps(ctx, state, 'emptyLeaf'))
    expect(cases).toContain('tombstoneNode:emptyLeaf')
    expect(cases).not.toContain('tombstoneNode:S')
    expect(cases).not.toContain('tombstoneNode:leafA')
    expect(cases).not.toContain('tombstoneNode:leafB')
    for (const c of cases) {
      expect(c.startsWith('setTabTileId')).toBe(false)
      expect(c.startsWith('setNodeKind')).toBe(false)
    }
  })

  it('no inverse-split when the parent SPLIT has three live children', () => {
    // 3-child SPLIT loses one: still a multi-leaf SPLIT after the
    // close, so projection won't collapse and we must not undo-split.
    const state = newState('org-1')
    state.workspaces.w1 = create(WorkspaceContentsRecordSchema, { workspaceId: 'w1', rootNodeId: 'T' })
    const ctx = newCtx()
    applyBuiltOps(state, [
      setNodeKind(ctx, 'T', NodeKind.SPLIT),
      setNodeKind(ctx, 'childA', NodeKind.LEAF),
      setNodeParentId(ctx, 'childA', 'T'),
      setNodeKind(ctx, 'childB', NodeKind.LEAF),
      setNodeParentId(ctx, 'childB', 'T'),
      setNodeKind(ctx, 'childC', NodeKind.LEAF),
      setNodeParentId(ctx, 'childC', 'T'),
      setTabTileId(ctx, TabType.TERMINAL, 'tab-1', 'childA'),
      setTabPosition(ctx, TabType.TERMINAL, 'tab-1', 'a0'),
    ])
    const cases = opCases(buildCloseTileOps(ctx, state, 'childB'))
    expect(cases).toContain('tombstoneNode:childB')
    expect(cases).not.toContain('tombstoneNode:childA')
    expect(cases).not.toContain('tombstoneNode:childC')
    for (const c of cases) expect(c.startsWith('setNodeKind')).toBe(false)
  })
})
