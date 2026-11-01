import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentInputQueuePauseReason, AgentInputQueueSnapshotSchema } from '~/generated/proto/leapmux/v1/agent_pb'
import { createAgentInputQueueStore } from './agentInputQueue.store'

describe('agent input queue store', () => {
  it('ignores an older authoritative snapshot', () => {
    const store = createAgentInputQueueStore()
    const newer = create(AgentInputQueueSnapshotSchema, { revision: 5n, pauseReason: AgentInputQueuePauseReason.MANUAL })
    const older = create(AgentInputQueueSnapshotSchema, { revision: 4n, pauseReason: AgentInputQueuePauseReason.UNSPECIFIED })

    expect(store.apply('a1', newer)).toBe(true)
    expect(store.apply('a1', older)).toBe(false)
    expect(store.get('a1')).toStrictEqual(newer)
  })

  it('accepts an equal revision for RPC reconciliation', () => {
    const store = createAgentInputQueueStore()
    const first = create(AgentInputQueueSnapshotSchema, { revision: 3n, pauseReason: AgentInputQueuePauseReason.UNSPECIFIED })
    const replacement = create(AgentInputQueueSnapshotSchema, { revision: 3n, pauseReason: AgentInputQueuePauseReason.MANUAL })

    store.apply('a1', first)
    expect(store.apply('a1', replacement)).toBe(true)
    expect(store.get('a1')?.pauseReason).toBe(AgentInputQueuePauseReason.MANUAL)
  })

  it('files the snapshot under the agent its carrier states', () => {
    const store = createAgentInputQueueStore()
    const snapshot = create(AgentInputQueueSnapshotSchema, { revision: 1n })

    expect(store.apply('a1', snapshot)).toBe(true)

    expect(store.get('a1')).toStrictEqual(snapshot)
    expect(store.get('a2')).toBeUndefined()
  })

  it('refuses an absent snapshot or an empty agent ID', () => {
    const store = createAgentInputQueueStore()

    expect(store.apply('a1', undefined)).toBe(false)
    expect(store.apply('', create(AgentInputQueueSnapshotSchema, { revision: 1n }))).toBe(false)

    expect(store.state.byAgent).toEqual({})
  })

  it('removes a closed agent without retaining a map key', () => {
    const store = createAgentInputQueueStore()
    store.apply('a1', create(AgentInputQueueSnapshotSchema, { revision: 1n }))

    store.clearAgent('a1')

    expect(store.get('a1')).toBeUndefined()
    expect('a1' in store.state.byAgent).toBe(false)
  })

  it('rejects a late snapshot after close and accepts one after revival', () => {
    const live = new Set(['a1'])
    const store = createAgentInputQueueStore(agentId => live.has(agentId))
    expect(store.apply('a1', create(AgentInputQueueSnapshotSchema, { revision: 1n }))).toBe(true)

    live.delete('a1')
    store.clearAgent('a1')
    expect(store.apply('a1', create(AgentInputQueueSnapshotSchema, { revision: 2n }))).toBe(false)
    expect(store.get('a1')).toBeUndefined()

    live.add('a1')
    expect(store.apply('a1', create(AgentInputQueueSnapshotSchema, { revision: 3n }))).toBe(true)
  })
})

// Maturity raises the revision, and the store's revision ordering is what
// makes the matured ordering win over a pre-deadline snapshot that arrives
// late.
describe('agent input queue store maturity', () => {
  it('replaces the pre-deadline ordering with a higher-revision maturity snapshot', () => {
    const store = createAgentInputQueueStore()
    const preDeadlineItems = () => [
      { id: 'future', reorderSegment: 2 },
      { id: 'now', reorderSegment: 1 },
    ]
    // A fresh decode per event, exactly as a Worker push arrives: the store
    // keeps one stored node per agent, so re-applying one instance would read
    // the store's own merged fields back.
    expect(store.apply('a1', create(AgentInputQueueSnapshotSchema, {
      revision: 4n,
      items: preDeadlineItems(),
    }))).toBe(true)
    expect(store.apply('a1', create(AgentInputQueueSnapshotSchema, {
      revision: 5n,
      items: [
        { id: 'now', reorderSegment: 1 },
        { id: 'future', reorderSegment: 1 },
      ],
    }))).toBe(true)
    expect(store.get('a1')?.items.map(row => row.id)).toEqual(['now', 'future'])

    // An older snapshot cannot undo the matured order.
    expect(store.apply('a1', create(AgentInputQueueSnapshotSchema, {
      revision: 4n,
      items: preDeadlineItems(),
    }))).toBe(false)
    expect(store.get('a1')?.items.map(row => row.id)).toEqual(['now', 'future'])
  })
})
