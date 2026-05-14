import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import {
  HLCSchema,
  NodeKind,
  WorkspaceContentsRecordSchema,
} from '~/generated/leapmux/v1/org_crdt_pb'
import {
  OrgOpSchema,
  SetNodeRegisterOpSchema,
  SetTabRegisterOpSchema,
  TombstoneNodeOpSchema,
} from '~/generated/leapmux/v1/org_ops_pb'
import { SplitDirection, TabType } from '~/generated/leapmux/v1/workspace_pb'
import { applyOp, newState } from './apply'
import { project } from './project'

function hlc(p: bigint, l: bigint, c: string) {
  return create(HLCSchema, { physical: p, logical: l, clientId: c })
}

function setNodeKindOp(nodeId: string, kind: NodeKind, p: bigint, l: bigint, c: string) {
  return create(OrgOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'setNodeRegister', value: create(SetNodeRegisterOpSchema, { nodeId, field: { case: 'kind', value: kind } }) },
  })
}
function setNodeParentOp(nodeId: string, parentId: string, p: bigint, l: bigint, c: string) {
  return create(OrgOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'setNodeRegister', value: create(SetNodeRegisterOpSchema, { nodeId, field: { case: 'parentId', value: parentId } }) },
  })
}
function setNodeDirOp(nodeId: string, direction: SplitDirection, p: bigint, l: bigint, c: string) {
  return create(OrgOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'setNodeRegister', value: create(SetNodeRegisterOpSchema, { nodeId, field: { case: 'direction', value: direction } }) },
  })
}
function setNodePosOp(nodeId: string, position: string, p: bigint, l: bigint, c: string) {
  return create(OrgOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'setNodeRegister', value: create(SetNodeRegisterOpSchema, { nodeId, field: { case: 'position', value: position } }) },
  })
}
function tombstoneNodeOp(nodeId: string, p: bigint, l: bigint, c: string) {
  return create(OrgOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'tombstoneNode', value: create(TombstoneNodeOpSchema, { nodeId }) },
  })
}
function setTabTileOp(tabId: string, tileId: string, p: bigint, l: bigint, c: string) {
  return create(OrgOpSchema, {
    canonicalHlc: hlc(p, l, c),
    body: { case: 'setTabRegister', value: create(SetTabRegisterOpSchema, { tabType: TabType.AGENT, tabId, field: { case: 'tileId', value: tileId } }) },
  })
}

function seedRoot(workspaceId: string, rootId: string) {
  const state = newState('org')
  state.workspaces[workspaceId] = create(WorkspaceContentsRecordSchema, { workspaceId, rootNodeId: rootId })
  applyOp(state, setNodeKindOp(rootId, NodeKind.LEAF, 1n, 0n, 'seed'))
  return state
}

describe('project', () => {
  it('skips tombstoned children', () => {
    const state = seedRoot('w1', 'root')
    applyOp(state, setNodeKindOp('child', NodeKind.LEAF, 2n, 0n, 'a'))
    applyOp(state, setNodeParentOp('child', 'root', 2n, 1n, 'a'))
    applyOp(state, tombstoneNodeOp('child', 3n, 0n, 'a'))
    const proj = project(state)
    const ws = proj.workspaces.get('w1')
    expect(ws?.mainTree.children.length).toBe(0)
  })

  it('drops orphans whose parent_id chain doesn\'t terminate at a registered root', () => {
    const state = seedRoot('w1', 'root')
    applyOp(state, setNodeKindOp('orphan', NodeKind.LEAF, 2n, 0n, 'a'))
    applyOp(state, setNodeParentOp('orphan', 'ghost', 2n, 1n, 'a'))
    applyOp(state, setTabTileOp('t1', 'orphan', 3n, 0n, 'a'))
    const proj = project(state)
    expect(proj.renderedTabs.find(t => t.tabId === 't1')).toBeUndefined()
    expect(proj.ownedTabs.find(t => t.tabId === 't1')).toBeUndefined()
  })

  it('sPLIT with one live child renders as that child (visual collapse)', () => {
    const state = seedRoot('w1', 'root')
    applyOp(state, setNodeKindOp('root', NodeKind.SPLIT, 2n, 0n, 'a'))
    applyOp(state, setNodeDirOp('root', SplitDirection.HORIZONTAL, 2n, 1n, 'a'))
    applyOp(state, setNodeKindOp('child', NodeKind.LEAF, 3n, 0n, 'a'))
    applyOp(state, setNodeParentOp('child', 'root', 3n, 1n, 'a'))
    applyOp(state, setNodePosOp('child', 'N', 3n, 2n, 'a'))
    const proj = project(state)
    const ws = proj.workspaces.get('w1')
    expect(ws?.mainTree.nodeId).toBe('root')
    expect(ws?.mainTree.kind).toBe(NodeKind.LEAF)
  })

  it('a tab whose tile resolves to a live leaf renders in both views', () => {
    const state = seedRoot('w1', 'root')
    applyOp(state, setTabTileOp('t1', 'root', 5n, 0n, 'a'))
    const proj = project(state)
    expect(proj.ownedTabs.length).toBe(1)
    expect(proj.renderedTabs.length).toBe(1)
    expect(proj.renderedTabs[0].workspaceId).toBe('w1')
  })
})
