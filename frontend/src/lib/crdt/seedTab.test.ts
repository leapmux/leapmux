import type { CRDTBridge } from './bridge'
import type { OpBatch } from '~/generated/leapmux/v1/org_ops_pb'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { OrgCrdtStateSchema, WorkspaceContentsRecordSchema } from '~/generated/leapmux/v1/org_crdt_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { setCRDTBridge } from './bridge'
import { HLCClock } from './hlc'
import { seedTabIntoNewWorkspace } from './seedTab'

function installBridge(wsId: string, rootNodeId: string | null) {
  const enqueued: OpBatch[] = []
  const state = rootNodeId
    ? create(OrgCrdtStateSchema, {
        orgId: 'o-1',
        workspaces: {
          [wsId]: create(WorkspaceContentsRecordSchema, {
            workspaceId: wsId,
            rootNodeId,
          }),
        },
      })
    : create(OrgCrdtStateSchema, { orgId: 'o-1' })
  const bridge: CRDTBridge = {
    orgId: () => 'o-1',
    workspaceId: () => wsId,
    enqueue: (batch) => {
      enqueued.push(batch)
      return batch.batchId
    },
    clock: () => new HLCClock('c-1'),
    originClientId: () => 'c-1',
    speculativeState: () => state,
  }
  setCRDTBridge(bridge)
  return { enqueued, bridge }
}

describe('seedTabIntoNewWorkspace', () => {
  it('enqueues a 3-op SetTabRegister batch (tile_id + position + worker_id) and returns the seed root', async () => {
    const { enqueued } = installBridge('ws-1', 'root-leaf-1')
    const result = await seedTabIntoNewWorkspace({
      workspaceId: 'ws-1',
      tabType: TabType.AGENT,
      tabId: 'agent-1',
      workerId: 'w-1',
      timeoutMs: 250,
    })
    expect(result).not.toBeNull()
    expect(result?.rootNodeId).toBe('root-leaf-1')
    expect(result?.position).toBeTruthy()
    expect(enqueued).toHaveLength(1)
    const ops = enqueued[0].ops
    expect(ops).toHaveLength(3)
    const cases = ops.map(o => o.body.case)
    expect(cases.every(c => c === 'setTabRegister')).toBe(true)
    const fieldCases = ops.map((o) => {
      if (o.body.case !== 'setTabRegister')
        return null
      return o.body.value.field?.case
    })
    expect(fieldCases).toEqual(['tileId', 'position', 'workerId'])
    if (ops[0].body.case === 'setTabRegister' && ops[0].body.value.field?.case === 'tileId')
      expect(ops[0].body.value.field.value).toBe('root-leaf-1')
    if (ops[1].body.case === 'setTabRegister' && ops[1].body.value.field?.case === 'position')
      expect(ops[1].body.value.field.value).toBe(result?.position)
  })

  it('returns null when the bridge is unwired', async () => {
    setCRDTBridge(null)
    const result = await seedTabIntoNewWorkspace({
      workspaceId: 'ws-1',
      tabType: TabType.AGENT,
      tabId: 'agent-1',
      timeoutMs: 100,
    })
    expect(result).toBeNull()
  })

  it('times out and returns null when the workspace has no root_node_id', async () => {
    installBridge('ws-1', null)
    const result = await seedTabIntoNewWorkspace({
      workspaceId: 'ws-1',
      tabType: TabType.AGENT,
      tabId: 'agent-1',
      timeoutMs: 100,
    })
    expect(result).toBeNull()
  })

  it('omits worker_id op when no workerId is provided', async () => {
    const { enqueued } = installBridge('ws-1', 'root-leaf-2')
    const result = await seedTabIntoNewWorkspace({
      workspaceId: 'ws-1',
      tabType: TabType.TERMINAL,
      tabId: 'term-1',
      timeoutMs: 250,
    })
    expect(result).not.toBeNull()
    expect(result?.rootNodeId).toBe('root-leaf-2')
    expect(enqueued).toHaveLength(1)
    expect(enqueued[0].ops).toHaveLength(2)
  })
})
