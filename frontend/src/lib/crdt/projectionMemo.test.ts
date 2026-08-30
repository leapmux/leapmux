import type { UserCrdtState } from '~/generated/proto/leapmux/v1/user_crdt_pb'
import { create } from '@bufbuild/protobuf'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { HLCSchema, NodeKind, WorkspaceContentsRecordSchema } from '~/generated/proto/leapmux/v1/user_crdt_pb'
import { CrdtOpSchema, SetNodeRegisterOpSchema } from '~/generated/proto/leapmux/v1/user_ops_pb'
import { applyOp, newState } from './apply'
import { createProjectionMemo } from './projectionMemo'

function seeded(): UserCrdtState {
  const state = newState('user')
  state.workspaces.w1 = create(WorkspaceContentsRecordSchema, { workspaceId: 'w1', rootNodeId: 'root' })
  applyOp(state, create(CrdtOpSchema, {
    canonicalHlc: create(HLCSchema, { physical: 1n, logical: 0n, clientId: 'seed' }),
    body: {
      case: 'setNodeRegister',
      value: create(SetNodeRegisterOpSchema, { nodeId: 'root', field: { case: 'kind', value: NodeKind.LEAF } }),
    },
  }))
  return state
}

describe('createProjectionMemo', () => {
  /**
   * The factory exists because half the `ProjectionCache` contract is not
   * expressible from the cache alone: `begin` evicts by generation, so a caller
   * that STOPS projecting never evicts and pins the last tenant's whole graph.
   * That rule used to live in each call site, and had already drifted -- the
   * shell cleared, the test harness did not.
   */
  it('reuses the projection across ticks and drops it when the state goes away', () => {
    createRoot((dispose) => {
      const state = seeded()
      // `equals: false` mirrors the real accessors: `PendingOpsManager` mutates
      // `speculativeState` in place, so identity never changes.
      const [src, setSrc] = createSignal<UserCrdtState | null>(state, { equals: false })
      const projection = createProjectionMemo(src)

      const first = projection()
      expect(first?.workspaces.get('w1')?.mainTree.nodeId).toBe('root')
      setSrc(state)
      expect(projection(), 'a tick that changed nothing reuses the whole projection').toBe(first)

      setSrc(null)
      expect(projection()).toBeNull()

      setSrc(state)
      const after = projection()
      expect(after, 'the null tick cleared the cache, so nothing is carried over').not.toBe(first)
      expect(after, 'and the answer is otherwise identical').toEqual(first)
      dispose()
    })
  })
})
