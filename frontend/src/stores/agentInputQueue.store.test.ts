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

  it('rejects a late snapshot after close and accepts one after revival', () => {
    const live = new Set(['a1'])
    const store = createAgentInputQueueStore(agentId => live.has(agentId))
    expect(store.apply(create(AgentInputQueueSnapshotSchema, { agentId: 'a1', revision: 1n }))).toBe(true)

    live.delete('a1')
    store.clearAgent('a1')
    expect(store.apply(create(AgentInputQueueSnapshotSchema, { agentId: 'a1', revision: 2n }))).toBe(false)
    expect(store.get('a1')).toBeUndefined()

    live.add('a1')
    expect(store.apply(create(AgentInputQueueSnapshotSchema, { agentId: 'a1', revision: 3n }))).toBe(true)
  })
})

// Maturity raises the revision, and the store's revision ordering is what
// makes the matured ordering win over a pre-deadline snapshot that arrives
// late.
describe('agent input queue store maturity', () => {
  it('replaces the pre-deadline ordering with a higher-revision maturity snapshot', () => {
    const store = createAgentInputQueueStore()
    const group = '2030-01-01T09:00:00.000Z'
    const preDeadlineItems = () => [
      { id: 'future', reorderGroup: group },
      { id: 'now', reorderGroup: '' },
    ]
    // A fresh decode per event, exactly as a Worker push arrives: the store
    // keeps one stored node per agent, so re-applying one instance would read
    // the store's own merged fields back.
    expect(store.apply(create(AgentInputQueueSnapshotSchema, {
      agentId: 'a1',
      revision: 4n,
      items: preDeadlineItems(),
    }))).toBe(true)
    expect(store.apply(create(AgentInputQueueSnapshotSchema, {
      agentId: 'a1',
      revision: 5n,
      items: [
        { id: 'now', reorderGroup: '' },
        { id: 'future', reorderGroup: '' },
      ],
    }))).toBe(true)
    expect(store.get('a1')?.items.map(row => row.id)).toEqual(['now', 'future'])

    // An older snapshot cannot undo the matured order.
    expect(store.apply(create(AgentInputQueueSnapshotSchema, {
      agentId: 'a1',
      revision: 4n,
      items: preDeadlineItems(),
    }))).toBe(false)
    expect(store.get('a1')?.items.map(row => row.id)).toEqual(['now', 'future'])
  })
})
