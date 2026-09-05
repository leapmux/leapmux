import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentInputQueueSnapshotSchema } from '~/generated/proto/leapmux/v1/agent_pb'
import { createAgentInputQueueStore } from './agentInputQueue.store'

describe('agent input queue store', () => {
  it('ignores an older authoritative snapshot', () => {
    const store = createAgentInputQueueStore()
    const newer = create(AgentInputQueueSnapshotSchema, { agentId: 'a1', revision: 5n, paused: true })
    const older = create(AgentInputQueueSnapshotSchema, { agentId: 'a1', revision: 4n, paused: false })

    expect(store.apply(newer)).toBe(true)
    expect(store.apply(older)).toBe(false)
    expect(store.get('a1')).toStrictEqual(newer)
  })

  it('accepts an equal revision for RPC reconciliation', () => {
    const store = createAgentInputQueueStore()
    const first = create(AgentInputQueueSnapshotSchema, { agentId: 'a1', revision: 3n, paused: false })
    const replacement = create(AgentInputQueueSnapshotSchema, { agentId: 'a1', revision: 3n, paused: true })

    store.apply(first)
    expect(store.apply(replacement)).toBe(true)
    expect(store.get('a1')?.paused).toBe(true)
  })

  it('removes a closed agent without retaining a map key', () => {
    const store = createAgentInputQueueStore()
    store.apply(create(AgentInputQueueSnapshotSchema, { agentId: 'a1', revision: 1n }))

    store.clearAgent('a1')

    expect(store.get('a1')).toBeUndefined()
    expect('a1' in store.state.byAgent).toBe(false)
  })
})
