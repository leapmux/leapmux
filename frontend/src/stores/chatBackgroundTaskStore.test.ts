import type { BackgroundTaskItem as ProtoBackgroundTaskItem } from '~/generated/leapmux/v1/agent_pb'
import { describe, expect, it } from 'vitest'
import { BackgroundTaskKind, BackgroundTaskStatus } from '~/generated/leapmux/v1/agent_pb'
import { createBackgroundTaskStore } from '~/stores/chatBackgroundTaskStore'

function proto(id: string, status: BackgroundTaskStatus, activeForm = ''): ProtoBackgroundTaskItem {
  return {
    id,
    kind: BackgroundTaskKind.SUBAGENT,
    status,
    title: id,
    activeForm,
    childAgentId: '',
    parentAgentId: '',
    groupKey: '',
    groupLabel: '',
    description: '',
    createdAt: '',
    updatedAt: '',
    endedAt: '',
  } as ProtoBackgroundTaskItem
}

describe('createBackgroundTaskStore', () => {
  it('replace applies the proto snapshot', () => {
    const store = createBackgroundTaskStore()
    store.replace('a1', [proto('t1', BackgroundTaskStatus.RUNNING, 'working')])
    expect(store.get('a1')).toHaveLength(1)
    expect(store.get('a1')[0].status).toBe('running')
    expect(store.get('a1')[0].activity).toBe('working')
  })

  it('replace skips an identical re-broadcast', () => {
    const store = createBackgroundTaskStore()
    store.replace('a1', [proto('t1', BackgroundTaskStatus.RUNNING)])
    const first = store.get('a1')
    store.replace('a1', [proto('t1', BackgroundTaskStatus.RUNNING)])
    expect(store.get('a1')).toBe(first)
  })

  it('replace applies when activity changed', () => {
    const store = createBackgroundTaskStore()
    store.replace('a1', [proto('t1', BackgroundTaskStatus.RUNNING, 'a')])
    store.replace('a1', [proto('t1', BackgroundTaskStatus.RUNNING, 'b')])
    expect(store.get('a1')[0].activity).toBe('b')
  })

  it('remove drops only the targeted agent', () => {
    const store = createBackgroundTaskStore()
    store.replace('a1', [proto('t1', BackgroundTaskStatus.RUNNING)])
    store.replace('a2', [proto('t2', BackgroundTaskStatus.PENDING)])
    store.remove('a1')
    expect(store.get('a1')).toEqual([])
    expect(store.get('a2')).toHaveLength(1)
  })

  it('a first set (undefined prior) always goes through', () => {
    const store = createBackgroundTaskStore()
    store.replace('a1', [])
    expect(store.get('a1')).toEqual([])
  })
})
