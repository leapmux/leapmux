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

  /**
   * A registry that could not be LOADED is not an empty registry.
   *
   * The section is hidden when it is empty, so the two rendering identically is
   * what let a worker-side failure take the whole section off screen with
   * nothing to say why -- a database missing a column did exactly that, and the
   * only trace was a slog.Warn nobody was reading.
   */
  describe('load failure', () => {
    it('reports no failure until one is recorded', () => {
      const store = createBackgroundTaskStore()
      expect(store.loadFailed('a1')).toBe(false)
      store.replace('a1', [])
      expect(store.loadFailed('a1'), 'an empty registry is not a failure').toBe(false)
    })

    it('records a failure per agent', () => {
      const store = createBackgroundTaskStore()
      store.markLoadFailed('a1')
      expect(store.loadFailed('a1')).toBe(true)
      expect(store.loadFailed('a2'), 'one agent\'s failure is not another\'s').toBe(false)
    })

    // The retry that succeeds is the one that clears it, and an EMPTY successful
    // answer still counts: it says the registry is readable again. Clearing only
    // on a non-empty answer would leave the error on screen for an agent whose
    // tasks were all reclaimed.
    it('clears the failure on the next successful load, empty or not', () => {
      const store = createBackgroundTaskStore()
      store.markLoadFailed('a1')
      store.replace('a1', [])
      expect(store.loadFailed('a1')).toBe(false)

      store.markLoadFailed('a2')
      store.replace('a2', [proto('t1', BackgroundTaskStatus.RUNNING)])
      expect(store.loadFailed('a2')).toBe(false)
    })

    // The identical-rebroadcast short-circuit returns BEFORE the row write, so
    // the flag has to be cleared ahead of it or a recovery whose rows happen to
    // match the last good ones never clears the error.
    it('clears the failure even when the rows are unchanged', () => {
      const store = createBackgroundTaskStore()
      store.replace('a1', [proto('t1', BackgroundTaskStatus.RUNNING)])
      store.markLoadFailed('a1')
      store.replace('a1', [proto('t1', BackgroundTaskStatus.RUNNING)])
      expect(store.loadFailed('a1')).toBe(false)
    })

    it('drops the failure with the agent', () => {
      const store = createBackgroundTaskStore()
      store.markLoadFailed('a1')
      store.remove('a1')
      expect(store.loadFailed('a1')).toBe(false)

      store.markLoadFailed('a2')
      store.clear('a2')
      expect(store.loadFailed('a2')).toBe(false)
    })
  })
})
